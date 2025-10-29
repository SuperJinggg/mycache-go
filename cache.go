package mycache

import (
	"sync"
)

// ============================================================
// cache - 内部缓存包装
// ============================================================

// cache 是并发安全的缓存包装
type cache struct {
	mu         sync.RWMutex
	lru        *LRUKCache
	nbytes     int64
	nhit, nget int64
	nevict     int64
}

// add 添加条目到缓存
func (c *cache) add(key string, value ByteView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	if c.lru == nil {
		c.lru = NewLRUK(0, 2)
		c.lru.OnEvicted = func(key string, value interface{}) {
			val := value.(ByteView)
			c.nbytes -= int64(len(key)) + int64(val.Len())
			c.nevict++
		}
	}
	
	c.lru.Add(key, value)
	c.nbytes += int64(len(key)) + int64(value.Len())
}

// get 从缓存获取
func (c *cache) get(key string) (value ByteView, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nget++
	
	if c.lru == nil {
		return
	}
	
	vi, ok := c.lru.Get(key)
	if !ok {
		return
	}
	
	c.nhit++
	return vi.(ByteView), true
}

// remove 移除条目
func (c *cache) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	if c.lru != nil {
		c.lru.Remove(key)
	}
}

// removeOldest 移除最旧的条目
func (c *cache) removeOldest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	if c.lru != nil {
		c.lru.RemoveOldest()
	}
}

// bytes 返回缓存字节数
func (c *cache) bytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nbytes
}

// items 返回缓存条目数
func (c *cache) items() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.itemsLocked()
}

func (c *cache) itemsLocked() int64 {
	if c.lru == nil {
		return 0
	}
	return int64(c.lru.Len())
}

// stats 返回缓存统计
func (c *cache) stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Bytes:     c.nbytes,
		Items:     c.itemsLocked(),
		Gets:      c.nget,
		Hits:      c.nhit,
		Evictions: c.nevict,
	}
}
