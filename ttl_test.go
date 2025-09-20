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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// TTLTestSuite provides specialized tests for TTL and expiration behavior
type TTLTestSuite struct {
	suite.Suite
	cache *Perch[string]
}

// SetupSuite initializes the test suite
func (s *TTLTestSuite) SetupSuite() {
	slog.Info("TTLTestSuite SetupSuite start")
}

// BeforeTest runs before each test
func (s *TTLTestSuite) BeforeTest(suiteName, testName string) {
	slog.Info("BeforeTest start", "TestSuite", "TTLTestSuite", "TestName", testName)
	s.cache = New[string](160) // 10 * 16 bytes = 160 bytes
	_ = s.cache.Reserve()      // Allocate slots
}

// AfterTest runs after each test
func (s *TTLTestSuite) AfterTest(suiteName, testName string) {
	slog.Info("AfterTest start", "TestSuite", "TTLTestSuite", "TestName", testName)
}

// TearDownSuite cleans up after all tests
func (s *TTLTestSuite) TearDownSuite() {
	slog.Info("TearDownSuite")
	slog.Info("TearDownSuite end")
}

// TestTTLExpiration tests basic TTL expiration
func (s *TTLTestSuite) TestTTLExpiration() {
	key := "ttl-key"
	value := "ttl-value"
	ttl := 50 * time.Millisecond

	callCount := 0
	loader := func(ctx context.Context, k string) (string, error) {
		callCount++
		return value, nil
	}

	// Load value
	result, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.Equal(value, result)
	s.Equal(1, callCount, "Should call loader once")

	// Immediate access should hit cache
	result, _, err = s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.Equal(value, result)
	s.Equal(1, callCount, "Should not call loader again")

	// Wait for expiration
	time.Sleep(ttl + 10*time.Millisecond)

	// Access after expiration should reload
	result, _, err = s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.Equal(value, result)
	s.Equal(2, callCount, "Should call loader again after expiration")
}

// TestDifferentTTLs tests different TTL values
func (s *TTLTestSuite) TestDifferentTTLs() {
	key1 := "short-ttl-key"
	key2 := "long-ttl-key"
	value1 := "short-ttl-value"
	value2 := "long-ttl-value"
	shortTTL := 20 * time.Millisecond
	longTTL := 100 * time.Millisecond

	callCount1 := 0
	callCount2 := 0

	loader1 := func(ctx context.Context, k string) (string, error) {
		callCount1++
		return value1, nil
	}

	loader2 := func(ctx context.Context, k string) (string, error) {
		callCount2++
		return value2, nil
	}

	// Load both values
	_, _, err := s.cache.Get(s.T().Context(), key1, shortTTL, loader1)
	s.NoError(err)

	_, _, err = s.cache.Get(s.T().Context(), key2, longTTL, loader2)
	s.NoError(err)

	// Wait for short TTL to expire but not long TTL
	time.Sleep(shortTTL + 10*time.Millisecond)

	// Short TTL should reload
	_, _, err = s.cache.Get(s.T().Context(), key1, shortTTL, loader1)
	s.NoError(err)
	s.Equal(2, callCount1, "Short TTL should reload")

	// Long TTL should still hit cache
	_, _, err = s.cache.Get(s.T().Context(), key2, longTTL, loader2)
	s.NoError(err)
	s.Equal(1, callCount2, "Long TTL should not reload yet")
}

// TestZeroTTL tests zero TTL behavior
func (s *TTLTestSuite) TestZeroTTL() {
	key := "zero-ttl-key"
	value := "zero-ttl-value"

	callCount := 0
	loader := func(ctx context.Context, k string) (string, error) {
		callCount++
		return value, nil
	}

	// Multiple calls with zero TTL should all call loader
	for i := 0; i < 5; i++ {
		result, _, err := s.cache.Get(s.T().Context(), key, 0, loader)
		s.NoError(err)
		s.Equal(value, result)
	}

	s.Equal(5, callCount, "Should call loader for each zero TTL request")
}

// TestNegativeTTL tests negative TTL behavior
func (s *TTLTestSuite) TestNegativeTTL() {
	key := "negative-ttl-key"
	value := "negative-ttl-value"

	callCount := 0
	loader := func(ctx context.Context, k string) (string, error) {
		callCount++
		return value, nil
	}

	// Negative TTL should behave like zero TTL
	for i := 0; i < 3; i++ {
		result, _, err := s.cache.Get(s.T().Context(), key, -1*time.Second, loader)
		s.NoError(err)
		s.Equal(value, result)
	}

	s.Equal(3, callCount, "Should call loader for each negative TTL request")
}

// TestTTLUpdate tests TTL update behavior
func (s *TTLTestSuite) TestTTLUpdate() {
	key := "ttl-update-key"
	value := "ttl-update-value"
	shortTTL := 20 * time.Millisecond
	longTTL := 100 * time.Millisecond

	callCount := 0
	loader := func(ctx context.Context, k string) (string, error) {
		callCount++
		return value, nil
	}

	// Load with short TTL
	_, _, err := s.cache.Get(s.T().Context(), key, shortTTL, loader)
	s.NoError(err)
	s.Equal(1, callCount)

	// Wait for short TTL to expire
	time.Sleep(shortTTL + 10*time.Millisecond)

	// Reload with longer TTL
	_, _, err = s.cache.Get(s.T().Context(), key, longTTL, loader)
	s.NoError(err)
	s.Equal(2, callCount)

	// Wait for original short TTL but not new long TTL
	time.Sleep(shortTTL + 10*time.Millisecond)

	// Should still hit cache due to longer TTL
	_, _, err = s.cache.Get(s.T().Context(), key, longTTL, loader)
	s.NoError(err)
	s.Equal(2, callCount, "Should not reload due to longer TTL")
}

// TestPeekExpiration tests Peek behavior with expiration
func (s *TTLTestSuite) TestPeekExpiration() {
	key := "peek-expiration-key"
	value := "peek-expiration-value"
	ttl := 30 * time.Millisecond

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load value
	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	// Peek should return true before expiration
	peekValue, found := s.cache.Peek(key)
	s.True(found, "Should find value before expiration")
	s.Equal(value, peekValue)

	// Wait for expiration
	time.Sleep(ttl + 10*time.Millisecond)

	// Peek should return false after expiration
	peekValue, found = s.cache.Peek(key)
	s.False(found, "Should not find value after expiration")
	s.Equal("", peekValue)
}

// TestConcurrentTTL tests concurrent access with TTL
func (s *TTLTestSuite) TestConcurrentTTL() {
	key := "concurrent-ttl-key"
	value := "concurrent-ttl-value"
	ttl := 50 * time.Millisecond

	callCount := 0
	var mu sync.Mutex
	loader := func(ctx context.Context, k string) (string, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond) // Simulate work
		return value, nil
	}

	// Load value
	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	// Wait for expiration
	time.Sleep(ttl + 10*time.Millisecond)

	// Launch multiple goroutines after expiration
	var wg sync.WaitGroup
	numGoroutines := 5
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
	s.Equal(2, actualCallCount, "Should call loader once initially and once after expiration")
}

// TestTTLTestSuite runs the TTL test suite
func TestTTLTestSuite(t *testing.T) {
	suite.Run(t, new(TTLTestSuite))
}
