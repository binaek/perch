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
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkCacheHit benchmarks cache hit performance
func BenchmarkCacheHit(b *testing.B) {
	cache := New[string](1600) // 100 * 16 bytes = 1600 bytes
	cache.Reserve()

	key := "benchmark-hit-key"
	value := "benchmark-hit-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Pre-populate cache
	_, _, _ = cache.Get(b.Context(), key, ttl, loader)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _ = cache.Get(b.Context(), key, ttl, loader)
		}
	})
}

// BenchmarkCacheMiss benchmarks cache miss performance
func BenchmarkCacheMiss(b *testing.B) {
	cache := New[string](1600) // 100 * 16 bytes = 1600 bytes
	cache.Reserve()

	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		// Simulate some work
		time.Sleep(1 * time.Microsecond)
		return "value-" + k, nil
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("miss-key-%d", i)
			_, _, _ = cache.Get(b.Context(), key, ttl, loader)
			i++
		}
	})
}

// BenchmarkCacheHitWithAllocationTracking benchmarks cache hits with allocation tracking
func BenchmarkCacheHitWithAllocationTracking(b *testing.B) {
	cache := New[string](1600) // 100 * 16 bytes = 1600 bytes
	cache.Reserve()

	key := "allocation-hit-key"
	value := "allocation-hit-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Pre-populate cache
	_, _, _ = cache.Get(b.Context(), key, ttl, loader)

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	// Report allocations per operation
	b.ReportMetric(float64(m2.Mallocs-m1.Mallocs)/float64(b.N), "allocs/op")
	b.ReportMetric(float64(m2.TotalAlloc-m1.TotalAlloc)/float64(b.N), "bytes/op")
}

// BenchmarkPeek benchmarks Peek operation performance
func BenchmarkPeek(b *testing.B) {
	cache := New[string](1600) // 100 * 16 bytes = 1600 bytes
	cache.Reserve()

	key := "peek-key"
	value := "peek-value"
	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return value, nil
	}

	// Pre-populate cache
	_, _, _ = cache.Get(b.Context(), key, ttl, loader)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cache.Peek(key)
		}
	})
}

// BenchmarkDelete benchmarks Delete operation performance
func BenchmarkDelete(b *testing.B) {
	cache := New[string](1600) // 100 * 16 bytes = 1600 bytes
	cache.Reserve()

	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Pre-populate cache with many keys
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("delete-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("delete-key-%d", i%1000)
			cache.Delete(key)
			i++
		}
	})
}

// BenchmarkConcurrentAccess benchmarks concurrent access patterns
func BenchmarkConcurrentAccess(b *testing.B) {
	cache := New[string](1600) // 100 * 16 bytes = 1600 bytes
	cache.Reserve()

	ttl := 5 * time.Minute
	numKeys := 10

	loader := func(ctx context.Context, k string) (string, error) {
		time.Sleep(10 * time.Microsecond) // Simulate work
		return "value-" + k, nil
	}

	// Pre-populate cache
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("concurrent-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("concurrent-key-%d", i%numKeys)
			_, _, _ = cache.Get(b.Context(), key, ttl, loader)
			i++
		}
	})
}

// BenchmarkLRUEviction benchmarks LRU eviction performance
func BenchmarkLRUEviction(b *testing.B) {
	cache := New[string](160) // 10 * 16 bytes = 160 bytes, small cache to force evictions
	cache.Reserve()

	ttl := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("evict-key-%d", i)
			_, _, _ = cache.Get(b.Context(), key, ttl, loader)
			i++
		}
	})
}

// BenchmarkDifferentSizes benchmarks different cache sizes
func BenchmarkDifferentSizes(b *testing.B) {
	sizes := []struct {
		name  string
		bytes int
	}{
		{"Small", 160},   // 10 entries
		{"Medium", 1600}, // 100 entries
		{"Large", 16000}, // 1000 entries
	}

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			cache := New[string](size.bytes)
			cache.Reserve()

			ttl := 5 * time.Minute
			loader := func(ctx context.Context, k string) (string, error) {
				return "value-" + k, nil
			}

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					key := fmt.Sprintf("size-key-%d", i)
					_, _, _ = cache.Get(b.Context(), key, ttl, loader)
					i++
				}
			})
		})
	}
}

