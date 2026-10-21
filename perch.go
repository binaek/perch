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
	"math"
	"sync"
	"time"
)

type Loader[T any] func(context.Context, string) (T, error)

// Perch is a bounded, per-key TTL, singleflight, zero-alloc-on-hit LRU cache.
// The capacity is the maximum number of entries. Entries are allocated lazily as
// keys are inserted, so memory use is proportional to what the cache holds rather
// than to its capacity. Call Reserve to preallocate every entry up front.
type Perch[T any] struct {
	mu    sync.Mutex
	cap   int               // maximum number of entries
	table map[string]uint32 // key -> slot index (1..len(slots)-1); 0 means nil / no entry
	slots []*entry[T]       // 1-based addressing; slots[0] is a nil sentinel. Guarded by mu.

	// intrusive doubly-linked LRU list using indices
	head uint32 // MRU
	tail uint32 // LRU

	free uint32 // head of freelist (stack of indices)

	// hit rate statistics
	hits   uint64 // number of cache hits
	misses uint64 // number of cache misses
}

type entry[T any] struct {
	// per-entry state (guards loading/result fields)
	mu  sync.Mutex
	cv  sync.Cond // cv.L == &mu
	key string

	// LRU links (indices into Cache.slots)
	prev, next uint32

	// value+state
	val     T
	expires time.Time
	loading bool
	gen     uint64 // incremented each time a load starts on this entry
	err     error
	inuse   bool // true once inserted in table (occupied)
}

func newEntry[T any]() *entry[T] {
	e := &entry[T]{}
	e.cv.L = &e.mu
	return e
}

// New creates a new Perch cache that holds at most capacity entries.
// No entries are allocated until they are needed; see Reserve.
// It panics if capacity is <= 0 or greater than math.MaxUint32-1.
func New[T any](capacity int) *Perch[T] {
	if capacity <= 0 {
		panic("perch: capacity must be > 0")
	}
	if uint64(capacity) > math.MaxUint32-1 {
		panic("perch: capacity must be <= math.MaxUint32-1")
	}

	return &Perch[T]{
		cap:   capacity,
		table: make(map[string]uint32),
		slots: make([]*entry[T], 1), // 1-based indexing; slots[0] is the nil sentinel
	}
}

// Cap returns the maximum number of entries the cache can hold
func (c *Perch[T]) Cap() int {
	return c.cap
}

// Reset resets the cache to its initial (empty, unallocated) state.
// Loads in flight when Reset is called still return their results to their
// callers, but those results are not cached.
func (c *Perch[T]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.table = make(map[string]uint32)
	c.slots = make([]*entry[T], 1)
	c.head = 0
	c.tail = 0
	c.free = 0
	c.hits = 0
	c.misses = 0
}

// Reserve preallocates every entry up to the cache's capacity so that later
// misses do not need to allocate one. It is optional: without it, entries are
// allocated one at a time as the cache fills. Calling it again is a no-op.
func (c *Perch[T]) Reserve() {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := len(c.slots) - 1
	if n >= c.cap {
		return
	}

	// One allocation for all remaining entries; they never move because
	// block is never resized.
	block := make([]entry[T], c.cap-n)
	grown := make([]*entry[T], len(c.slots), c.cap+1)
	copy(grown, c.slots)
	c.slots = grown
	for i := range block {
		e := &block[i]
		e.cv.L = &e.mu
		c.slots = append(c.slots, e)

		// Add to freelist
		e.next = c.free
		c.free = uint32(len(c.slots) - 1)
	}
}

