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
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// PerchTestSuite provides a test suite for the Perch cache
type PerchTestSuite struct {
	suite.Suite
	cache *Perch[string]
}

// SetupSuite initializes the test suite
func (s *PerchTestSuite) SetupSuite() {
	slog.Info("PerchTestSuite SetupSuite start")
}

// BeforeTest runs before each test
func (s *PerchTestSuite) BeforeTest(suiteName, testName string) {
	slog.Info("BeforeTest start", "TestSuite", "PerchTestSuite", "TestName", testName)
	// Create a fresh cache for each test - enough bytes for 10 string entries
	s.cache = New[string](160) // 10 * 16 bytes = 160 bytes
	s.cache.Reserve()          // Allocate slots
}

// AfterTest runs after each test
func (s *PerchTestSuite) AfterTest(suiteName, testName string) {
	slog.Info("AfterTest start", "TestSuite", "PerchTestSuite", "TestName", testName)
}

// TearDownSuite cleans up after all tests
func (s *PerchTestSuite) TearDownSuite() {
	slog.Info("TearDownSuite")
	slog.Info("TearDownSuite end")
}

// TestNew tests the New function
func (s *PerchTestSuite) TestNew() {
	// Test valid capacity - pass enough bytes for 5 string entries
	// Each string is ~16 bytes (pointer + length), so 5 * 16 = 80 bytes
	cache := New[string](80)
	s.NotNil(cache)
	s.Equal(5, cache.cap) // Should calculate to 5 slots
	s.NotNil(cache.mu)
	s.NotNil(cache.table)
	s.NotNil(cache.slots)
	s.Equal(0, len(cache.slots)) // starts empty before Reserve()

	// Reserve slots
	cache.Reserve()
	s.Equal(6, len(cache.slots)) // capacity + 1 for 1-based indexing, allocated after Reserve()

	// Test panic on invalid capacity
	s.Panics(func() {
		New[string](0)
	}, "Should panic on capacity <= 0")

	s.Panics(func() {
		New[string](-1)
	}, "Should panic on negative capacity")
}

// TestBasicGetAndSet tests basic Get functionality
func (s *PerchTestSuite) TestBasicGetAndSet() {
	key := "test-key"
	expectedValue := "test-value"
	ttl := 5 * time.Minute

	// Create a simple loader
	loader := func(ctx context.Context, k string) (string, error) {
		s.Equal(key, k)
		return expectedValue, nil
	}

	// First call should load the value
	value, hit, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")
	s.Equal(expectedValue, value)

	// Second call should hit cache
	value, hit, err = s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.True(hit, "Second call should be a cache hit")
	s.Equal(expectedValue, value)
}

// TestTTLExpiration tests TTL expiration behavior
func (s *PerchTestSuite) TestTTLExpiration() {
	key := "ttl-key"
	value := "ttl-value"
	shortTTL := 10 * time.Millisecond

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load value with short TTL
	result, hit, err := s.cache.Get(s.T().Context(), key, shortTTL, loader)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")
	s.Equal(value, result)

	// Wait for expiration
	time.Sleep(shortTTL + 5*time.Millisecond)

	// Should reload due to expiration
	result, hit, err = s.cache.Get(s.T().Context(), key, shortTTL, loader)
	s.NoError(err)
	s.False(hit, "After expiration should be a cache miss")
	s.Equal(value, result)
}

// TestZeroTTL tests behavior with zero TTL (no caching)
func (s *PerchTestSuite) TestZeroTTL() {
	key := "no-cache-key"
	value := "no-cache-value"

	callCount := 0
	loader := func(ctx context.Context, k string) (string, error) {
		callCount++
		return value, nil
	}

	// Multiple calls with TTL=0 (indefinite caching) should only call loader once
	for i := 0; i < 3; i++ {
		result, hit, err := s.cache.Get(s.T().Context(), key, 0, loader)
		s.NoError(err)
		if i == 0 {
			s.False(hit, "First call with TTL=0 should be a cache miss")
		} else {
			s.True(hit, "Subsequent calls with TTL=0 should be cache hits")
		}
		s.Equal(value, result)
	}

	s.Equal(1, callCount, "Loader should be called only once with TTL=0 (indefinite caching)")
}