// BenchmarkDifferentDataTypes benchmarks different data types
func BenchmarkDifferentDataTypes(b *testing.B) {
	// String cache
	b.Run("String", func(b *testing.B) {
		cache := New[string](1600)
		cache.Reserve()

		ttl := 5 * time.Minute
		loader := func(ctx context.Context, k string) (string, error) {
			return "value-" + k, nil
		}

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := fmt.Sprintf("string-key-%d", i)
				_, _, _ = cache.Get(b.Context(), key, ttl, loader)
				i++
			}
		})
	})

	// Int cache
	b.Run("Int", func(b *testing.B) {
		cache := New[int](1600)
		cache.Reserve()

		ttl := 5 * time.Minute
		loader := func(ctx context.Context, k string) (int, error) {
			return len(k), nil
		}

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := fmt.Sprintf("int-key-%d", i)
				_, _, _ = cache.Get(b.Context(), key, ttl, loader)
				i++
			}
		})
	})

	// Struct cache
	type TestStruct struct {
		ID    int
		Name  string
		Value float64
	}

	b.Run("Struct", func(b *testing.B) {
		cache := New[TestStruct](1600)
		cache.Reserve()

		ttl := 5 * time.Minute
		loader := func(ctx context.Context, k string) (TestStruct, error) {
			return TestStruct{
				ID:    len(k),
				Name:  "name-" + k,
				Value: float64(len(k)),
			}, nil
		}

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := fmt.Sprintf("struct-key-%d", i)
				_, _, _ = cache.Get(b.Context(), key, ttl, loader)
				i++
			}
		})
	})
}

// BenchmarkTTLExpiration benchmarks TTL expiration handling
func BenchmarkTTLExpiration(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	shortTTL := 1 * time.Millisecond
	longTTL := 5 * time.Minute

	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Pre-populate with short TTL
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("ttl-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, shortTTL, loader)
	}

	// Wait for expiration
	time.Sleep(shortTTL + 10*time.Millisecond)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("ttl-key-%d", i%100)
			_, _, _ = cache.Get(b.Context(), key, longTTL, loader)
			i++
		}
	})
}

// BenchmarkZeroTTL benchmarks zero TTL (no caching) performance
func BenchmarkZeroTTL(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	loader := func(ctx context.Context, k string) (string, error) {
		time.Sleep(1 * time.Microsecond) // Simulate work
		return "value-" + k, nil
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("zero-ttl-key-%d", i)
			_, _, _ = cache.Get(b.Context(), key, 0, loader)
			i++
		}
	})
}

// BenchmarkSingleflight benchmarks singleflight behavior
func BenchmarkSingleflight(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	key := "singleflight-key"
	value := "singleflight-value"
	ttl := 5 * time.Minute

	callCount := int32(0)
	loader := func(ctx context.Context, k string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(100 * time.Microsecond) // Simulate work
		return value, nil
	}

	// Pre-populate cache
	_, _, _ = cache.Get(b.Context(), key, ttl, loader)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _ = cache.Get(b.Context(), key, ttl, loader)
		}
	})

	// Verify singleflight behavior
	finalCallCount := atomic.LoadInt32(&callCount)
	b.ReportMetric(float64(finalCallCount), "loader-calls")
}

// BenchmarkMemoryUsage benchmarks memory usage patterns
func BenchmarkMemoryUsage(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("memory-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	b.ReportMetric(float64(m2.Mallocs-m1.Mallocs)/float64(b.N), "allocs/op")
	b.ReportMetric(float64(m2.TotalAlloc-m1.TotalAlloc)/float64(b.N), "bytes/op")
}

// BenchmarkHitRateCalculation benchmarks hit rate calculation performance
func BenchmarkHitRateCalculation(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Pre-populate cache and generate some hits/misses
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("hitrate-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	// Generate some hits
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("hitrate-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.HitRate()
	}
}

// BenchmarkStatsCalculation benchmarks stats calculation performance
func BenchmarkStatsCalculation(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Pre-populate cache and generate some hits/misses
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("stats-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	// Generate some hits
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("stats-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.Stats()
	}
}

// BenchmarkConcurrentMixedOperations benchmarks mixed concurrent operations
func BenchmarkConcurrentMixedOperations(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) {
		time.Sleep(10 * time.Microsecond)
		return "value-" + k, nil
	}

	// Pre-populate cache
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("mixed-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("mixed-key-%d", i%100)

			// Mix of operations
			switch i % 4 {
			case 0:
				_, _, _ = cache.Get(b.Context(), key, ttl, loader)
			case 1:
				_, _ = cache.Peek(key)
			case 2:
				cache.Delete(key)
			case 3:
				_ = cache.HitRate()
			}
			i++
		}
	})
}

// BenchmarkCacheReset benchmarks cache reset performance
func BenchmarkCacheReset(b *testing.B) {
	cache := New[string](1600)
	cache.Reserve()

	ttl := 5 * time.Minute
	loader := func(ctx context.Context, k string) (string, error) {
		return "value-" + k, nil
	}

	// Pre-populate cache
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("reset-key-%d", i)
		_, _, _ = cache.Get(b.Context(), key, ttl, loader)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Reset()

		// Re-populate after reset
		for j := 0; j < 10; j++ {
			key := fmt.Sprintf("reset-key-%d", j)
			_, _, _ = cache.Get(b.Context(), key, ttl, loader)
		}
	}
}
