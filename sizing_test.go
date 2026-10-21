// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// SizingTestSuite covers entry-count sizing, lazy slot allocation, Reserve and Reset
type SizingTestSuite struct {
	suite.Suite
}

// checkInvariants verifies the table, LRU list and freelist agree with each other.
func checkInvariants[T any](s *SizingTestSuite, c *Perch[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	s.Require().GreaterOrEqual(len(c.slots), 1)
	s.Require().Nil(c.slots[0], "slots[0] must stay the nil sentinel")
	s.Require().LessOrEqual(len(c.slots)-1, c.cap, "never more slots than capacity")
	s.Require().LessOrEqual(len(c.table), c.cap, "never more keys than capacity")

	// Every table entry points at a slot holding that key
	for key, idx := range c.table {
		s.Require().NotZero(idx)
		s.Require().Less(int(idx), len(c.slots))
		s.Require().Equal(key, c.slots[idx].key)
	}

	// Walking the LRU list from head visits every table entry exactly once
	seen := make(map[uint32]bool)
	var prev uint32
	for idx := c.head; idx != 0; idx = c.slots[idx].next {
		s.Require().False(seen[idx], "cycle in LRU list")
		seen[idx] = true
		s.Require().Equal(prev, c.slots[idx].prev)
		s.Require().Equal(idx, c.table[c.slots[idx].key])
		prev = idx
	}
	s.Require().Equal(prev, c.tail)
	s.Require().Equal(len(c.table), len(seen))

	// The freelist holds only slots that are not in the LRU list
	free := 0
	for idx := c.free; idx != 0; idx = c.slots[idx].next {
		s.Require().False(seen[idx], "slot is both linked and free")
		seen[idx] = true
		free++
	}
	s.Require().LessOrEqual(len(c.table)+free, len(c.slots)-1)
}

func (s *SizingTestSuite) totalAllocDelta(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestNewDoesNotPreallocate is the regression guard for sentrie-sh/sentrie#140:
// creating a large cache must not allocate memory in proportion to its capacity.
func (s *SizingTestSuite) TestNewDoesNotPreallocate() {
	const limit = 64 << 10

	var c1 *Perch[string]
	delta := s.totalAllocDelta(func() { c1 = New[string](10_000_000) })
	s.Less(delta, uint64(limit), "New[string](10_000_000) allocated %d bytes", delta)
	s.Equal(10_000_000, c1.Cap())

	// The shapes sentrie uses
	type binding struct {
		name string
		deps []string
	}
	var c2 *Perch[*binding]
	delta = s.totalAllocDelta(func() { c2 = New[*binding](100 << 20) })
	s.Less(delta, uint64(limit), "New[*binding](100<<20) allocated %d bytes", delta)

	var c3 *Perch[any]
	delta = s.totalAllocDelta(func() { c3 = New[any](10 << 20) })
	s.Less(delta, uint64(limit), "New[any](10<<20) allocated %d bytes", delta)

	runtime.KeepAlive(c1)
	runtime.KeepAlive(c2)
	runtime.KeepAlive(c3)
}

// TestSlotsGrowOneAtATime checks that entries are allocated one per new key up
// to capacity, and that eviction only starts once the cache is full.
func (s *SizingTestSuite) TestSlotsGrowOneAtATime() {
	const capacity = 5
	c := New[string](capacity)
	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) { return "v-" + k, nil }

	s.Equal(1, len(c.slots))
	for i := 0; i < capacity; i++ {
		key := fmt.Sprintf("k%d", i)
		v, hit, err := c.Get(s.T().Context(), key, ttl, loader)
		s.NoError(err)
		s.False(hit)
		s.Equal("v-"+key, v)
		s.Equal(i+2, len(c.slots), "one new slot per new key")
		s.Equal(i+1, c.Stats().Size)
		checkInvariants(s, c)
	}

	// Hitting an existing key does not grow
	_, hit, err := c.Get(s.T().Context(), "k0", ttl, loader)
	s.NoError(err)
	s.True(hit)
	s.Equal(capacity+1, len(c.slots))

	// At capacity: the next new key evicts the LRU tail (k1, since k0 was just used)
	_, _, err = c.Get(s.T().Context(), "k5", ttl, loader)
	s.NoError(err)
	s.Equal(capacity+1, len(c.slots), "no growth beyond capacity")
	s.Equal(capacity, c.Stats().Size)
	_, found := c.Peek("k1")
	s.False(found, "k1 should have been evicted")
	_, found = c.Peek("k0")
	s.True(found)
	v, found := c.Peek("k5")
	s.True(found)
	s.Equal("v-k5", v)
	checkInvariants(s, c)

	// Deleting frees a slot that is reused before growing or evicting
	c.Delete("k2")
	s.Equal(capacity-1, c.Stats().Size)
	_, _, err = c.Get(s.T().Context(), "k6", ttl, loader)
	s.NoError(err)
	s.Equal(capacity+1, len(c.slots))
	s.Equal(capacity, c.Stats().Size)
	for _, key := range []string{"k0", "k3", "k4", "k5", "k6"} {
		_, found := c.Peek(key)
		s.True(found, "%s should be present", key)
	}
	checkInvariants(s, c)
}

// TestReserveIdempotent checks that Reserve allocates up to capacity once and
// that repeated calls change nothing.
func (s *SizingTestSuite) TestReserveIdempotent() {
	c := New[string](4)
	c.Reserve()
	s.Equal(5, len(c.slots))
	before := append([]*entry[string](nil), c.slots...)
	free := c.free

	c.Reserve()
	s.Equal(before, c.slots, "second Reserve must not replace entries")
	s.Equal(free, c.free)
	checkInvariants(s, c)

	// Reserve on a partially filled cache keeps existing entries and only adds the rest
	c2 := New[string](4)
	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) { return "v-" + k, nil }
	_, _, _ = c2.Get(s.T().Context(), "a", ttl, loader)
	_, _, _ = c2.Get(s.T().Context(), "b", ttl, loader)
	s.Equal(3, len(c2.slots))
	existing := c2.slots[1]

	c2.Reserve()
	s.Equal(5, len(c2.slots))
	s.Same(existing, c2.slots[1])
	v, found := c2.Peek("a")
	s.True(found)
	s.Equal("v-a", v)
	checkInvariants(s, c2)

	// Filling the reserved slots does not grow further
	_, _, _ = c2.Get(s.T().Context(), "c", ttl, loader)
	_, _, _ = c2.Get(s.T().Context(), "d", ttl, loader)
	_, _, _ = c2.Get(s.T().Context(), "e", ttl, loader)
	s.Equal(5, len(c2.slots))
	s.Equal(4, c2.Stats().Size)
	checkInvariants(s, c2)
}