// TestDelete tests the Delete functionality
func (s *PerchTestSuite) TestDelete() {
	key := "delete-key"
	value := "delete-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load value
	result, hit, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")
	s.Equal(value, result)

	// Delete the key
	s.cache.Delete(key)

	// Should reload after deletion
	callCount := 0
	loader2 := func(ctx context.Context, k string) (string, error) {
		callCount++
		return value, nil
	}

	result, hit, err = s.cache.Get(s.T().Context(), key, ttl, loader2)
	s.NoError(err)
	s.False(hit, "After deletion should be a cache miss")
	s.Equal(value, result)
	s.Equal(1, callCount, "Should reload after deletion")
}

// TestPeek tests the Peek functionality
func (s *PerchTestSuite) TestPeek() {
	key := "peek-key"
	value := "peek-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Peek before loading should return false
	peekValue, found := s.cache.Peek(key)
	s.False(found)
	s.Equal("", peekValue)

	// Load value
	result, hit, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")
	s.Equal(value, result)

	// Peek after loading should return true
	peekValue, found = s.cache.Peek(key)
	s.True(found)
	s.Equal(value, peekValue)
}

// TestLRUEviction tests LRU eviction when capacity is exceeded
func (s *PerchTestSuite) TestLRUEviction() {
	// Create cache with small capacity - 2 string entries
	cache := New[string](32) // 2 * 16 bytes = 32 bytes
	cache.Reserve()          // Allocate slots
	ttl := 5 * time.Minute

	// Load 3 items (exceeds capacity)
	loader1 := func(ctx context.Context, k string) (string, error) {
		return "value1", nil
	}
	loader2 := func(ctx context.Context, k string) (string, error) {
		return "value2", nil
	}
	loader3 := func(ctx context.Context, k string) (string, error) {
		return "value3", nil
	}

	// Load items in order
	_, hit, err := cache.Get(s.T().Context(), "key1", ttl, loader1)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")

	_, hit, err = cache.Get(s.T().Context(), "key2", ttl, loader2)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")

	_, hit, err = cache.Get(s.T().Context(), "key3", ttl, loader3)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")

	var peekValue string

	// key1 should be evicted (LRU)
	_, found := cache.Peek("key1")
	s.False(found, "key1 should be evicted")

	// key2 and key3 should still be present
	peekValue, found = cache.Peek("key2")
	s.True(found, "key2 should still be present")
	s.Equal("value2", peekValue)

	peekValue, found = cache.Peek("key3")
	s.True(found, "key3 should still be present")
	s.Equal("value3", peekValue)
}

// TestConcurrency tests concurrent access to the cache
func (s *PerchTestSuite) TestConcurrency() {
	key := "concurrent-key"
	value := "concurrent-value"
	ttl := 5 * time.Minute

	callCount := 0
	var mu sync.Mutex
	loader := func(ctx context.Context, k string) (string, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond) // Simulate work
		return value, nil
	}

	// Launch multiple goroutines
	var wg sync.WaitGroup
	numGoroutines := 10
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
			s.NoError(err)
			s.Equal(value, result)
		}()
	}

	wg.Wait()

	// Should only call loader once due to singleflight
	mu.Lock()
	actualCallCount := callCount
	mu.Unlock()
	s.Equal(1, actualCallCount, "Loader should only be called once due to singleflight")
}

// TestErrorHandling tests error handling in loaders
func (s *PerchTestSuite) TestErrorHandling() {
	key := "error-key"
	expectedError := errors.New("loader error")

	loader := func(ctx context.Context, k string) (string, error) {
		return "", expectedError
	}

	// Should return error
	result, _, err := s.cache.Get(s.T().Context(), key, 5*time.Minute, loader)
	s.Error(err)
	s.Equal(expectedError, err)
	s.Equal("", result)

	// Should not cache errors
	callCount := 0
	loader2 := func(ctx context.Context, k string) (string, error) {
		callCount++
		return "", expectedError
	}

	// Multiple calls should all call the loader
	for i := 0; i < 3; i++ {
		_, _, err := s.cache.Get(s.T().Context(), key, 5*time.Minute, loader2)
		s.Error(err)
	}

	s.Equal(3, callCount, "Should call loader for each error")
}

