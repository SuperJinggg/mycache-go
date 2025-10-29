package mycache

import (
	"container/list"
	"time"
)

// ============================================================
// LRUKCache - LRU-K 缓存算法实现
// ============================================================

// LRUKCache 实现了LRU-K算法（K=2）
// 相比传统LRU，能够更好地识别真正的热点数据，减少缓存污染
type LRUKCache struct {
	maxEntries int
	k          int // K值，默认为2
	
	// 缓存队列：存储访问次数>=K的数据
	cacheList *list.List
	cache     map[string]*list.Element
	
	// 历史队列：记录访问次数<K的数据
	historyList *list.List
	history     map[string]*list.Element
	
	// 访问计数
	accessCount map[string]int
	accessTime  map[string][]time.Time
	
	// 淘汰回调
	OnEvicted func(key string, value interface{})
}

// kEntry 缓存条目
type kEntry struct {
	key        string
	value      interface{}
	accessTime time.Time
}

// NewLRUK 创建LRU-K缓存
func NewLRUK(maxEntries int, k int) *LRUKCache {
	if k < 1 {
		k = 2 // 默认K=2
	}
	
	return &LRUKCache{
		maxEntries:  maxEntries,
		k:           k,
		cacheList:   list.New(),
		cache:       make(map[string]*list.Element),
		historyList: list.New(),
		history:     make(map[string]*list.Element),
		accessCount: make(map[string]int),
		accessTime:  make(map[string][]time.Time),
	}
}

// Add 添加条目到缓存
func (c *LRUKCache) Add(key string, value interface{}) {
	now := time.Now()
	
	// 如果已在缓存队列中，更新值
	if ele, exists := c.cache[key]; exists {
		c.cacheList.MoveToFront(ele)
		entry := ele.Value.(*kEntry)
		entry.value = value
		entry.accessTime = now
		return
	}
	
	// 如果在历史队列中
	if ele, exists := c.history[key]; exists {
		entry := ele.Value.(*kEntry)
		entry.value = value
		entry.accessTime = now
		
		c.accessCount[key]++
		c.updateAccessTime(key, now)
		
		// 如果访问次数达到K，提升到缓存队列
		if c.accessCount[key] >= c.k {
			c.promoteToCache(key, value)
		} else {
			c.historyList.MoveToFront(ele)
		}
		return
	}
	
	// 新条目，添加到历史队列
	c.addToHistory(key, value, now)
	
	// 检查大小限制
	c.enforceMaxEntries()
}

// Get 获取缓存值
func (c *LRUKCache) Get(key string) (interface{}, bool) {
	now := time.Now()
	
	// 检查缓存队列
	if ele, hit := c.cache[key]; hit {
		c.cacheList.MoveToFront(ele)
		entry := ele.Value.(*kEntry)
		entry.accessTime = now
		c.updateAccessTime(key, now)
		return entry.value, true
	}
	
	// 检查历史队列
	if ele, hit := c.history[key]; hit {
		entry := ele.Value.(*kEntry)
		
		c.accessCount[key]++
		c.updateAccessTime(key, now)
		
		// 如果访问次数达到K，提升到缓存队列
		if c.accessCount[key] >= c.k {
			c.promoteToCache(key, entry.value)
		} else {
			c.historyList.MoveToFront(ele)
			entry.accessTime = now
		}
		
		return entry.value, true
	}
	
	return nil, false
}

// Remove 移除指定key
func (c *LRUKCache) Remove(key string) {
	// 从缓存队列移除
	if ele, exists := c.cache[key]; exists {
		c.removeElement(c.cacheList, ele, c.cache)
	}
	
	// 从历史队列移除
	if ele, exists := c.history[key]; exists {
		c.removeElement(c.historyList, ele, c.history)
	}
	
	// 清理访问记录
	delete(c.accessCount, key)
	delete(c.accessTime, key)
}

// RemoveOldest 移除最旧的条目
func (c *LRUKCache) RemoveOldest() {
	// 优先从历史队列移除
	if c.historyList.Len() > 0 {
		ele := c.historyList.Back()
		c.removeElement(c.historyList, ele, c.history)
		return
	}
	
	// 从缓存队列移除
	if c.cacheList.Len() > 0 {
		ele := c.cacheList.Back()
		c.removeElement(c.cacheList, ele, c.cache)
	}
}

// Len 返回缓存中的条目总数
func (c *LRUKCache) Len() int {
	return c.cacheList.Len() + c.historyList.Len()
}

// Clear 清空缓存
func (c *LRUKCache) Clear() {
	// 如果有淘汰回调，调用它
	if c.OnEvicted != nil {
		for _, e := range c.cache {
			kv := e.Value.(*kEntry)
			c.OnEvicted(kv.key, kv.value)
		}
		for _, e := range c.history {
			kv := e.Value.(*kEntry)
			c.OnEvicted(kv.key, kv.value)
		}
	}
	
	// 重新初始化
	c.cacheList = list.New()
	c.cache = make(map[string]*list.Element)
	c.historyList = list.New()
	c.history = make(map[string]*list.Element)
	c.accessCount = make(map[string]int)
	c.accessTime = make(map[string][]time.Time)
}

// ============================================================
// 内部辅助方法
// ============================================================

// updateAccessTime 更新访问时间记录
func (c *LRUKCache) updateAccessTime(key string, t time.Time) {
	times := c.accessTime[key]
	if times == nil {
		times = make([]time.Time, 0, c.k)
	}
	
	times = append(times, t)
	// 只保留最近K次访问时间
	if len(times) > c.k {
		times = times[len(times)-c.k:]
	}
	
	c.accessTime[key] = times
}

// addToHistory 添加到历史队列
func (c *LRUKCache) addToHistory(key string, value interface{}, t time.Time) {
	entry := &kEntry{
		key:        key,
		value:      value,
		accessTime: t,
	}
	
	ele := c.historyList.PushFront(entry)
	c.history[key] = ele
	c.accessCount[key] = 1
	c.updateAccessTime(key, t)
}

// promoteToCache 将条目从历史队列提升到缓存队列
func (c *LRUKCache) promoteToCache(key string, value interface{}) {
	// 从历史队列移除
	if ele, exists := c.history[key]; exists {
		c.historyList.Remove(ele)
		delete(c.history, key)
	}
	
	// 添加到缓存队列
	entry := &kEntry{
		key:        key,
		value:      value,
		accessTime: time.Now(),
	}
	
	ele := c.cacheList.PushFront(entry)
	c.cache[key] = ele
}

// removeElement 移除元素
func (c *LRUKCache) removeElement(l *list.List, e *list.Element, m map[string]*list.Element) {
	l.Remove(e)
	kv := e.Value.(*kEntry)
	delete(m, kv.key)
	
	// 调用淘汰回调
	if c.OnEvicted != nil {
		c.OnEvicted(kv.key, kv.value)
	}
}

// enforceMaxEntries 强制执行最大条目限制
func (c *LRUKCache) enforceMaxEntries() {
	if c.maxEntries <= 0 {
		return
	}
	
	// 历史队列最大为缓存大小的50%
	maxHistorySize := c.maxEntries / 2
	if maxHistorySize < 1 {
		maxHistorySize = 1
	}
	
	// 限制历史队列大小
	for c.historyList.Len() > maxHistorySize {
		ele := c.historyList.Back()
		c.removeElement(c.historyList, ele, c.history)
	}
	
	// 限制总大小
	for c.Len() > c.maxEntries {
		c.RemoveOldest()
	}
}