// TestReservedMatchesLazy runs the same sequence of operations on a reserved
// and a lazily allocated cache and checks they behave identically.
func (s *SizingTestSuite) TestReservedMatchesLazy() {
	lazy := New[string](8)
	reserved := New[string](8)
	reserved.Reserve()

	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) {
		if k == "k13" {
			return "", errors.New("boom")
		}
		return "v-" + k, nil
	}

	type result struct {
		v     string
		hit   bool
		err   bool
		found bool
	}
	run := func(c *Perch[string]) []result {
		var out []result
		for i := 0; i < 200; i++ {
			key := fmt.Sprintf("k%d", (i*7)%23)
			switch {
			case i%11 == 0:
				c.Delete(key)
			case i%5 == 0:
				v, found := c.Peek(key)
				out = append(out, result{v: v, found: found})
			default:
				t := ttl
				if i%13 == 0 {
					t = -1
				}
				v, hit, err := c.Get(s.T().Context(), key, t, loader)
				out = append(out, result{v: v, hit: hit, err: err != nil})
			}
		}
		return out
	}

	s.Equal(run(lazy), run(reserved))
	ls, rs := lazy.Stats(), reserved.Stats()
	s.Equal(ls, rs)
	checkInvariants(s, lazy)
	checkInvariants(s, reserved)
}

// TestResetThenReuse checks that Reset returns the cache to a clean,
// unallocated state and that it keeps working (including evictions) afterwards.
func (s *SizingTestSuite) TestResetThenReuse() {
	for _, reserve := range []bool{false, true} {
		s.Run(fmt.Sprintf("reserve=%v", reserve), func() {
			c := New[string](3)
			if reserve {
				c.Reserve()
			}
			ttl := 5 * time.Minute
			loader := func(ctx context.Context, k string) (string, error) { return "v-" + k, nil }

			for i := 0; i < 5; i++ {
				_, _, err := c.Get(s.T().Context(), fmt.Sprintf("old%d", i), ttl, loader)
				s.NoError(err)
			}
			c.Delete("old4")

			c.Reset()
			s.Equal(1, len(c.slots))
			s.Zero(c.head)
			s.Zero(c.tail)
			s.Zero(c.free)
			s.Equal(CacheStats{Capacity: 3}, c.Stats())
			for i := 0; i < 5; i++ {
				_, found := c.Peek(fmt.Sprintf("old%d", i))
				s.False(found)
			}

			// Fill past capacity again
			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("new%d", i)
				v, hit, err := c.Get(s.T().Context(), key, ttl, loader)
				s.NoError(err)
				s.False(hit)
				s.Equal("v-"+key, v)
				checkInvariants(s, c)
			}
			s.Equal(4, len(c.slots))
			s.Equal(3, c.Stats().Size)
			for i, want := range []bool{false, false, true, true, true} {
				v, found := c.Peek(fmt.Sprintf("new%d", i))
				s.Equal(want, found, "new%d", i)
				if found {
					s.Equal(fmt.Sprintf("v-new%d", i), v)
				}
			}

			// Delete and re-add still works
			c.Delete("new3")
			_, _, err := c.Get(s.T().Context(), "new5", ttl, loader)
			s.NoError(err)
			_, found := c.Peek("new5")
			s.True(found)
			checkInvariants(s, c)
		})
	}
}

// blockingLoader returns a loader that signals started and then waits for release.
func blockingLoader(started chan<- struct{}, release <-chan struct{}, val string, err error) Loader[string] {
	return func(ctx context.Context, k string) (string, error) {
		close(started)
		<-release
		return val, err
	}
}