// TestPanicRecovery tests panic recovery in loaders
func (s *PerchTestSuite) TestPanicRecovery() {
	key := "panic-key"

	loader := func(ctx context.Context, k string) (string, error) {
		panic("test panic")
	}

	// Should recover from panic and return error
	result, _, err := s.cache.Get(s.T().Context(), key, 5*time.Minute, loader)
	s.Error(err)
	s.Contains(err.Error(), "loader panicked")
	s.Equal("", result)
}

// TestContextCancellation tests context cancellation
func (s *PerchTestSuite) TestContextCancellation() {
	key := "cancel-key"
	value := "cancel-value"

	// Create a context that will be cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	loader := func(ctx context.Context, k string) (string, error) {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			return value, nil
		}
	}

	// Should handle cancelled context
	result, _, err := s.cache.Get(ctx, key, 5*time.Minute, loader)
	s.Error(err)
	s.Equal(context.Canceled, err)
	s.Equal("", result)
}

// TestEdgeCases tests various edge cases
func (s *PerchTestSuite) TestEdgeCases() {
	// Test empty key
	loader := func(ctx context.Context, k string) (string, error) {
		return "empty-key-value", nil
	}

	result, _, err := s.cache.Get(s.T().Context(), "", 5*time.Minute, loader)
	s.NoError(err)
	s.Equal("empty-key-value", result)

	// Test very long key
	longKey := string(make([]byte, 1000))
	for i := range longKey {
		longKey = longKey[:i] + "a" + longKey[i+1:]
	}

	result, _, err = s.cache.Get(s.T().Context(), longKey, 5*time.Minute, loader)
	s.NoError(err)
	s.Equal("empty-key-value", result)

	// Test zero value
	zeroLoader := func(ctx context.Context, k string) (string, error) {
		return "", nil
	}

	result, _, err = s.cache.Get(s.T().Context(), "zero-key", 5*time.Minute, zeroLoader)
	s.NoError(err)
	s.Equal("", result)
}

// TestMoveToFront tests LRU move-to-front behavior
func (s *PerchTestSuite) TestMoveToFront() {
	// Create cache with capacity 2 - 2 string entries
	cache := New[string](32) // 2 * 16 bytes = 32 bytes
	cache.Reserve()          // Allocate slots
	ttl := 5 * time.Minute

	// Load two items
	loader1 := func(ctx context.Context, k string) (string, error) {
		return "value1", nil
	}
	loader2 := func(ctx context.Context, k string) (string, error) {
		return "value2", nil
	}
	loader3 := func(ctx context.Context, k string) (string, error) {
		return "value3", nil
	}

	_, hit, err := cache.Get(s.T().Context(), "key1", ttl, loader1)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")

	_, hit, err = cache.Get(s.T().Context(), "key2", ttl, loader2)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")

	// Access key1 to move it to front
	_, hit, err = cache.Get(s.T().Context(), "key1", ttl, loader1)
	s.NoError(err)
	s.True(hit, "Second call to key1 should be a cache hit")

	// Add key3, should evict key2 (now LRU)
	_, hit, err = cache.Get(s.T().Context(), "key3", ttl, loader3)
	s.NoError(err)
	s.False(hit, "First call to key3 should be a cache miss")

	// key1 should still be present (moved to front)
	peekValue, found := cache.Peek("key1")
	s.True(found, "key1 should still be present after move-to-front")
	s.Equal("value1", peekValue)

	// key2 should be evicted
	_, found = cache.Peek("key2")
	s.False(found, "key2 should be evicted")

	// key3 should be present
	peekValue, found = cache.Peek("key3")
	s.True(found, "key3 should be present")
	s.Equal("value3", peekValue)
}

// TestPerchTestSuite runs the test suite
func TestPerchTestSuite(t *testing.T) {
	suite.Run(t, new(PerchTestSuite))
}
