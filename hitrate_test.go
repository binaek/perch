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
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// HitRateTestSuite provides tests for hit rate functionality
type HitRateTestSuite struct {
	suite.Suite
	cache *Perch[string]
}

// SetupSuite initializes the test suite
func (s *HitRateTestSuite) SetupSuite() {
	slog.Info("HitRateTestSuite SetupSuite start")
}

// BeforeTest runs before each test
func (s *HitRateTestSuite) BeforeTest(suiteName, testName string) {
	slog.Info("BeforeTest start", "TestSuite", "HitRateTestSuite", "TestName", testName)
	s.cache = New[string](160) // 10 * 16 bytes = 160 bytes
	_ = s.cache.Reserve()      // Allocate slots
}

// AfterTest runs after each test
func (s *HitRateTestSuite) AfterTest(suiteName, testName string) {
	slog.Info("AfterTest start", "TestSuite", "HitRateTestSuite", "TestName", testName)
}

// TearDownSuite cleans up after all tests
func (s *HitRateTestSuite) TearDownSuite() {
	slog.Info("TearDownSuite")
	slog.Info("TearDownSuite end")
}

// TestHitRateBasic tests basic hit rate functionality
func (s *HitRateTestSuite) TestHitRateBasic() {
	key := "hitrate-key"
	value := "hitrate-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Initially no requests, hit rate should be 0
	hitRate := s.cache.HitRate()
	s.Equal(0.0, hitRate, "Initial hit rate should be 0")

	stats := s.cache.Stats()
	s.Equal(uint64(0), stats.Hits)
	s.Equal(uint64(0), stats.Misses)
	s.Equal(uint64(0), stats.Total)
	s.Equal(0.0, stats.HitRate)

	// First call - cache miss
	_, hit, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")

	// Check stats after miss
	hitRate = s.cache.HitRate()
	s.Equal(0.0, hitRate, "Hit rate after miss should be 0")

	stats = s.cache.Stats()
	s.Equal(uint64(0), stats.Hits)
	s.Equal(uint64(1), stats.Misses)
	s.Equal(uint64(1), stats.Total)
	s.Equal(0.0, stats.HitRate)

	// Second call - cache hit
	_, hit, err = s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)
	s.True(hit, "Second call should be a cache hit")

	// Check stats after hit
	hitRate = s.cache.HitRate()
	s.Equal(50.0, hitRate, "Hit rate after one hit and one miss should be 50%")

	stats = s.cache.Stats()
	s.Equal(uint64(1), stats.Hits)
	s.Equal(uint64(1), stats.Misses)
	s.Equal(uint64(2), stats.Total)
	s.Equal(50.0, stats.HitRate)
}

// TestHitRateMultipleKeys tests hit rate with multiple keys
func (s *HitRateTestSuite) TestHitRateMultipleKeys() {
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Load 3 different keys (all misses)
	_, _, err := s.cache.Get(s.T().Context(), "key1", ttl, loader)
	s.NoError(err)
	_, _, err = s.cache.Get(s.T().Context(), "key2", ttl, loader)
	s.NoError(err)
	_, _, err = s.cache.Get(s.T().Context(), "key3", ttl, loader)
	s.NoError(err)

	// All misses so far
	hitRate := s.cache.HitRate()
	s.Equal(0.0, hitRate, "Hit rate after 3 misses should be 0")

	// Access key1 again (hit)
	_, hit, err := s.cache.Get(s.T().Context(), "key1", ttl, loader)
	s.NoError(err)
	s.True(hit, "Second access to key1 should be a hit")

	// 1 hit, 3 misses = 25% hit rate
	hitRate = s.cache.HitRate()
	s.Equal(25.0, hitRate, "Hit rate should be 25%")

	// Access key2 and key3 again (2 more hits)
	_, hit, err = s.cache.Get(s.T().Context(), "key2", ttl, loader)
	s.NoError(err)
	s.True(hit, "Second access to key2 should be a hit")

	_, hit, err = s.cache.Get(s.T().Context(), "key3", ttl, loader)
	s.NoError(err)
	s.True(hit, "Second access to key3 should be a hit")

	// 3 hits, 3 misses = 50% hit rate
	hitRate = s.cache.HitRate()
	s.Equal(50.0, hitRate, "Hit rate should be 50%")
}