// TestLoadFinishingAfterReset checks that a load that started before Reset does
// not unlink a slot that has since been reused for the same key and index.
func (s *SizingTestSuite) TestLoadFinishingAfterReset() {
	c := New[string](2)
	ttl := 5 * time.Minute
	started, release := make(chan struct{}), make(chan struct{})

	done := make(chan error)
	go func() {
		_, _, err := c.Get(context.Background(), "a", ttl, blockingLoader(started, release, "", errors.New("old load failed")))
		done <- err
	}()
	<-started

	c.Reset()
	// "a" lands in slot 1 again, but in a new entry
	v, _, err := c.Get(s.T().Context(), "a", ttl, func(ctx context.Context, k string) (string, error) { return "fresh", nil })
	s.NoError(err)
	s.Equal("fresh", v)

	close(release)
	s.EqualError(<-done, "old load failed")

	v, found := c.Peek("a")
	s.True(found, "the fresh entry must survive the stale load finishing")
	s.Equal("fresh", v)
	checkInvariants(s, c)
}

// TestEvictWhileLoading checks that evicting or deleting a key whose load is in
// flight does not let that load's result leak into the slot's next occupant.
func (s *SizingTestSuite) TestEvictWhileLoading() {
	ttl := 5 * time.Minute
	fast := func(ctx context.Context, k string) (string, error) { return "v-" + k, nil }

	for _, mode := range []string{"evict", "delete"} {
		s.Run(mode, func() {
			c := New[string](1)
			started, release := make(chan struct{}), make(chan struct{})
			type res struct {
				v   string
				err error
			}
			done := make(chan res)
			go func() {
				v, _, err := c.Get(context.Background(), "a", ttl, blockingLoader(started, release, "v-a", nil))
				done <- res{v, err}
			}()
			<-started

			if mode == "delete" {
				c.Delete("a")
			}
			// "b" takes slot 1 (evicting "a" if it is still there)
			v, _, err := c.Get(s.T().Context(), "b", ttl, fast)
			s.NoError(err)
			s.Equal("v-b", v)

			close(release)
			r := <-done
			s.NoError(r.err)
			s.Equal("v-a", r.v, "the evicted load still returns its own value")

			v, found := c.Peek("b")
			s.True(found)
			s.Equal("v-b", v, "b must not see a's late result")
			_, found = c.Peek("a")
			s.False(found)
			v, hit, err := c.Get(s.T().Context(), "b", ttl, fast)
			s.NoError(err)
			s.True(hit)
			s.Equal("v-b", v)
			checkInvariants(s, c)
		})
	}
}

// TestConcurrentGrowthAndEviction hammers a cache with many distinct keys so it
// grows from empty to full and then evicts, all concurrently. Run with -race.
func (s *SizingTestSuite) TestConcurrentGrowthAndEviction() {
	const (
		capacity   = 128
		goroutines = 32
		perG       = 2000
		keySpace   = 1024
	)
	c := New[string](capacity)
	loader := func(ctx context.Context, k string) (string, error) {
		if len(k)%7 == 0 {
			runtime.Gosched()
		}
		return "v-" + k, nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < perG; i++ {
				key := fmt.Sprintf("key-%d", (g*7919+i*31)%keySpace)
				switch i % 17 {
				case 0:
					c.Delete(key)
				case 1:
					if v, found := c.Peek(key); found && v != "v-"+key {
						errs <- fmt.Errorf("Peek(%s) = %q", key, v)
						return
					}
				default:
					ttl := time.Minute
					if i%23 == 0 {
						ttl = -1
					}
					v, _, err := c.Get(ctx, key, ttl, loader)
					if err != nil {
						errs <- fmt.Errorf("Get(%s): %w", key, err)
						return
					}
					if v != "v-"+key {
						errs <- fmt.Errorf("Get(%s) = %q", key, v)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		s.NoError(err)
	}

	s.Equal(capacity+1, len(c.slots), "cache should have grown to capacity")
	s.LessOrEqual(c.Stats().Size, capacity)
	checkInvariants(s, c)
}

// TestHitDoesNotAllocate checks that a cache hit still allocates nothing.
func (s *SizingTestSuite) TestHitDoesNotAllocate() {
	for _, reserve := range []bool{false, true} {
		s.Run(fmt.Sprintf("reserve=%v", reserve), func() {
			c := New[string](16)
			if reserve {
				c.Reserve()
			}
			ctx := context.Background()
			ttl := 5 * time.Minute
			loader := func(ctx context.Context, k string) (string, error) { return "value", nil }
			_, _, err := c.Get(ctx, "key", ttl, loader)
			s.NoError(err)
			_, _, err = c.Get(ctx, "other", ttl, loader)
			s.NoError(err)

			allocs := testing.AllocsPerRun(1000, func() {
				_, _, _ = c.Get(ctx, "key", ttl, loader)
			})
			s.Zero(allocs, "cache hit should not allocate")
		})
	}
}

// TestSizingTestSuite runs the test suite
func TestSizingTestSuite(t *testing.T) {
	suite.Run(t, new(SizingTestSuite))
}
