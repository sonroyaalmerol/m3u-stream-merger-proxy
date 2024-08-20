package database

import (
	"sync"
	"time"
)

type CacheEntry struct {
	Data      []StreamInfo
	Timestamp time.Time
}

type Cache struct {
	mu      sync.Mutex
	entries map[string]CacheEntry
}

func NewCache() *Cache {
	return &Cache{
		entries: make(map[string]CacheEntry),
	}
}

func (c *Cache) Get(key string) ([]StreamInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, found := c.entries[key]
	if !found {
		return nil, false
	}

	return entry.Data, true
}

func (c *Cache) Set(key string, data []StreamInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = CacheEntry{
		Data:      data,
		Timestamp: time.Now(),
	}
}

func (c *Cache) Clear(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.entries[key]
	if ok {
		delete(c.entries, key)
	}
}
