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

// LRUTestSuite provides specialized tests for LRU eviction behavior
type LRUTestSuite struct {
	suite.Suite
	ctx context.Context
}

// SetupSuite initializes the test suite
func (s *LRUTestSuite) SetupSuite() {
	s.ctx = context.Background()
	slog.Info("LRUTestSuite SetupSuite start")
}

// BeforeTest runs before each test
func (s *LRUTestSuite) BeforeTest(suiteName, testName string) {
	slog.Info("BeforeTest start", "TestSuite", "LRUTestSuite", "TestName", testName)
}

// AfterTest runs after each test
func (s *LRUTestSuite) AfterTest(suiteName, testName string) {
	slog.Info("AfterTest start", "TestSuite", "LRUTestSuite", "TestName", testName)
}

// TearDownSuite cleans up after all tests
func (s *LRUTestSuite) TearDownSuite() {
	slog.Info("TearDownSuite")
	slog.Info("TearDownSuite end")
}

// TestBasicLRUEviction tests basic LRU eviction
func (s *LRUTestSuite) TestBasicLRUEviction() {
	cache := New[string](48) // 3 * 16 bytes = 48 bytes
	_ = cache.Reserve()      // Allocate slots
	ttl := 5 * time.Minute

	// Create loaders for different keys
	loaders := map[string]func(context.Context, string) (string, error){
		"key1": func(ctx context.Context, k string) (string, error) { return "value1", nil },
		"key2": func(ctx context.Context, k string) (string, error) { return "value2", nil },
		"key3": func(ctx context.Context, k string) (string, error) { return "value3", nil },
		"key4": func(ctx context.Context, k string) (string, error) { return "value4", nil },
	}

	// Load 3 items (capacity)
	_, err := cache.Get(s.ctx, "key1", ttl, loaders["key1"])
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key2", ttl, loaders["key2"])
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key3", ttl, loaders["key3"])
	s.NoError(err)

	// All should be present
	_, found := cache.Peek("key1")
	s.True(found)
	_, found = cache.Peek("key2")
	s.True(found)
	_, found = cache.Peek("key3")
	s.True(found)

	// Add 4th item, should evict key1 (LRU)
	_, err = cache.Get(s.ctx, "key4", ttl, loaders["key4"])
	s.NoError(err)

	// key1 should be evicted
	_, found = cache.Peek("key1")
	s.False(found, "key1 should be evicted")

	// Others should still be present
	_, found = cache.Peek("key2")
	s.True(found, "key2 should still be present")
	_, found = cache.Peek("key3")
	s.True(found, "key3 should still be present")
	_, found = cache.Peek("key4")
	s.True(found, "key4 should be present")
}

// TestLRUAccessOrder tests that access order affects eviction
func (s *LRUTestSuite) TestLRUAccessOrder() {
	cache := New[string](48) // 3 * 16 bytes = 48 bytes
	_ = cache.Reserve()      // Allocate slots
	ttl := 5 * time.Minute

	loaders := map[string]func(context.Context, string) (string, error){
		"key1": func(ctx context.Context, k string) (string, error) { return "value1", nil },
		"key2": func(ctx context.Context, k string) (string, error) { return "value2", nil },
		"key3": func(ctx context.Context, k string) (string, error) { return "value3", nil },
		"key4": func(ctx context.Context, k string) (string, error) { return "value4", nil },
	}

	// Load 3 items
	_, err := cache.Get(s.ctx, "key1", ttl, loaders["key1"])
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key2", ttl, loaders["key2"])
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key3", ttl, loaders["key3"])
	s.NoError(err)

	// Access key1 to move it to front (MRU)
	_, err = cache.Get(s.ctx, "key1", ttl, loaders["key1"])
	s.NoError(err)

	// Add key4, should evict key2 (now LRU)
	_, err = cache.Get(s.ctx, "key4", ttl, loaders["key4"])
	s.NoError(err)

	// key1 should still be present (moved to front)
	_, found := cache.Peek("key1")
	s.True(found, "key1 should still be present after access")

	// key2 should be evicted
	_, found = cache.Peek("key2")
	s.False(found, "key2 should be evicted")

	// key3 and key4 should be present
	_, found = cache.Peek("key3")
	s.True(found, "key3 should be present")
	_, found = cache.Peek("key4")
	s.True(found, "key4 should be present")
}