// TestHitRateWithExpiration tests hit rate with TTL expiration
func (s *HitRateTestSuite) TestHitRateWithExpiration() {
	key := "expire-key"
	value := "expire-value"
	shortTTL := 10 * time.Millisecond

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load value
	_, hit, err := s.cache.Get(s.T().Context(), key, shortTTL, loader)
	s.NoError(err)
	s.False(hit, "First call should be a cache miss")

	// Access before expiration (hit)
	_, hit, err = s.cache.Get(s.T().Context(), key, shortTTL, loader)
	s.NoError(err)
	s.True(hit, "Access before expiration should be a hit")

	// Wait for expiration
	time.Sleep(shortTTL + 5*time.Millisecond)

	// Access after expiration (miss)
	_, hit, err = s.cache.Get(s.T().Context(), key, shortTTL, loader)
	s.NoError(err)
	s.False(hit, "Access after expiration should be a miss")

	// 1 hit, 2 misses = 33.33% hit rate
	hitRate := s.cache.HitRate()
	s.InDelta(33.33, hitRate, 0.1, "Hit rate should be approximately 33.33%")
}

// TestHitRateWithZeroTTL tests hit rate with zero TTL (no caching)
func (s *HitRateTestSuite) TestHitRateWithZeroTTL() {
	key := "zero-ttl-key"
	value := "zero-ttl-value"

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Multiple calls with zero TTL should all be misses
	for i := 0; i < 5; i++ {
		_, hit, err := s.cache.Get(s.T().Context(), key, 0, loader)
		s.NoError(err)
		s.False(hit, "Zero TTL should always be a cache miss")
	}

	// All misses
	hitRate := s.cache.HitRate()
	s.Equal(0.0, hitRate, "Hit rate with zero TTL should be 0")

	stats := s.cache.Stats()
	s.Equal(uint64(0), stats.Hits)
	s.Equal(uint64(5), stats.Misses)
	s.Equal(uint64(5), stats.Total)
}

// TestResetStats tests the ResetStats functionality
func (s *HitRateTestSuite) TestResetStats() {
	key := "reset-key"
	value := "reset-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Generate some hits and misses
	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader) // miss
	s.NoError(err)
	_, _, err = s.cache.Get(s.T().Context(), key, ttl, loader) // hit
	s.NoError(err)

	// Verify we have stats
	stats := s.cache.Stats()
	s.Equal(uint64(1), stats.Hits)
	s.Equal(uint64(1), stats.Misses)

	// Reset stats
	s.cache.ResetStats()

	// Verify stats are reset
	stats = s.cache.Stats()
	s.Equal(uint64(0), stats.Hits)
	s.Equal(uint64(0), stats.Misses)
	s.Equal(uint64(0), stats.Total)
	s.Equal(0.0, stats.HitRate)

	hitRate := s.cache.HitRate()
	s.Equal(0.0, hitRate, "Hit rate after reset should be 0")
}

// TestStatsDetails tests the detailed stats functionality
func (s *HitRateTestSuite) TestStatsDetails() {
	key := "stats-key"
	value := "stats-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Load a value
	_, _, err := s.cache.Get(s.T().Context(), key, ttl, loader)
	s.NoError(err)

	stats := s.cache.Stats()
	s.Equal(uint64(0), stats.Hits)
	s.Equal(uint64(1), stats.Misses)
	s.Equal(uint64(1), stats.Total)
	s.Equal(0.0, stats.HitRate)
	s.Equal(10, stats.Capacity) // 160 bytes / 16 bytes per string = 10 slots
	s.Equal(1, stats.Size)      // 1 item in cache
}

// TestHitRateTestSuite runs the hit rate test suite
func TestHitRateTestSuite(t *testing.T) {
	suite.Run(t, new(HitRateTestSuite))
}
