// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package perch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

type Loader[T any] func(context.Context, string) (T, error)

// getTypeSize returns the size in bytes of type T
func getTypeSize[T any]() uintptr {
	var zero T
	return unsafe.Sizeof(zero)
}

// Perch is a bounded, per-key TTL, singleflight, zero-alloc-on-hit LRU cache.
// The capacity is specified in bytes and the number of slots is calculated based on the size of type T.
// The memory footprint is calculated and pre-allocated to avoid resizing and memory reallocation.
type Perch[T any] struct {
	mu    *sync.Mutex
	cap   int               // number of slots (calculated from capacityBytes / sizeof(T))
	table map[string]uint32 // key -> slot index (1..len(slots)); 0 means nil / no entry
	slots []entry[T]        // 1-based addressing to keep 0 = null

	// intrusive doubly-linked LRU list using indices
	head uint32 // MRU
	tail uint32 // LRU

	free        uint32     // head of freelist (stack of indices)
	reserveOnce *sync.Once // ensures Reserve() only runs once

	// hit rate statistics
	hits   uint64 // number of cache hits
	misses uint64 // number of cache misses
}

type entry[T any] struct {
	// per-entry state (guards loading/result fields)
	mu  *sync.Mutex
	cv  *sync.Cond
	key string

	// LRU links (indices into Cache.slots)
	prev, next uint32

	// value+state
	val     T
	expires time.Time
	loading bool
	err     error
	inuse   bool // true once inserted in table (occupied)
}

// New creates a new Perch cache with the specified capacity in bytes
// The actual number of slots is calculated as capacityBytes / sizeof(T).
func New[T any](capacityBytes int) *Perch[T] {
	if capacityBytes <= 0 {
		panic("capacity must be > 0")
	}

	// Calculate the number of slots based on the size of type T
	typeSize := getTypeSize[T]()
	if typeSize == 0 {
		// For zero-sized types, use a reasonable default
		typeSize = 1
	}

	// Calculate number of slots that fit in the given capacity - round down
	capacityCount := int(capacityBytes) / int(typeSize)
	if capacityCount == 0 {
		// Ensure we can store at least one entry
		capacityCount = 1
	}

	c := &Perch[T]{
		mu:          &sync.Mutex{},
		cap:         capacityCount,
		table:       make(map[string]uint32, capacityCount*2), // Pre-allocate with capacity hint
		slots:       make([]entry[T], 0, capacityCount+1),     // 1-based indexing, start empty
		reserveOnce: &sync.Once{},
	}

	return c
}

// Cap returns the number of slots in the cache
func (c *Perch[T]) Cap() int {
	return c.cap
}

// Reset resets the cache to its initial state
func (c *Perch[T]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.table = make(map[string]uint32, c.cap*2)
	c.slots = make([]entry[T], 0, c.cap+1)
	c.reserveOnce = &sync.Once{}
	c.hits = 0
	c.misses = 0
}

// Reserve allocates all the slots required for the cache
// This MUST be called before the cache is used
// Safe to call multiple times - only allocates once
func (c *Perch[T]) Reserve() {
	c.reserveOnce.Do(func() {
		// Allocate all slots upfront
		c.slots = make([]entry[T], c.cap+1) // 1-based indexing

		// Initialize all slots and build freelist
		for i := 1; i <= c.cap; i++ {
			e := &c.slots[i]
			e.mu = &sync.Mutex{}
			e.cv = sync.NewCond(e.mu)
			e.prev = 0
			e.next = 0
			e.inuse = false

			// Add to freelist
			e.next = c.free
			c.free = uint32(i)
		}
	})
}