// TestLRUWithExpiration tests LRU behavior with expiration
func (s *LRUTestSuite) TestLRUWithExpiration() {
	cache := New[string](48) // 3 * 16 bytes = 48 bytes
	_ = cache.Reserve()      // Allocate slots
	shortTTL := 20 * time.Millisecond
	longTTL := 5 * time.Minute

	// Load items with different TTLs
	_, err := cache.Get(s.ctx, "short-key", shortTTL, func(ctx context.Context, k string) (string, error) {
		return "short-value", nil
	})
	s.NoError(err)

	_, err = cache.Get(s.ctx, "long-key1", longTTL, func(ctx context.Context, k string) (string, error) {
		return "long-value1", nil
	})
	s.NoError(err)

	_, err = cache.Get(s.ctx, "long-key2", longTTL, func(ctx context.Context, k string) (string, error) {
		return "long-value2", nil
	})
	s.NoError(err)

	// Wait for short TTL to expire
	time.Sleep(shortTTL + 10*time.Millisecond)

	// Add new item, should evict expired short-key first
	_, err = cache.Get(s.ctx, "new-key", longTTL, func(ctx context.Context, k string) (string, error) {
		return "new-value", nil
	})
	s.NoError(err)

	// short-key should be gone (expired)
	_, found := cache.Peek("short-key")
	s.False(found, "short-key should be gone due to expiration")

	// long-key1 and long-key2 should still be present
	_, found = cache.Peek("long-key1")
	s.True(found, "long-key1 should be present")
	_, found = cache.Peek("long-key2")
	s.True(found, "long-key2 should be present")

	// new-key should be present
	_, found = cache.Peek("new-key")
	s.True(found, "new-key should be present")
}

// TestLRUDelete tests LRU behavior with deletion
func (s *LRUTestSuite) TestLRUDelete() {
	cache := New[string](48) // 3 * 16 bytes = 48 bytes
	_ = cache.Reserve()      // Allocate slots
	ttl := 5 * time.Minute

	loaders := map[string]func(context.Context, string) (string, error){
		"key1": func(ctx context.Context, k string) (string, error) { return "value1", nil },
		"key2": func(ctx context.Context, k string) (string, error) { return "value2", nil },
		"key3": func(ctx context.Context, k string) (string, error) { return "value3", nil },
		"key4": func(ctx context.Context, k string) (string, error) { return "value4", nil },
	}

	// Load 3 items
	_, err := cache.Get(s.ctx, "key1", ttl, loaders["key1"])
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key2", ttl, loaders["key2"])
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key3", ttl, loaders["key3"])
	s.NoError(err)

	// Delete key2
	cache.Delete("key2")

	// Add key4, should use the slot freed by key2 deletion
	_, err = cache.Get(s.ctx, "key4", ttl, loaders["key4"])
	s.NoError(err)

	// key1 and key3 should still be present
	_, found := cache.Peek("key1")
	s.True(found, "key1 should be present")
	_, found = cache.Peek("key3")
	s.True(found, "key3 should be present")

	// key2 should be gone (deleted)
	_, found = cache.Peek("key2")
	s.False(found, "key2 should be gone due to deletion")

	// key4 should be present
	_, found = cache.Peek("key4")
	s.True(found, "key4 should be present")
}

