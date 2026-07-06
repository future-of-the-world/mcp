// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLRUCache(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](10, 5*time.Minute)

	require.NotNil(t, cache)
	assert.Equal(t, 0, cache.Size())
}

func TestLRUCache_SetGet(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](10, 5*time.Minute)

	cache.Set("key1", "value1")

	val, ok := cache.Get("key1")
	require.True(t, ok)
	assert.Equal(t, "value1", val)
}

func TestLRUCache_GetMissing(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](10, 5*time.Minute)

	_, ok := cache.Get("nonexistent")
	assert.False(t, ok)
}

func TestLRUCache_GetExpired(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](10, 50*time.Millisecond)

	cache.Set("key1", "value1")

	time.Sleep(100 * time.Millisecond)

	_, ok := cache.Get("key1")
	assert.False(t, ok)
	assert.Equal(t, 0, cache.Size())
}

func TestLRUCache_SetOverwrites(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](10, 5*time.Minute)

	cache.Set("key1", "value1")
	cache.Set("key1", "value2")

	val, ok := cache.Get("key1")
	require.True(t, ok)
	assert.Equal(t, "value2", val)
	assert.Equal(t, 1, cache.Size())
}

func TestLRUCache_Eviction(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](3, 5*time.Minute)

	cache.Set("a", "1")
	cache.Set("b", "2")
	cache.Set("c", "3")
	cache.Set("d", "4") // evicts "a"

	_, ok := cache.Get("a")
	assert.False(t, ok)

	val, ok := cache.Get("d")
	require.True(t, ok)
	assert.Equal(t, "4", val)

	assert.Equal(t, 3, cache.Size())
}

func TestLRUCache_GetRefreshesRecency(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](3, 5*time.Minute)

	cache.Set("a", "1")
	cache.Set("b", "2")
	cache.Set("c", "3")

	// Access "a" so it becomes most recent.
	_, ok := cache.Get("a")
	require.True(t, ok)

	// Adding "d" should evict "b" (now oldest), not "a".
	cache.Set("d", "4")

	_, ok = cache.Get("a")
	assert.Truef(t, ok, "'a' should still be present after recency refresh")

	_, ok = cache.Get("b")
	assert.Falsef(t, ok, "'b' should have been evicted")
}

func TestLRUCache_SetWithTTL(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](10, 5*time.Minute)

	cache.SetWithTTL("short", "data", 50*time.Millisecond)

	val, ok := cache.Get("short")
	require.True(t, ok)
	assert.Equal(t, "data", val)

	time.Sleep(100 * time.Millisecond)

	_, ok = cache.Get("short")
	assert.False(t, ok)
}

func TestLRUCache_Clear(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[string](10, 5*time.Minute)

	cache.Set("a", "1")
	cache.Set("b", "2")

	cache.Clear()

	assert.Equal(t, 0, cache.Size())

	_, ok := cache.Get("a")
	assert.False(t, ok)
}

func TestLRUCache_Size(t *testing.T) {
	t.Parallel()

	cache := NewLRUCache[int](10, 5*time.Minute)

	assert.Equal(t, 0, cache.Size())

	cache.Set("a", 1)
	assert.Equal(t, 1, cache.Size())

	cache.Set("b", 2)
	assert.Equal(t, 2, cache.Size())

	cache.Set("a", 99) // overwrite
	assert.Equal(t, 2, cache.Size())
}
