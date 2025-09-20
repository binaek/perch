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
func (c *Perch[T]) Reserve() error {
	var err error
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
	return err
}

// Get returns cached value if present+fresh. Otherwise calls loader once per key,
// caches result with TTL > 0, and returns it. ttl<=0 means "do not cache".
// Returns (value, cacheHit, error) where cacheHit indicates if the value was found in cache.
func (c *Perch[T]) Get(ctx context.Context, key string, ttl time.Duration, loader Loader[T]) (T, bool, error) {
	if err := c.Reserve(); err != nil {
		return *new(T), false, err
	}

	var zero T
	now := time.Now() // cache this for perf

	// if the provided ttl is 0, we should not cache
	if ttl <= 0 {
		// do not do anything - just call the loader and get out
		// the rest of this logic does a lot of things to effectively manage contention
		// but we don't need to do that if we're not caching
		val, err := loader(ctx, key)
		// Count as a miss since we're not caching
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return val, false, err
	}

	// Fast path: lookup under global lock.
	c.mu.Lock()
	if idx := c.table[key]; idx != 0 {
		e := &c.slots[idx] // 1-based indexing
		// Take entry lock to check freshness or wait if loading.
		e.mu.Lock()
		c.mu.Unlock()

		// If another goroutine is loading this key, wait.
		for e.loading {
			e.cv.Wait()
		}
		// Fresh?
		if e.inuse && !e.expires.IsZero() && now.Before(e.expires) {
			// copy under e.mu, then release before taking c.mu
			v := e.val
			e.mu.Unlock()

			// bump MRU safely (don't hold e.mu here)
			c.mu.Lock()
			if c.table[key] == idx { // still same slot?
				c.moveToFront(idx)
			}
			c.hits++ // increment hit counter
			c.mu.Unlock()

			return v, true, nil
		}

		// Stale: we'll (re)load below.
		e.loading = true
		e.err = nil
		e.mu.Unlock()
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
	if err != nil || ttl <= 0 {
		// On error or no-cache request, clear expiry to mark as uncached.
		e.expires = time.Time{}
		e.val = *new(T) // zero it
		e.err = err
	} else {
		e.val = val
		e.expires = now.Add(ttl)
		e.err = nil
	}
	e.loading = false
	e.mu.Unlock()
	e.cv.Broadcast()

	// If error or ttl<=0, remove from cache map/LRU (but still woke waiters).
	if err != nil || ttl <= 0 {
		c.mu.Lock()
		// Ensure idx still maps to key (it might, unless evicted/raced).
		if c.table[key] == idx {
			delete(c.table, key)
			c.unlink(idx)
			// push to freelist
			e.key = ""
			e.inuse = false
			e.next = c.free
			c.free = idx
		}
		c.mu.Unlock()
		return *new(T), false, err
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
	if err := c.Reserve(); err != nil {
		return
	}

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
	if err := c.Reserve(); err != nil {
		return *new(T), false
	}

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
