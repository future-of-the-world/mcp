// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package websearch provides foundation utilities for the web search MCP tool,
// including an LRU+TTL cache, result-ID minting, cursor encoding, and HTTP
// fetch with retry.
package websearch

import (
	"container/list"
	"sync"
	"time"
)

// entry holds a cached value together with its key, expiration time, and list
// element used for LRU ordering.
type entry[V any] struct {
	key     string
	value   V
	expires time.Time
}

// LRUCache is a generic in-memory cache with least-recently-used eviction and
// per-entry time-to-live expiration. It is safe for concurrent use.
type LRUCache[V any] struct {
	mu      sync.Mutex
	maxSize int
	ttl     time.Duration
	entries map[string]*list.Element
	order   *list.List
}

// NewLRUCache creates a cache that holds at most capacity entries. Each entry
// inserted with Set receives the default TTL. Use SetWithTTL to override on a
// per-entry basis.
func NewLRUCache[V any](capacity int, ttl time.Duration) *LRUCache[V] {
	return &LRUCache[V]{
		mu:      sync.Mutex{},
		maxSize: capacity,
		ttl:     ttl,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// Get retrieves the value for key. If the key is missing or expired it returns
// the zero value and false. A successful lookup refreshes recency.
func (c *LRUCache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[key]
	if !ok {
		var zero V

		return zero, false
	}

	ent, ok := elem.Value.(*entry[V])
	if !ok {
		var zero V

		return zero, false
	}

	if time.Now().After(ent.expires) {
		c.removeElement(elem)

		var zero V

		return zero, false
	}

	// Refresh recency.
	c.order.MoveToFront(elem)

	return ent.value, true
}

// Set stores value under key using the cache's default TTL. If key already
// exists the old entry is replaced. If the cache exceeds max capacity the
// least-recently-used entry is evicted.
func (c *LRUCache[V]) Set(key string, value V) {
	c.SetWithTTL(key, value, c.ttl)
}

// SetWithTTL stores value under key with an explicit TTL. If key already
// exists the old entry is replaced. If the cache exceeds max capacity the
// least-recently-used entry is evicted.
func (c *LRUCache[V]) SetWithTTL(key string, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove existing entry so it is re-inserted at the front.
	if elem, ok := c.entries[key]; ok {
		c.removeElement(elem)
	}

	ent := &entry[V]{
		key:     key,
		value:   value,
		expires: time.Now().Add(ttl),
	}

	elem := c.order.PushFront(ent)

	c.entries[key] = elem

	if c.order.Len() > c.maxSize {
		c.evictOldest()
	}
}

// Clear removes all entries from the cache.
func (c *LRUCache[V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*list.Element)
	c.order.Init()
}

// Size returns the number of entries currently in the cache.
func (c *LRUCache[V]) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.order.Len()
}

// removeElement removes a list element and its corresponding map entry.
func (c *LRUCache[V]) removeElement(elem *list.Element) {
	ent, ok := elem.Value.(*entry[V])
	if !ok {
		return
	}

	delete(c.entries, ent.key)
	c.order.Remove(elem)
}

// evictOldest removes the least-recently-used entry.
func (c *LRUCache[V]) evictOldest() {
	oldest := c.order.Back()
	if oldest != nil {
		c.removeElement(oldest)
	}
}
