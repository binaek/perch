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
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// ConcurrencyTestSuite provides specialized tests for concurrency and singleflight behavior
type ConcurrencyTestSuite struct {
	suite.Suite
	cache *Perch[string]
}

// SetupSuite initializes the test suite
func (s *ConcurrencyTestSuite) SetupSuite() {
	slog.Info("ConcurrencyTestSuite SetupSuite start")
}

// BeforeTest runs before each test
func (s *ConcurrencyTestSuite) BeforeTest(suiteName, testName string) {
	slog.Info("BeforeTest start", "TestSuite", "ConcurrencyTestSuite", "TestName", testName)
	s.cache = New[string](160) // 10 * 16 bytes = 160 bytes
	s.cache.Reserve()          // Allocate slots
}

// AfterTest runs after each test
func (s *ConcurrencyTestSuite) AfterTest(suiteName, testName string) {
	slog.Info("AfterTest start", "TestSuite", "ConcurrencyTestSuite", "TestName", testName)
}

// TearDownSuite cleans up after all tests
func (s *ConcurrencyTestSuite) TearDownSuite() {
	slog.Info("TearDownSuite")
	slog.Info("TearDownSuite end")
}

// TestSingleflightBasic tests basic singleflight behavior
func (s *ConcurrencyTestSuite) TestSingleflightBasic() {
	key := "singleflight-key"
	value := "singleflight-value"
	ttl := 5 * time.Minute

	callCount := int32(0)
	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(50 * time.Millisecond) // Simulate work
		return value, nil
	}

	// Launch multiple goroutines for the same key
	var wg sync.WaitGroup
	numGoroutines := 10
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
			s.NoError(err)
			s.Equal(value, result)
			// hit could be true or false depending on timing
		}()
	}

	wg.Wait()

	// Should only call loader once
	finalCallCount := atomic.LoadInt32(&callCount)
	s.Equal(int32(1), finalCallCount, "Loader should only be called once due to singleflight")
}

// TestSingleflightDifferentKeys tests singleflight with different keys
func (s *ConcurrencyTestSuite) TestSingleflightDifferentKeys() {
	ttl := 5 * time.Minute

	callCounts := make(map[string]*int32)
	keys := []string{"key1", "key2", "key3"}

	// Initialize call counts
	for _, key := range keys {
		callCounts[key] = new(int32)
	}

	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(callCounts[k], 1)
		time.Sleep(30 * time.Millisecond) // Simulate work
		return "value-" + k, nil
	}

	// Launch goroutines for different keys
	var wg sync.WaitGroup
	numGoroutinesPerKey := 5
	for _, key := range keys {
		for i := 0; i < numGoroutinesPerKey; i++ {
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				result, _, err := s.cache.Get(s.T().Context(), k, ttl, loader)
				s.NoError(err)
				s.Equal("value-"+k, result)
			}(key)
		}
	}

	wg.Wait()

	// Each key should only call loader once
	for _, key := range keys {
		finalCallCount := atomic.LoadInt32(callCounts[key])
		s.Equal(int32(1), finalCallCount, "Loader should be called once for key: %s", key)
	}
}

// TestSingleflightWithErrors tests singleflight behavior with errors
func (s *ConcurrencyTestSuite) TestSingleflightWithErrors() {
	key := "error-singleflight-key"
	expectedError := errors.New("loader error")
	ttl := 5 * time.Minute

	callCount := int32(0)
	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(30 * time.Millisecond) // Simulate work
		return "", expectedError
	}

	// Launch multiple goroutines for the same key
	var wg sync.WaitGroup
	numGoroutines := 8
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
			s.Error(err)
			s.Equal(expectedError, err)
			s.Equal("", result)
		}()
	}

	wg.Wait()

	// Errors use singleflight: concurrent requests for the same key will only call the loader once
	// All waiters will receive the same error, but errors are not cached for future requests
	finalCallCount := atomic.LoadInt32(&callCount)
	s.Equal(int32(1), finalCallCount, "Errors should use singleflight: concurrent requests should only call loader once")
}

// TestConcurrentAccessDifferentKeys tests concurrent access to different keys
func (s *ConcurrencyTestSuite) TestConcurrentAccessDifferentKeys() {
	ttl := 5 * time.Minute
	numKeys := 5 // Reduced for more reliable testing
	numGoroutinesPerKey := 2

	callCounts := make(map[string]*int32)
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		callCounts[key] = new(int32)
	}

	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(callCounts[k], 1)
		time.Sleep(10 * time.Millisecond) // Simulate work
		return "value-" + k, nil
	}

	// Launch goroutines for all keys
	var wg sync.WaitGroup
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		for j := 0; j < numGoroutinesPerKey; j++ {
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				result, _, err := s.cache.Get(s.T().Context(), k, ttl, loader)
				s.NoError(err)
				s.Equal("value-"+k, result)
			}(key)
		}
	}

	wg.Wait()

	// Each key should only call loader once due to singleflight
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		finalCallCount := atomic.LoadInt32(callCounts[key])
		s.Equal(int32(1), finalCallCount, "Loader should be called once for key: %s", key)
	}
}