// Get returns cached value if present+fresh. Otherwise calls loader once per key,
// caches result with TTL >= 0, and returns it. ttl==0 means "cache indefinitely", ttl<0 means "do not cache".
// When ttl<0, the cache is bypassed and the loader is always called (preserving singleflight for concurrent requests).
// Returns (value, cacheHit, error) where cacheHit indicates if the value was found in cache.
func (c *Perch[T]) Get(ctx context.Context, key string, ttl time.Duration, loader Loader[T]) (T, bool, error) {
	c.Reserve()

	var zero T
	now := time.Now() // cache this for perf

	// Fast path: lookup under global lock.
	c.mu.Lock()
	if idx := c.table[key]; idx != 0 {
		e := &c.slots[idx] // 1-based indexing
		// Take entry lock to check freshness or wait if loading.
		e.mu.Lock()

		// If another goroutine is loading this key, wait.
		for e.loading {
			e.cv.Wait()
		}
		// After waiting, check if we got a value or error
		// If there was an error, return it
		if e.err != nil {
			err := e.err
			e.mu.Unlock()
			c.mu.Unlock()
			return *new(T), false, err
		}

		// If ttl < 0, handle "do not cache" semantics
		if ttl < 0 {
			// If entry has a cached expiry, it was cached from a previous ttl>=0 call
			// Bypass it and force reload to honor "do not cache" semantics
			if e.inuse && !e.expires.IsZero() && now.Before(e.expires) {
				// This is a cached entry, bypass it
				e.loading = true
				e.err = nil
				e.mu.Unlock()
				c.mu.Unlock()
				// proceed to load with this existing slot index
				return c.loadInto(ctx, idx, key, ttl, loader)
			}
			// If expires.IsZero(), this was just loaded with ttl<0
			// Return the value to preserve singleflight (all waiters get same value)
			// Note: inuse might be false if entry was removed, but value is still valid
			if e.expires.IsZero() && e.err == nil {
				v := e.val
				e.mu.Unlock()
				c.mu.Unlock()
				return v, false, nil
			}
		}

		// After waiting, check if we got a value (even if not cached due to ttl<0)
		if e.inuse && e.err == nil {
			// Check if it's a cached (fresh) entry
			if !e.expires.IsZero() && now.Before(e.expires) {
				// copy under e.mu, then release before taking c.mu
				v := e.val
				e.mu.Unlock()

				// bump MRU safely (don't hold e.mu here)
				if c.table[key] == idx { // still same slot?
					c.moveToFront(idx)
				}
				c.hits++ // increment hit counter
				c.mu.Unlock()

				return v, true, nil
			}
		}

		// Stale: we'll (re)load below.
		e.loading = true
		e.err = nil
		e.mu.Unlock()
		c.mu.Unlock()
		// proceed to load with this existing slot index
		return c.loadInto(ctx, idx, key, ttl, loader)
	}
	// Miss: need a slot (either free or evict LRU).
	var idx uint32
	if c.free != 0 {
		// Use a slot from the freelist
		idx = c.free
		c.free = c.slots[idx].next // 1-based indexing
	} else {
		// evict tail (we're at capacity)
		idx = c.tail
		if idx == 0 {
			// shouldn't happen (cap>0)
			c.mu.Unlock()
			return zero, false, errors.New("lru: no slot available")
		}
		et := &c.slots[idx] // 1-based indexing
		// unlink from list
		c.unlink(idx)
		// remove old key from table
		delete(c.table, et.key)
		et.key = ""
		et.inuse = false
	}
	e := &c.slots[idx] // 1-based indexing
	// prepare this slot for the new key
	e.mu.Lock()
	e.key = key
	e.loading = true
	e.err = nil
	e.inuse = true
	// insert at head (MRU) now; if loader fails we’ll clear in place.
	c.linkFront(idx)
	c.table[key] = idx

	// Unlock
	e.mu.Unlock()
	c.mu.Unlock()

	// Load outside of global lock.
	return c.loadInto(ctx, idx, key, ttl, loader)
}

// loadInto performs the loader call for a specific slot index and signals waiters.
func (c *Perch[T]) loadInto(ctx context.Context, idx uint32, key string, ttl time.Duration, loader Loader[T]) (T, bool, error) {
	e := &c.slots[idx] // 1-based indexing

	// wrap the loader - so that we can intercept panics if they happen
	wrappedLoader := func(ctx context.Context, key string) (t T, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("loader panicked: %v", r)
			}
		}()
		return loader(ctx, key)
	}

	// do the load without holding global lock
	val, err := wrappedLoader(ctx, key)

	now := time.Now()

	e.mu.Lock()
	if err != nil {
		// On error, clear expiry and zero value
		e.expires = time.Time{}
		e.val = *new(T) // zero it
		e.err = err
	} else if ttl < 0 {
		// On no-cache request (ttl<0), set value for waiters but mark as uncached
		e.expires = time.Time{}
		e.val = val // keep value for waiters
		e.err = nil
	} else {
		e.expires = now.Add(ttl)
		if ttl == 0 {
			// set expiry to 99 years from now
			e.expires = time.Now().Add(99 * 365 * 24 * time.Hour)
		}
		e.val = val
		e.err = nil
	}
	e.loading = false
	e.mu.Unlock()
	e.cv.Broadcast()

	// If error or ttl<0, remove from cache map/LRU (but still wake up waiters).
	// Note: ttl==0 means cache indefinitely, so we keep it in cache.
	// For errors, waiters already got the error via singleflight, now remove entry
	if err != nil || ttl < 0 {
		c.mu.Lock()
		// Ensure idx still maps to key (it might, unless evicted/raced).
		if c.table[key] == idx {
			delete(c.table, key)
			c.unlink(idx)
			// push to freelist
			e.mu.Lock()
			e.key = ""
			e.inuse = false
			e.mu.Unlock()
			e.next = c.free
			c.free = idx
		}
		c.mu.Unlock()
		if err != nil {
			return *new(T), false, err
		}
		// For ttl<0, return the value (not cached, but singleflight worked)
		return val, false, nil
	}

	// Success with caching: bump to MRU (cheap if already at head).
	c.mu.Lock()
	if c.table[key] == idx {
		c.moveToFront(idx)
	}
	c.misses++ // increment miss counter
	c.mu.Unlock()
	return val, false, nil
}