// Get returns cached value if present+fresh. Otherwise calls loader once per key,
// caches result with TTL >= 0, and returns it. ttl==0 means "cache indefinitely", ttl<0 means "do not cache".
// When ttl<0, the cache is bypassed and the loader is always called (preserving singleflight for concurrent requests).
// Returns (value, cacheHit, error) where cacheHit indicates if the value was found in cache.
func (c *Perch[T]) Get(ctx context.Context, key string, ttl time.Duration, loader Loader[T]) (T, bool, error) {
	var zero T
	now := time.Now() // cache this for perf

	// Fast path: lookup under global lock.
	c.mu.Lock()
	if idx := c.table[key]; idx != 0 {
		e := c.slots[idx] // 1-based indexing
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
				e.gen++
				e.err = nil
				e.mu.Unlock()
				c.mu.Unlock()
				// proceed to load into this existing entry
				return c.loadInto(ctx, idx, e, key, ttl, loader)
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
		e.gen++
		e.err = nil
		e.mu.Unlock()
		c.mu.Unlock()
		// proceed to load into this existing entry
		return c.loadInto(ctx, idx, e, key, ttl, loader)
	}
	// Miss: need a slot (from the freelist, a newly allocated entry, or by evicting the LRU tail).
	var idx uint32
	if c.free != 0 {
		// Use a slot from the freelist
		idx = c.free
		c.free = c.slots[idx].next // 1-based indexing
	} else if len(c.slots)-1 < c.cap {
		// Below capacity: allocate a new entry
		c.slots = append(c.slots, newEntry[T]())
		idx = uint32(len(c.slots) - 1)
	} else {
		// evict tail (we're at capacity)
		idx = c.tail
		if idx == 0 {
			// shouldn't happen (cap>0)
			c.mu.Unlock()
			return zero, false, errors.New("lru: no slot available")
		}
		c.detach(idx)
	}
	e := c.slots[idx] // 1-based indexing
	// prepare this slot for the new key
	e.mu.Lock()
	e.key = key
	e.loading = true
	e.gen++
	e.err = nil
	e.inuse = true
	// insert at head (MRU) now; if loader fails we’ll clear in place.
	c.linkFront(idx)
	c.table[key] = idx

	// Unlock
	e.mu.Unlock()
	c.mu.Unlock()

	// Load outside of global lock.
	return c.loadInto(ctx, idx, e, key, ttl, loader)
}

// loadInto performs the loader call for entry e (at slot index idx) and signals waiters.
// e is passed in by the caller because c.slots must not be read without c.mu.
func (c *Perch[T]) loadInto(ctx context.Context, idx uint32, e *entry[T], key string, ttl time.Duration, loader Loader[T]) (T, bool, error) {
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
	gen := e.gen // no other load can start on e until loading is cleared below
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
		// Ensure the slot is still ours: the key may have been deleted, evicted
		// or Reset (and the slot index reused) while we were loading, or another
		// Get may have started a new load on this entry after we cleared loading.
		if c.owns(key, idx, e) {
			e.mu.Lock()
			if e.gen == gen {
				delete(c.table, key)
				c.unlink(idx)
				// push to freelist
				e.key = ""
				e.inuse = false
				e.next = c.free
				c.free = idx
			}
			e.mu.Unlock()
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
	if c.owns(key, idx, e) {
		c.moveToFront(idx)
	}
	c.misses++ // increment miss counter
	c.mu.Unlock()
	return val, false, nil
}

func (c *Perch[T]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.table[key]
	if idx == 0 {
		return
	}
	c.detach(idx)
	// free-list push under c.mu
	e := c.slots[idx] // 1-based indexing
	e.next = c.free
	c.free = idx
}

// Peek returns (value, true) only if cached and fresh at the time of call.
func (c *Perch[T]) Peek(key string) (T, bool) {
	now := time.Now()
	c.mu.Lock()
	if idx := c.table[key]; idx != 0 {
		e := c.slots[idx] // 1-based indexing
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

// --- slot helpers (c.mu held) ---

// owns reports whether key still maps to slot idx and that slot still holds e.
// Comparing the entry pointer matters: after a Reset, eviction or Delete the
// same index can be handed to a different entry.
func (c *Perch[T]) owns(key string, idx uint32, e *entry[T]) bool {
	return c.table[key] == idx && c.slots[idx] == e
}

// detach removes the occupant of slot idx from the table and the LRU list and
// leaves a clean entry in the slot, ready for reuse. If the occupant is still
// loading, it is left to its loader (which will see that it no longer owns the
// slot) and a fresh entry takes its place, so the loader cannot write its
// result into a slot that now belongs to another key.
func (c *Perch[T]) detach(idx uint32) {
	e := c.slots[idx] // 1-based indexing
	c.unlink(idx)
	delete(c.table, e.key)

	e.mu.Lock()
	if e.loading {
		e.mu.Unlock()
		c.slots[idx] = newEntry[T]()
		return
	}
	e.key = ""
	e.inuse = false
	e.err = nil
	e.expires = time.Time{}
	var zero T
	e.val = zero
	e.mu.Unlock()
}

// --- intrusive LRU helpers (c.mu held) ---

func (c *Perch[T]) unlink(idx uint32) {
	e := c.slots[idx] // 1-based indexing
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
	e := c.slots[idx] // 1-based indexing
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
	Capacity int     // Cache capacity in number of entries
	Size     int     // Current number of items in cache
}