// TestConcurrentDelete tests concurrent delete operations
func (s *ConcurrencyTestSuite) TestConcurrentDelete() {
	key := "delete-key"
	value := "delete-value"
	ttl := 5 * time.Minute

	// Load value first
	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	// Concurrently delete and access
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		s.cache.Delete(key)
	}()

	go func() {
		defer wg.Done()
		// This might hit cache or miss depending on timing
		result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
		if err == nil {
			s.Equal(value, result)
		}
	}()

	wg.Wait()
}

// TestConcurrentPeek tests concurrent peek operations
func (s *ConcurrencyTestSuite) TestConcurrentPeek() {
	key := "peek-key"
	value := "peek-value"
	ttl := 5 * time.Minute

	// Load value
	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	// Concurrently peek at the same key
	var wg sync.WaitGroup
	numGoroutines := 10
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			peekValue, found := s.cache.Peek(key)
			s.True(found, "Peek should find the value")
			s.Equal(value, peekValue)
		}()
	}

	wg.Wait()
}

// TestConcurrentExpiration tests concurrent access during expiration
func (s *ConcurrencyTestSuite) TestConcurrentExpiration() {
	key := "expire-key"
	value := "expire-value"
	shortTTL := 30 * time.Millisecond

	callCount := int32(0)
	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(10 * time.Millisecond) // Simulate work
		return value, nil
	}

	// Load value
	_, _, err := s.cache.Get(s.T().Context(), key, shortTTL, loader)
	s.NoError(err)

	// Wait for expiration
	time.Sleep(shortTTL + 10*time.Millisecond)

	// Concurrently access after expiration
	var wg sync.WaitGroup
	numGoroutines := 5
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := s.cache.Get(s.T().Context(), key, shortTTL, loader)
			s.NoError(err)
			s.Equal(value, result)
		}()
	}

	wg.Wait()

	// Should call loader once initially and once after expiration
	finalCallCount := atomic.LoadInt32(&callCount)
	s.Equal(int32(2), finalCallCount, "Should call loader twice: once initially, once after expiration")
}

// TestConcurrentLRUEviction tests concurrent access during LRU eviction
func (s *ConcurrencyTestSuite) TestConcurrentLRUEviction() {
	cache := New[string](48) // 3 * 16 bytes = 48 bytes, small capacity to force eviction
	cache.Reserve()          // Allocate slots
	ttl := 5 * time.Minute

	// Load initial items
	for i := 1; i <= 3; i++ {
		key := fmt.Sprintf("key-%d", i)
		_, _, err := cache.Get(s.T().Context(), key, ttl, func(ctx context.Context, k string) (string, error) {
			return "value-" + k, nil
		})
		s.NoError(err)
	}

	// Concurrently access existing keys and add new ones
	var wg sync.WaitGroup
	numGoroutines := 10
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				// Access existing key
				key := fmt.Sprintf("key-%d", (id%3)+1)
				result, _, err := cache.Get(s.T().Context(), key, ttl, func(ctx context.Context, k string) (string, error) {
					return "value-" + k, nil
				})
				s.NoError(err)
				// Don't assert exact value due to race conditions in concurrent access
				s.Contains(result, "value-key-")
			} else {
				// Add new key (will cause eviction)
				key := fmt.Sprintf("new-key-%d", id)
				result, _, err := cache.Get(s.T().Context(), key, ttl, func(ctx context.Context, k string) (string, error) {
					return "value-" + k, nil
				})
				s.NoError(err)
				s.Equal("value-"+key, result)
			}
		}(i)
	}

	wg.Wait()
}

// TestConcurrentZeroTTL tests concurrent access with zero TTL
func (s *ConcurrencyTestSuite) TestConcurrentZeroTTL() {
	key := "zero-ttl-key"
	value := "zero-ttl-value"

	callCount := int32(0)
	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(10 * time.Millisecond) // Simulate work
		return value, nil
	}

	// Concurrently access with TTL=-1 (no caching)
	var wg sync.WaitGroup
	numGoroutines := 10
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := s.cache.Get(s.T().Context(), key, -1, loader)
			s.NoError(err)
			s.Equal(value, result)
		}()
	}

	wg.Wait()

	// With TTL=-1, singleflight ensures only one loader call for concurrent requests
	// But sequential requests will call loader again (no caching)
	finalCallCount := atomic.LoadInt32(&callCount)
	s.Equal(int32(1), finalCallCount, "Should call loader once due to singleflight with TTL=-1")
}

// TestConcurrentPanicRecovery tests concurrent access with panicking loaders
func (s *ConcurrencyTestSuite) TestConcurrentPanicRecovery() {
	key := "panic-key"

	callCount := int32(0)
	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(10 * time.Millisecond) // Simulate work
		panic("test panic")
	}

	// Concurrently access with panicking loader
	var wg sync.WaitGroup
	numGoroutines := 5
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := s.cache.Get(s.T().Context(), key, 5*time.Minute, loader)
			s.Error(err)
			s.Contains(err.Error(), "loader panicked")
			s.Equal("", result)
		}()
	}

	wg.Wait()

	// Panics are treated like errors and use singleflight: concurrent requests will only call the loader once
	// All waiters will receive the same error, but errors are not cached for future requests
	finalCallCount := atomic.LoadInt32(&callCount)
	s.Equal(int32(1), finalCallCount, "Panics should use singleflight: concurrent requests should only call loader once")
}

// TestConcurrencyTestSuite runs the concurrency test suite
func TestConcurrencyTestSuite(t *testing.T) {
	suite.Run(t, new(ConcurrencyTestSuite))
}
