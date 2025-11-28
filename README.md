# Perch

[![Go Report Card](https://goreportcard.com/badge/github.com/binaek/perch)](https://goreportcard.com/report/github.com/binaek/perch)
[![GoDoc](https://pkg.go.dev/badge/github.com/binaek/perch)](https://pkg.go.dev/github.com/binaek/perch)
[![codecov](https://codecov.io/gh/binaek/perch/branch/main/graph/badge.svg)](https://codecov.io/gh/binaek/perch)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

A high-performance, bounded, per-key TTL, singleflight, zero-allocation-on-hit LRU cache for Go.

## Features

- **Zero Allocation on Hit**: Cache hits don't cause any memory allocations, making it extremely efficient for high-frequency access patterns
- **Bounded Memory**: Capacity is specified in bytes, with automatic calculation of the number of slots based on the size of the cached type
- **Per-Key TTL**: Each cache entry can have its own time-to-live (TTL)
- **Singleflight**: Concurrent requests for the same key will only execute the `loader function` once
- **LRU Eviction**: Least Recently Used eviction policy when capacity is exceeded
- **Hit Rate Tracking**: Built-in statistics for monitoring cache effectiveness
- **Thread-Safe**: Fully concurrent and safe for use across multiple goroutines
- **Type-Safe**: Generic implementation that works with any Go type
- **Panic Recovery**: `Loader` function panics are caught and converted to errors
- **Context Support**: Full `context.Context` support for cancellation and timeouts

## Installation

```bash
go get github.com/binaek/perch
```

## Quick Start

```go
package main

import (
  "context"
  "fmt"
  "time"

  "github.com/binaek/perch"
)

func main() {
  // Create a cache with 1KB capacity for string values
  cache := perch.New[string](1024)

  // Reserve memory for the cache (required before use)
  cache.Reserve()

  // Define a loader function
  loader := func(ctx context.Context, key string) (string, error) {
    // Simulate expensive operation
    time.Sleep(100 * time.Millisecond)
    return "value-for-" + key, nil
  }

  ctx := context.Background()

  // Get value with 5-minute TTL
  value, hit, err := cache.Get(ctx, "my-key", 5*time.Minute, loader)
  if err != nil {
    panic(err)
  }
  fmt.Printf("Value: %s, Cache hit: %t\n", value, hit) // "value-for-my-key", false

  // Subsequent calls will hit the cache (zero allocation)
  value, hit, err = cache.Get(ctx, "my-key", 5*time.Minute, loader)
  // loader function won't be called again
  fmt.Printf("Value: %s, Cache hit: %t\n", value, hit) // "value-for-my-key", true

  // ttl == 0 pins the value (expires ~99 years later internally)
  cache.Get(ctx, "pinned-key", 0, loader)

  // ttl < 0 bypasses caching but still shares the loader result
  value, hit, err = cache.Get(ctx, "live-key", -1, loader)
  fmt.Printf("Value: %s, Cache hit: %t\n", value, hit) // hit will always be false

  // Peek at the value without affecting LRU order
  peekValue, found := cache.Peek("my-key")
  if found {
    fmt.Println("Peeked value:", peekValue) // "value-for-my-key"
  }
}
```

## TTL Semantics & Singleflight

Perch accepts any TTL and adapts its behavior:

- `ttl > 0`: cache with the provided expiry
- `ttl == 0`: cache indefinitely (implemented as a ~99-year expiry) while still respecting LRU eviction
- `ttl < 0`: bypass caching entirely; the loader still runs once per key thanks to singleflight, and all waiters receive the same result before the entry is evicted

This flexibility makes it easy to mix cached, pinned, and real-time reads while still benefiting from the built-in singleflight protections.

> **Note:** TTL changes take effect only when the loader runs. If a subsequent `Get` call uses a different TTL while the cached value is still fresh, Perch keeps the original expiry until the entry is reloaded (e.g., after it expires, is deleted, or you force a reload with `ttl < 0`). This prevents one caller from unintentionally shortening or lengthening the lifetime chosen by the loader that populated the cache.

## API Reference

### New[T any](capacityBytes int) \*Perch[T]

Creates a new Perch cache with the specified capacity in bytes. The actual number of slots is calculated as `capacityBytes / sizeof(T)`.

```go
cache := perch.New[string](1024) // 1KB capacity for strings
```

### Reserve()

Allocates all the slots required for the cache. **Must be called before the cache is used**. Safe to call multiple times - only allocates once.

```go
cache.Reserve()
```

### Get(ctx, key, ttl, loader) (T, bool, error)

Retrieves a value from the cache. If the value is not present or has expired, the loader function is called to load it.

- `ctx`: Context for cancellation and timeouts
- `key`: Cache key
- `ttl`: Time-to-live for the cached value (`0` caches indefinitely, `ttl < 0` bypasses caching, and TTL changes apply when the loader runs)
- `loader`: Function to load the value if not in cache
- Returns: `(value, cacheHit, error)` where `cacheHit` indicates if the value was found in cache

```go
value, hit, err := cache.Get(ctx, "key", 5*time.Minute, loader)
if err != nil {
    // handle error
}
if hit {
  fmt.Println("Cache hit!")
} else {
  fmt.Println("Cache miss - value was loaded")
}
```

### Peek(key) (T, bool)

Returns the cached value if present and fresh, without affecting the LRU order or calling the loader.

```go
value, found := cache.Peek("key")
if found {
  fmt.Println("Value:", value)
}
```

### Delete(key)

Removes a key from the cache.

```go
cache.Delete("key")
```

### Cap() int

Returns the number of slots in the cache.

```go
fmt.Println("Cache capacity:", cache.Cap())
```

### HitRate() float64

Returns the current hit rate as a percentage (0.0 to 100.0).

```go
hitRate := cache.HitRate()
fmt.Printf("Cache hit rate: %.2f%%\n", hitRate)
```

### Stats() CacheStats

Returns detailed cache statistics including hits, misses, total requests, hit rate, capacity, and current size.

```go
stats := cache.Stats()
fmt.Printf("Hits: %d, Misses: %d, Hit Rate: %.2f%%\n",
    stats.Hits, stats.Misses, stats.HitRate)
```

### ResetStats()

Resets the hit/miss counters to zero without affecting cached data.

```go
cache.ResetStats() // Reset statistics but keep cached data
```

### Reset()

Resets the cache to its initial state, clearing all entries and statistics.

```go
cache.Reset()
```

## Cache Statistics

Perch provides built-in hit rate tracking and detailed statistics to help you monitor cache effectiveness.

### CacheStats Type

```go
type CacheStats struct {
  Hits     uint64  // Number of cache hits
  Misses   uint64  // Number of cache misses
  Total    uint64  // Total number of requests (hits + misses)
  HitRate  float64 // Hit rate as a percentage (0.0 to 100.0)
  Capacity int     // Cache capacity in number of slots
  Size     int     // Current number of items in cache
}
```

### Monitoring Cache Performance

```go
// Monitor hit rate over time
for i := 0; i < 1000; i++ {
  value, hit, err := cache.Get(ctx, "key", ttl, loader)
  if err != nil {
    continue
  }

  if i%100 == 0 { // Check every 100 requests
    stats := cache.Stats()
    fmt.Printf("Requests: %d, Hit Rate: %.2f%%, Size: %d/%d\n",
        stats.Total, stats.HitRate, stats.Size, stats.Capacity)
  }
}
```

### Hit Rate Best Practices

1. **Monitor Regularly**: Check hit rates during development and production
1. **Set Targets**: Aim for hit rates above 80% for most use cases
1. **Analyze Patterns**: Low hit rates may indicate:
   - Cache size too small
   - TTL too short
   - Poor access patterns
   - Need for different eviction strategy

```go
// Example: Monitor cache performance over time windows
func monitorCache(cache *perch.Perch[string], duration time.Duration) {
  ticker := time.NewTicker(duration)
  defer ticker.Stop()

  for range ticker.C {
    stats := cache.Stats()
    if stats.Total > 0 {
      fmt.Printf("Window: Hits=%d, Misses=%d, HitRate=%.2f%%\n",
        stats.Hits, stats.Misses, stats.HitRate)

      // Reset for next window
      cache.ResetStats()
    }
  }
}
```

## Advanced Usage

### Explicit TTL Controls

Use TTL values to dial in caching semantics per call:

```go
// Pin the value indefinitely (subject to LRU eviction when capacity is full)
cache.Get(ctx, "key", 0, loader)

// Disable caching for this call; the loader still runs once due to singleflight
value, hit, err := cache.Get(ctx, "key", -1, loader) // hit is always false
```

### Error Handling

Errors from loader functions are not cached and will be returned immediately:

```go
loader := func(ctx context.Context, key string) (string, error) {
  if key == "invalid" {
    return "", errors.New("invalid key")
  }
  return "value", nil
}

value, hit, err := cache.Get(ctx, "invalid", 5*time.Minute, loader)
if err != nil {
  // Handle error
  // hit will be false since errors are not cached
}
```

### Context Cancellation

The cache respects context cancellation:

```go
ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
defer cancel()

value, hit, err := cache.Get(ctx, "key", 5*time.Minute, loader)
if err != nil {
  // Could be context.DeadlineExceeded
  // hit will be false since errors are not cached
}
```

### Custom Types

Perch works with any Go type:

```go
type User struct {
  ID   int
  Name string
}

cache := perch.New[User](1024)
cache.Reserve()

loader := func(ctx context.Context, id string) (User, error) {
  // Load user from database
  return User{ID: 1, Name: "John"}, nil
}

user, hit, err := cache.Get(ctx, "user:1", 10*time.Minute, loader)
if err != nil {
  // handle error
}
if hit {
  fmt.Println("User loaded from cache")
} else {
  fmt.Println("User loaded from database")
}
```

## Performance Characteristics

- **Cache Hits**: Sub-microsecond latency with zero allocations
- **Cache Misses**: Performance depends on the loader function
- **Memory Usage**: Bounded by the specified capacity in bytes
- **Concurrency**: Excellent performance under high concurrent load
- **LRU Operations**: O(1) for most operations

## Benchmark Results

Comprehensive benchmarks demonstrate Perch's exceptional performance across various scenarios. All benchmarks were run on Apple M3 Pro with Go 1.24.

### Core Performance Benchmarks

| Benchmark      | Operations/sec | ns/op | Allocations/op | Bytes/op |
| -------------- | -------------- | ----- | -------------- | -------- |
| **Cache Hit**  | 9,110,590      | 129.8 | 0              | 0        |
| **Cache Miss** | 1,982,652      | 622.3 | 2              | 45       |
| **Peek**       | 5,787,956      | 192.9 | 0              | 0        |
| **Delete**     | 12,150,006     | 98.0  | 1              | 21       |

### Concurrent Performance

| Benchmark                | Operations/sec | ns/op | Allocations/op | Bytes/op |
| ------------------------ | -------------- | ----- | -------------- | -------- |
| **Concurrent Access**    | 6,404,605      | 188.0 | 1              | 16       |
| **Concurrent Mixed Ops** | 6,479,191      | 181.4 | 1              | 16       |
| **Singleflight**         | 9,111,879      | 131.9 | 0              | 0        |

### Cache Size Scaling

| Cache Size               | Operations/sec | ns/op | Allocations/op | Bytes/op |
| ------------------------ | -------------- | ----- | -------------- | -------- |
| **Small (10 entries)**   | 2,762,568      | 436.1 | 2              | 48       |
| **Medium (100 entries)** | 2,498,115      | 469.9 | 2              | 47       |
| **Large (1000 entries)** | 3,599,035      | 403.1 | 2              | 37       |

### Data Type Performance

| Data Type  | Operations/sec | ns/op | Allocations/op | Bytes/op |
| ---------- | -------------- | ----- | -------------- | -------- |
| **String** | 2,570,358      | 473.8 | 2              | 51       |
| **Int**    | 2,672,947      | 452.1 | 1              | 24       |
| **Struct** | 2,622,630      | 463.1 | 2              | 52       |

### Specialized Operations

| Benchmark          | Operations/sec | ns/op | Allocations/op | Bytes/op |
| ------------------ | -------------- | ----- | -------------- | -------- |
| **LRU Eviction**   | 2,741,290      | 432.7 | 2              | 48       |
| **TTL Expiration** | 6,554,294      | 181.1 | 1              | 16       |
| **Zero TTL**       | 4,923,733      | 247.5 | 2              | 62       |
| **Memory Usage**   | 4,601,263      | 230.7 | 3              | 55.86    |

### Statistics Operations

| Benchmark                | Operations/sec | ns/op | Allocations/op | Bytes/op |
| ------------------------ | -------------- | ----- | -------------- | -------- |
| **Hit Rate Calculation** | 303,347,949    | 3.942 | 0              | 0        |
| **Stats Calculation**    | 217,571,858    | 5.535 | 0              | 0        |

### Key Performance Highlights

- **Zero Allocation on Hit**: Cache hits produce 0 allocations, making them extremely efficient
- **High Throughput**: Over 9M operations per second for cache hits
- **Excellent Concurrency**: Maintains high performance under concurrent load
- **Memory Efficient**: Minimal memory overhead with bounded allocations
- **Fast Statistics**: Hit rate and stats calculations are extremely fast (sub-10ns)
- **Scalable**: Performance remains consistent across different cache sizes

### Running Benchmarks

To run the benchmarks yourself:

```bash
# Run all benchmarks
go test -bench=. -benchmem

# Run specific benchmarks
go test -bench=BenchmarkCacheHit -benchmem

# Run with more iterations for better accuracy
go test -bench=. -benchmem -benchtime=10s

# Run with CPU profiling
go test -bench=. -benchmem -cpuprofile=cpu.prof
```

## LRU Eviction Policy

Perch implements a sophisticated Least Recently Used (LRU) eviction policy to manage cache capacity efficiently. Here's how it works:

### How LRU Works

1. **Access Order Tracking**: Every time a value is accessed via `Get()`, it's moved to the Most Recently Used (MRU) position
2. **Eviction Strategy**: When the cache reaches capacity, the Least Recently Used item is evicted to make room for new entries
3. **Peek Behavior**: The `Peek()` method doesn't affect LRU order - it only reads without updating access time

### LRU Implementation Details

- **Intrusive Doubly-Linked List**: Uses an efficient intrusive doubly-linked list with O(1) operations
- **1-Based Indexing**: Internal slot indexing starts at 1, with 0 reserved for null/empty slots
- **Atomic Operations**: LRU updates are performed under appropriate locks to ensure thread safety

### LRU Examples

```go
// Create a small cache to demonstrate LRU behavior
cache := perch.New[string](64) // 4 * 16 bytes = 4 slots
cache.Reserve()

loader := func(ctx context.Context, key string) (string, error) {
  return "value-" + key, nil
}

// Load 4 items (fills cache)
cache.Get(ctx, "key1", 5*time.Minute, loader) // MRU: key1
cache.Get(ctx, "key2", 5*time.Minute, loader) // MRU: key2, LRU: key1
cache.Get(ctx, "key3", 5*time.Minute, loader) // MRU: key3, LRU: key1
cache.Get(ctx, "key4", 5*time.Minute, loader) // MRU: key4, LRU: key1

// Access key1 to move it to MRU
cache.Get(ctx, "key1", 5*time.Minute, loader) // MRU: key1, LRU: key2

// Add key5 - key2 gets evicted (was LRU)
cache.Get(ctx, "key5", 5*time.Minute, loader) // MRU: key5, LRU: key3

// key2 is no longer in cache
_, found := cache.Peek("key2")
fmt.Println(found) // false

// key1, key3, key4, key5 are still present
```

### LRU with TTL Interaction

LRU eviction works in conjunction with TTL expiration:

1. **Expired Items**: Items that have expired are treated as if they don't exist
2. **Eviction Priority**: When at capacity, expired items are evicted first, then LRU items
3. **Fresh Access**: Accessing a fresh item moves it to MRU position
4. **Stale Access**: Accessing an expired item triggers a reload and moves the new value to MRU

### LRU Performance

- **Access Time**: O(1) - moving items to MRU position
- **Eviction Time**: O(1) - removing LRU item
- **Memory Overhead**: Minimal - only two pointers per entry (prev/next)
- **Concurrency**: Thread-safe with fine-grained locking

### LRU Best Practices

1. **Size Your Cache**: Choose an appropriate capacity based on your access patterns
2. **Monitor Hit Rates**: Use the built-in `HitRate()` and `Stats()` methods to monitor effectiveness
3. **Consider Access Patterns**: LRU works best with temporal locality (recently accessed items are likely to be accessed again)
4. **Peek vs Get**: Use `Peek()` when you don't want to affect LRU order, `Get()` when you do
5. **Track Performance**: Monitor hit rates over time to optimize cache configuration

```go
// Example: Monitoring cache effectiveness with built-in statistics
value, hit, err := cache.Get(ctx, key, ttl, loader)
if err != nil {
  // handle error
}

// Use built-in hit rate tracking
hitRate := cache.HitRate()
fmt.Printf("Current hit rate: %.2f%%\n", hitRate)

// Get detailed statistics
stats := cache.Stats()
fmt.Printf("Cache stats: %d hits, %d misses, %d total, %.2f%% hit rate\n",
  stats.Hits, stats.Misses, stats.Total, stats.HitRate)
```

## Thread Safety

Perch is fully thread-safe and can be used concurrently from multiple goroutines. The implementation uses fine-grained locking to minimize contention:

- Global mutex for the cache structure
- Per-entry mutexes for individual cache entries
- Singleflight behavior prevents duplicate loader calls

## Memory Management

The cache pre-allocates all memory during `Reserve()`, preventing runtime allocations during normal operation. The memory footprint is calculated based on:

- The size of the cached type `T`
- The specified capacity in bytes
- Overhead for LRU list pointers and metadata

## Testing

Perch includes comprehensive test coverage with multiple test suites and extensive benchmarks.

### Test Suites

Run the complete test suite:

```bash
go test ./...
```

Run specific test suites:

```bash
go test -run TestPerchTestSuite
go test -run TestPerformanceTestSuite
go test -run TestConcurrencyTestSuite
go test -run TestLRUTestSuite
go test -run TestTTLTestSuite
go test -run TestHitRateTestSuite
```

### Benchmark Tests

Run all benchmarks:

```bash
go test -bench=. -benchmem
```

Run specific benchmarks:

```bash
# Core performance
go test -bench=BenchmarkCacheHit -benchmem
go test -bench=BenchmarkCacheMiss -benchmem

# Concurrency tests
go test -bench=BenchmarkConcurrentAccess -benchmem
go test -bench=BenchmarkSingleflight -benchmem

# Memory allocation tests
go test -bench=BenchmarkCacheHitWithAllocationTracking -benchmem
go test -bench=BenchmarkMemoryUsage -benchmem

# Different cache sizes
go test -bench=BenchmarkDifferentSizes -benchmem

# Different data types
go test -bench=BenchmarkDifferentDataTypes -benchmem
```

### Test Coverage

The test suite includes:

- **Unit Tests**: Basic functionality, edge cases, and error handling
- **Performance Tests**: Memory allocation tracking and performance validation
- **Concurrency Tests**: Thread safety, singleflight behavior, and race conditions
- **LRU Tests**: Eviction policy validation and access pattern testing
- **TTL Tests**: Time-to-live expiration and refresh behavior
- **Hit Rate Tests**: Statistics accuracy and monitoring
- **Benchmark Tests**: Comprehensive performance measurement across all operations

### Continuous Integration

The project includes GitHub Actions for automated testing:

- Runs all test suites on every commit
- Executes benchmarks for performance regression detection
- Validates code coverage and quality metrics

## License

Copyright 2025 Binaek Sarkar

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