// TestLRUConcurrentAccess tests concurrent access affecting LRU order
func (s *LRUTestSuite) TestLRUConcurrentAccess() {
	cache := New[string](48) // 3 * 16 bytes = 48 bytes
	_ = cache.Reserve()      // Allocate slots
	ttl := 5 * time.Minute

	// Load initial items
	_, err := cache.Get(s.ctx, "key1", ttl, func(ctx context.Context, k string) (string, error) {
		return "value1", nil
	})
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key2", ttl, func(ctx context.Context, k string) (string, error) {
		return "value2", nil
	})
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key3", ttl, func(ctx context.Context, k string) (string, error) {
		return "value3", nil
	})
	s.NoError(err)

	// Concurrently access different keys
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		_, err := cache.Get(s.ctx, "key1", ttl, func(ctx context.Context, k string) (string, error) {
			return "value1", nil
		})
		s.NoError(err)
	}()

	go func() {
		defer wg.Done()
		_, err := cache.Get(s.ctx, "key2", ttl, func(ctx context.Context, k string) (string, error) {
			return "value2", nil
		})
		s.NoError(err)
	}()

	go func() {
		defer wg.Done()
		_, err := cache.Get(s.ctx, "key3", ttl, func(ctx context.Context, k string) (string, error) {
			return "value3", nil
		})
		s.NoError(err)
	}()

	wg.Wait()

	// All keys should still be present
	_, found := cache.Peek("key1")
	s.True(found, "key1 should be present")
	_, found = cache.Peek("key2")
	s.True(found, "key2 should be present")
	_, found = cache.Peek("key3")
	s.True(found, "key3 should be present")
}

// TestLRUEdgeCases tests edge cases for LRU
func (s *LRUTestSuite) TestLRUEdgeCases() {
	// Test with capacity 1
	cache1 := New[string](16) // 1 * 16 bytes = 16 bytes
	_ = cache1.Reserve()      // Allocate slots
	ttl := 5 * time.Minute

	_, err := cache1.Get(s.ctx, "key1", ttl, func(ctx context.Context, k string) (string, error) {
		return "value1", nil
	})
	s.NoError(err)

	_, err = cache1.Get(s.ctx, "key2", ttl, func(ctx context.Context, k string) (string, error) {
		return "value2", nil
	})
	s.NoError(err)

	// key1 should be evicted
	_, found := cache1.Peek("key1")
	s.False(found, "key1 should be evicted with capacity 1")

	// key2 should be present
	_, found = cache1.Peek("key2")
	s.True(found, "key2 should be present")

	// Test with capacity 2
	cache2 := New[string](32) // 2 * 16 bytes = 32 bytes
	_ = cache2.Reserve()      // Allocate slots

	// Load same key multiple times
	for i := 0; i < 5; i++ {
		_, err := cache2.Get(s.ctx, "same-key", ttl, func(ctx context.Context, k string) (string, error) {
			return "same-value", nil
		})
		s.NoError(err)
	}

	// Should still be present (no eviction of same key)
	_, found = cache2.Peek("same-key")
	s.True(found, "same-key should still be present")
}

// TestLRUPeekBehavior tests that Peek doesn't affect LRU order
func (s *LRUTestSuite) TestLRUPeekBehavior() {
	cache := New[string](48) // 3 * 16 bytes = 48 bytes
	_ = cache.Reserve()      // Allocate slots
	ttl := 5 * time.Minute

	// Load 3 items
	_, err := cache.Get(s.ctx, "key1", ttl, func(ctx context.Context, k string) (string, error) {
		return "value1", nil
	})
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key2", ttl, func(ctx context.Context, k string) (string, error) {
		return "value2", nil
	})
	s.NoError(err)

	_, err = cache.Get(s.ctx, "key3", ttl, func(ctx context.Context, k string) (string, error) {
		return "value3", nil
	})
	s.NoError(err)

	// Peek at key1 (should not affect LRU order)
	_, found := cache.Peek("key1")
	s.True(found, "key1 should be found by peek")

	// Add key4, should evict key1 (still LRU despite peek)
	_, err = cache.Get(s.ctx, "key4", ttl, func(ctx context.Context, k string) (string, error) {
		return "value4", nil
	})
	s.NoError(err)

	// key1 should be evicted (peek doesn't affect LRU)
	_, found = cache.Peek("key1")
	s.False(found, "key1 should be evicted despite peek")
}

// TestLRUTestSuite runs the LRU test suite
func TestLRUTestSuite(t *testing.T) {
	suite.Run(t, new(LRUTestSuite))
}
