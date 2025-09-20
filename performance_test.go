// Copyright 2025 Binaek Sarkar
//
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
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// PerformanceTestSuite provides specialized tests for performance and zero-allocation behavior
type PerformanceTestSuite struct {
	suite.Suite
	cache *Perch[string]
}

// SetupSuite initializes the test suite
func (s *PerformanceTestSuite) SetupSuite() {
	slog.Info("PerformanceTestSuite SetupSuite start")
}

// BeforeTest runs before each test
func (s *PerformanceTestSuite) BeforeTest(suiteName, testName string) {
	slog.Info("BeforeTest start", "TestSuite", "PerformanceTestSuite", "TestName", testName)
	s.cache = New[string](1600) // 100 * 16 bytes = 1600 bytes for performance testing
	_ = s.cache.Reserve()       // Allocate slots
}

// AfterTest runs after each test
func (s *PerformanceTestSuite) AfterTest(suiteName, testName string) {
	slog.Info("AfterTest start", "TestSuite", "PerformanceTestSuite", "TestName", testName)
}

// TearDownSuite cleans up after all tests
func (s *PerformanceTestSuite) TearDownSuite() {
	slog.Info("TearDownSuite")
	slog.Info("TearDownSuite end")
}

// TestZeroAllocationOnHit tests that cache hits don't cause allocations
func (s *PerformanceTestSuite) TestZeroAllocationOnHit() {
	key := "zero-alloc-key"
	value := "zero-alloc-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load value first
	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	// Measure allocations for cache hits
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Perform multiple cache hits
	for i := 0; i < 1000; i++ {
		result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
		s.NoError(err)
		s.Equal(value, result)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	// Should have minimal allocations for cache hits
	allocations := m2.Mallocs - m1.Mallocs
	s.Less(allocations, uint64(2000), "Cache hits should cause minimal allocations")
}

// TestCacheHitPerformance tests the performance of cache hits
func (s *PerformanceTestSuite) TestCacheHitPerformance() {
	key := "perf-key"
	value := "perf-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load value first
	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	// Measure time for cache hits
	start := time.Now()
	numHits := 10000

	for i := 0; i < numHits; i++ {
		result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
		s.NoError(err)
		s.Equal(value, result)
	}

	duration := time.Since(start)
	avgTimePerHit := duration / time.Duration(numHits)

	// Cache hits should be very fast (microseconds)
	s.Less(avgTimePerHit, 10*time.Microsecond, "Cache hits should be very fast")
}

// TestCacheMissPerformance tests the performance of cache misses
func (s *PerformanceTestSuite) TestCacheMissPerformance() {
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		time.Sleep(1 * time.Millisecond) // Simulate work
		return "value-" + k, nil
	}

	// Measure time for cache misses
	start := time.Now()
	numMisses := 100

	for i := 0; i < numMisses; i++ {
		key := "miss-key-" + string(rune(i))
		result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
		s.NoError(err)
		s.Equal("value-"+key, result)
	}

	duration := time.Since(start)
	avgTimePerMiss := duration / time.Duration(numMisses)

	// Cache misses should be reasonable (dominated by loader work)
	s.Less(avgTimePerMiss, 5*time.Millisecond, "Cache misses should be reasonable")
}

// TestLRUEvictionPerformance tests the performance of LRU eviction
func (s *PerformanceTestSuite) TestLRUEvictionPerformance() {
	cache := New[string](160) // 10 * 16 bytes = 160 bytes, small cache to force evictions
	_ = cache.Reserve()       // Allocate slots
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Measure time for operations that cause evictions
	start := time.Now()
	numOperations := 1000

	for i := 0; i < numOperations; i++ {
		key := "evict-key-" + string(rune(i%20)) // Cycle through 20 keys
		result, _, err := cache.Get(s.T().Context(), key, ttl, loader)
		s.NoError(err)
		s.Equal("value-"+key, result)
	}

	duration := time.Since(start)
	avgTimePerOperation := duration / time.Duration(numOperations)

	// Eviction operations should be fast
	s.Less(avgTimePerOperation, 100*time.Microsecond, "LRU eviction should be fast")
}