func (c *Perch[T]) Delete(key string) {
	c.Reserve()

	c.mu.Lock()
	idx := c.table[key]
	if idx == 0 {
		c.mu.Unlock()
		return
	}
	delete(c.table, key)
	c.unlink(idx)
	e := &c.slots[idx] // 1-based indexing
	// free-list push under c.mu
	e.next = c.free
	c.free = idx
	c.mu.Unlock()

	// clean entry state under e.mu (no c.mu here)
	e.mu.Lock()
	e.key = ""
	e.inuse = false
	e.loading = false
	e.err = nil
	e.expires = time.Time{}
	var zero T
	e.val = zero
	e.mu.Unlock()
}

// Peek returns (value, true) only if cached and fresh at the time of call.
func (c *Perch[T]) Peek(key string) (T, bool) {
	c.Reserve()

	now := time.Now()
	c.mu.Lock()
	if idx := c.table[key]; idx != 0 {
		e := &c.slots[idx] // 1-based indexing
		e.mu.Lock()
		c.mu.Unlock()
		fresh := e.inuse && !e.loading && !e.expires.IsZero() && now.Before(e.expires)
		if !fresh {
			e.mu.Unlock()
			var zero T
			return zero, false
		}
		v := e.val
		e.mu.Unlock()
		// (Optional) we could bump to MRU here; omitted to keep Peek read-only.
		return v, true
	}
	c.mu.Unlock()
	var zero T
	return zero, false
}

// --- intrusive LRU helpers (c.mu held) ---

func (c *Perch[T]) unlink(idx uint32) {
	e := &c.slots[idx] // 1-based indexing
	prev, next := e.prev, e.next
	if prev != 0 {
		c.slots[prev].next = next // 1-based indexing
	} else {
		c.head = next
	}
	if next != 0 {
		c.slots[next].prev = prev // 1-based indexing
	} else {
		c.tail = prev
	}
	e.prev, e.next = 0, 0
}

func (c *Perch[T]) linkFront(idx uint32) {
	e := &c.slots[idx] // 1-based indexing
	e.prev = 0
	e.next = c.head
	if c.head != 0 {
		c.slots[c.head].prev = idx // 1-based indexing
	}
	c.head = idx
	if c.tail == 0 {
		c.tail = idx
	}
}

func (c *Perch[T]) moveToFront(idx uint32) {
	if c.head == idx {
		return
	}
	c.unlink(idx)
	c.linkFront(idx)
}

// HitRate returns the current hit rate as a percentage (0.0 to 100.0)
func (c *Perch[T]) HitRate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	total := c.hits + c.misses
	if total == 0 {
		return 0.0
	}
	return float64(c.hits) / float64(total) * 100.0
}

// Stats returns detailed cache statistics
func (c *Perch[T]) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	total := c.hits + c.misses
	var hitRate float64
	if total > 0 {
		hitRate = float64(c.hits) / float64(total) * 100.0
	}

	return CacheStats{
		Hits:     c.hits,
		Misses:   c.misses,
		Total:    total,
		HitRate:  hitRate,
		Capacity: c.cap,
		Size:     len(c.table),
	}
}

// ResetStats resets the hit/miss counters to zero
func (c *Perch[T]) ResetStats() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits = 0
	c.misses = 0
}

// CacheStats contains detailed cache statistics
type CacheStats struct {
	Hits     uint64  // Number of cache hits
	Misses   uint64  // Number of cache misses
	Total    uint64  // Total number of requests (hits + misses)
	HitRate  float64 // Hit rate as a percentage (0.0 to 100.0)
	Capacity int     // Cache capacity in number of slots
	Size     int     // Current number of items in cache
}
