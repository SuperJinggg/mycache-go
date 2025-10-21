// Package lru 实现了 LRU（Least Recently Used，最近最少使用）缓存
//
// LRU 算法原理：
// - 当缓存满时，优先淘汰最久未使用的条目
// - 每次访问（Get）或添加（Add）都会将条目移到最前面
// - 最旧的条目总是在链表尾部
package lru

import "container/list"

// ============================================================
// Cache - LRU 缓存
// ============================================================
// Cache 是一个 LRU 缓存。它不是并发安全的。
//
// 数据结构：
// - 使用双向链表维护访问顺序（container/list）
// - 使用 map 实现 O(1) 的查找
//
// 注意：此实现不是线程安全的，需要外部同步
// （groupcache 中的 cache 结构体提供了同步包装）
type Cache struct {
	// MaxEntries 是缓存淘汰条目前的最大条目数
	// 零值表示没有限制
	MaxEntries int

	// OnEvicted 可选地指定当条目从缓存中清除时执行的回调函数
	// 应用场景：
	// - 记录淘汰日志
	// - 更新统计信息
	// - 清理资源
	OnEvicted func(key Key, value interface{})

	ll    *list.List                    // 双向链表，维护访问顺序
	cache map[interface{}]*list.Element // key 到链表元素的映射
}

// ============================================================
// Key - 键类型
// ============================================================
// Key 可以是任何可比较的值
// 参见 http://golang.org/ref/spec#Comparison_operators
//
// 可比较的类型包括：
// - 基本类型：bool, 数字, string, 指针, channel
// - 结构体（如果所有字段都可比较）
// - 数组（如果元素类型可比较）
//
// 不可比较的类型：
// - 切片 (slice)
// - 映射 (map)
// - 函数 (function)
type Key interface{}

// entry 是链表中存储的元素
type entry struct {
	key   Key         // 键
	value interface{} // 值
}

// ============================================================
// New - 创建新的 LRU 缓存
// ============================================================
// New 创建一个新的 Cache
// 如果 maxEntries 为零，缓存没有限制，假定淘汰由调用者完成
func New(maxEntries int) *Cache {
	return &Cache{
		MaxEntries: maxEntries,
		ll:         list.New(),
		cache:      make(map[interface{}]*list.Element),
	}
}

// ============================================================
// Add - 添加条目到缓存
// ============================================================
// Add 向缓存添加一个值
//
// 工作流程：
// 1. 如果 key 已存在，更新值并移到前面（最近使用）
// 2. 如果 key 不存在，添加新条目到前面
// 3. 如果超出大小限制，移除最旧的条目
func (c *Cache) Add(key Key, value interface{}) {
	// 延迟初始化
	if c.cache == nil {
		c.cache = make(map[interface{}]*list.Element)
		c.ll = list.New()
	}

	// 如果 key 已存在
	if ee, ok := c.cache[key]; ok {
		// 将元素移到链表前面（标记为最近使用）
		c.ll.MoveToFront(ee)
		// 更新值
		ee.Value.(*entry).value = value
		return
	}

	// 添加新条目到链表前面
	ele := c.ll.PushFront(&entry{key, value})
	c.cache[key] = ele

	// 检查是否超出大小限制
	if c.MaxEntries != 0 && c.ll.Len() > c.MaxEntries {
		// 移除最旧的条目（链表尾部）
		c.RemoveOldest()
	}
}

// ============================================================
// Get - 从缓存获取值
// ============================================================
// Get 从缓存中查找 key 的值
//
// 副作用：如果 key 存在，会将其移到前面（标记为最近使用）
// 这是 LRU 算法的核心：访问会更新"最近使用"状态
func (c *Cache) Get(key Key) (value interface{}, ok bool) {
	if c.cache == nil {
		return
	}

	// 查找 key
	if ele, hit := c.cache[key]; hit {
		// 缓存命中，将元素移到前面
		c.ll.MoveToFront(ele)
		// 返回值
		return ele.Value.(*entry).value, true
	}

	// 缓存未命中
	return
}

// ============================================================
// Remove - 移除指定的 key
// ============================================================
// Remove 从缓存中移除指定的 key
func (c *Cache) Remove(key Key) {
	if c.cache == nil {
		return
	}

	// 查找并移除
	if ele, hit := c.cache[key]; hit {
		c.removeElement(ele)
	}
}

// ============================================================
// RemoveOldest - 移除最旧的条目
// ============================================================
// RemoveOldest 从缓存中移除最旧的条目
// 这是 LRU 淘汰算法的实现：总是淘汰最久未使用的
func (c *Cache) RemoveOldest() {
	if c.cache == nil {
		return
	}

	// 获取链表尾部元素（最旧的）
	ele := c.ll.Back()
	if ele != nil {
		c.removeElement(ele)
	}
}

// ------------------------------------------------------------
// removeElement - 移除链表元素（内部方法）
// ------------------------------------------------------------
func (c *Cache) removeElement(e *list.Element) {
	// 从链表中移除
	c.ll.Remove(e)
	// 获取键值对
	kv := e.Value.(*entry)
	// 从 map 中删除
	delete(c.cache, kv.key)
	// 如果有淘汰回调，调用它
	if c.OnEvicted != nil {
		c.OnEvicted(kv.key, kv.value)
	}
}

// ============================================================
// Len - 返回缓存中的条目数
// ============================================================
// Len 返回缓存中的条目数
func (c *Cache) Len() int {
	if c.cache == nil {
		return 0
	}
	return c.ll.Len()
}

// ============================================================
// Clear - 清空缓存
// ============================================================
// Clear 清除所有存储的条目
// 如果设置了 OnEvicted 回调，会为每个条目调用它
func (c *Cache) Clear() {
	// 如果有淘汰回调，为每个条目调用它
	if c.OnEvicted != nil {
		for _, e := range c.cache {
			kv := e.Value.(*entry)
			c.OnEvicted(kv.key, kv.value)
		}
	}
	// 清空数据结构
	c.ll = nil
	c.cache = nil
}