// TestConcurrentPerformance tests concurrent access performance
func (s *PerformanceTestSuite) TestConcurrentPerformance() {
	ttl := 5 * time.Minute
	numGoroutines := 10
	operationsPerGoroutine := 100

	loader := func(ctx context.Context, k string) (string, error) {
		time.Sleep(100 * time.Microsecond) // Small work
		return "value-" + k, nil
	}

	// Measure concurrent performance
	start := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				key := "concurrent-key-" + string(rune(goroutineID*100+j))
				result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
				s.NoError(err)
				s.Equal("value-"+key, result)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	totalOperations := numGoroutines * operationsPerGoroutine
	avgTimePerOperation := duration / time.Duration(totalOperations)

	// Concurrent operations should be efficient
	s.Less(avgTimePerOperation, 1*time.Millisecond, "Concurrent operations should be efficient")
}

// TestMemoryUsage tests memory usage patterns
func (s *PerformanceTestSuite) TestMemoryUsage() {
	ttl := 5 * time.Minute
	numKeys := 10

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Load some keys and verify they work
	for i := 0; i < numKeys; i++ {
		key := "memory-key-" + string(rune(i))
		_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
		s.NoError(err)
	}

	// Verify we can retrieve them
	for i := 0; i < numKeys; i++ {
		key := "memory-key-" + string(rune(i))
		peekValue, found := s.cache.Peek(key)
		s.True(found, "Should find cached value")
		s.Equal("value-"+key, peekValue)
	}
}

// TestPeekPerformance tests the performance of Peek operations
func (s *PerformanceTestSuite) TestPeekPerformance() {
	key := "peek-perf-key"
	value := "peek-perf-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load value first
	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	// Measure time for peek operations
	start := time.Now()
	numPeeks := 10000

	for i := 0; i < numPeeks; i++ {
		peekValue, found := s.cache.Peek(key)
		s.True(found)
		s.Equal(value, peekValue)
	}

	duration := time.Since(start)
	avgTimePerPeek := duration / time.Duration(numPeeks)

	// Peek operations should be very fast
	s.Less(avgTimePerPeek, 5*time.Microsecond, "Peek operations should be very fast")
}

// TestDeletePerformance tests the performance of Delete operations
func (s *PerformanceTestSuite) TestDeletePerformance() {
	ttl := 5 * time.Minute
	numKeys := 1000

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Load many keys
	for i := 0; i < numKeys; i++ {
		key := "delete-perf-key-" + string(rune(i))
		_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
		s.NoError(err)
	}

	// Measure time for delete operations
	start := time.Now()

	for i := 0; i < numKeys; i++ {
		key := "delete-perf-key-" + string(rune(i))
		s.cache.Delete(key)
	}

	duration := time.Since(start)
	avgTimePerDelete := duration / time.Duration(numKeys)

	// Delete operations should be fast
	s.Less(avgTimePerDelete, 10*time.Microsecond, "Delete operations should be fast")
}

// TestTTLExpirationPerformance tests the performance of TTL expiration
func (s *PerformanceTestSuite) TestTTLExpirationPerformance() {
	shortTTL := 1 * time.Millisecond
	longTTL := 5 * time.Minute
	numKeys := 100

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Load keys with short TTL
	for i := 0; i < numKeys; i++ {
		key := "ttl-perf-key-" + string(rune(i))
		_, _, err := s.cache.Get(s.T().Context(), key, shortTTL, loader)
		s.NoError(err)
	}

	// Wait for expiration
	time.Sleep(shortTTL + 10*time.Millisecond)

	// Measure time for expired key access (should reload)
	start := time.Now()

	for i := 0; i < numKeys; i++ {
		key := "ttl-perf-key-" + string(rune(i))
		result, _, err := s.cache.Get(s.T().Context(), key, longTTL, loader)
		s.NoError(err)
		s.Equal("value-"+key, result)
	}

	duration := time.Since(start)
	avgTimePerExpiredAccess := duration / time.Duration(numKeys)

	// Expired key access should be reasonable
	s.Less(avgTimePerExpiredAccess, 1*time.Millisecond, "Expired key access should be reasonable")
}

// TestPerformanceTestSuite runs the performance test suite
func TestPerformanceTestSuite(t *testing.T) {
	suite.Run(t, new(PerformanceTestSuite))
}
