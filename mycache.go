// Package mycache 改进版分布式缓存实现
// 核心改进：LRU-K算法、HeavyKeeper热点检测、简洁的批量API
package mycache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	
	pb "mycache/mycachepb"
)

// ============================================================
// 全局变量和注册
// ============================================================

var (
	mu     sync.RWMutex
	groups = make(map[string]*Group)
)

// ============================================================
// Group - 缓存组（对外主要接口）
// ============================================================

// Group 是缓存的命名空间
type Group struct {
	name       string
	getter     Getter
	peersOnce  sync.Once
	peers      PeerPicker
	cacheBytes int64 // 缓存大小限制
	
	// 缓存
	mainCache cache
	hotCache  *cache
	
	// 热点检测
	hotDetector *HeavyKeeper
	
	// 防止缓存击穿
	loader flightGroup
	
	// 统计
	stats Stats  // 改为小写，避免与Stats()方法冲突
}

// ============================================================
// 公开API - 简洁的对外接口
// ============================================================

// Gets 批量获取缓存值
func (g *Group) Gets(keys []string) (map[string][]byte, map[string]error) {
	return g.gets(context.Background(), keys)
}

// GetsWithContext 带context的批量获取
func (g *Group) GetsWithContext(ctx context.Context, keys []string) (map[string][]byte, map[string]error) {
	return g.gets(ctx, keys)
}

// Sets 批量设置缓存值
func (g *Group) Sets(items map[string][]byte) map[string]error {
	return g.sets(context.Background(), items)
}

// Deletes 批量删除缓存值
func (g *Group) Deletes(keys []string) map[string]error {
	errors := make(map[string]error)
	for _, key := range keys {
		g.mainCache.remove(key)
		if g.hotCache != nil {
			g.hotCache.remove(key)
		}
	}
	return errors
}

// HotKeys 返回热点key列表
func (g *Group) HotKeys() []string {
	if g.hotDetector == nil {
		return nil
	}
	return g.hotDetector.TopK()
}

// CacheStats 缓存统计信息
type CacheStats struct {
	Bytes     int64   // 缓存字节数
	Items     int64   // 缓存条目数
	Gets      int64   // 总请求数
	Hits      int64   // 命中数
	HitRate   float64 // 命中率
	Evictions int64   // 驱逐次数
}

// Stats 获取缓存统计
func (g *Group) Stats() *CacheStats {
	mainStats := g.mainCache.stats()
	
	stats := &CacheStats{
		Bytes:     mainStats.Bytes,
		Items:     mainStats.Items,
		Gets:      mainStats.Gets,
		Hits:      mainStats.Hits,
		Evictions: mainStats.Evictions,
	}
	
	if stats.Gets > 0 {
		stats.HitRate = float64(stats.Hits) / float64(stats.Gets) * 100
	}
	
	return stats
}

// Name 返回组名
func (g *Group) Name() string {
	return g.name
}

// ============================================================
// Group创建和获取
// ============================================================

// NewGroup 创建新的缓存组
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	return newGroup(name, cacheBytes, getter, 0.2) // 默认20%热点缓存
}

// NewGroupWithHotCache 创建带热点缓存配置的组
func NewGroupWithHotCache(name string, cacheBytes int64, getter Getter, hotRatio float64) *Group {
	return newGroup(name, cacheBytes, getter, hotRatio)
}

func newGroup(name string, cacheBytes int64, getter Getter, hotRatio float64) *Group {
	if getter == nil {
		panic("nil Getter")
	}
	
	mu.Lock()
	defer mu.Unlock()
	
	if _, dup := groups[name]; dup {
		panic("duplicate registration of group " + name)
	}
	
	g := &Group{
		name:       name,
		getter:     getter,
		cacheBytes: cacheBytes,
		loader:     &singleflightGroup{},
	}
	
	// 配置热点缓存
	if hotRatio > 0 && hotRatio < 1 {
		g.hotCache = &cache{}
		g.hotDetector = NewHeavyKeeper(1000, 4, 100, 0.95)
	}
	
	groups[name] = g
	return g
}

// GetGroup 获取已存在的组
func GetGroup(name string) *Group {
	mu.RLock()
	defer mu.RUnlock()
	return groups[name]
}

// ============================================================
// 内部实现 - 单个操作
// ============================================================

// get 内部的单个获取方法
func (g *Group) get(ctx context.Context, key string) ([]byte, error) {
	g.stats.Gets.Add(1)
	
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}
	
	// 查找缓存
	if value, ok := g.lookupCache(key); ok {
		g.stats.CacheHits.Add(1)
		return value.ByteSlice(), nil
	}
	
	// 加载数据
	value, err := g.load(ctx, key)
	if err != nil {
		return nil, err
	}
	
	return value.ByteSlice(), nil
}

// lookupCache 查找缓存
func (g *Group) lookupCache(key string) (value ByteView, ok bool) {
	// 先查热点缓存
	if g.hotCache != nil {
		if value, ok = g.hotCache.get(key); ok {
			return
		}
	}
	
	// 查主缓存
	value, ok = g.mainCache.get(key)
	
	// 更新热点检测
	if ok && g.hotDetector != nil {
		g.hotDetector.Add(key)
		if g.hotDetector.IsHot(key) && g.hotCache != nil {
			g.hotCache.add(key, value)
		}
	}
	
	return
}

// load 加载数据
func (g *Group) load(ctx context.Context, key string) (value ByteView, err error) {
	g.stats.Loads.Add(1)
	
	// 使用singleflight防止缓存击穿
	viewi, err := g.loader.Do(key, func() (interface{}, error) {
		// 先尝试从peer加载
		if g.peers != nil {
			if peer, ok := g.peers.PickPeer(key); ok {
				value, err := g.getFromPeer(ctx, peer, key)
				if err == nil {
					g.stats.PeerLoads.Add(1)
					return value, nil
				}
				g.stats.PeerErrors.Add(1)
			}
		}
		
		// 本地加载
		g.stats.LocalLoads.Add(1)
		value, err := g.getLocally(ctx, key)
		if err != nil {
			g.stats.LocalLoadErrs.Add(1)
			return ByteView{}, err
		}
		
		// 填充缓存
		g.populateCache(key, value, &g.mainCache)
		
		// 更新热点检测
		if g.hotDetector != nil {
			g.hotDetector.Add(key)
		}
		
		return value, nil
	})
	
	if err != nil {
		return ByteView{}, err
	}
	
	return viewi.(ByteView), nil
}

// getLocally 本地获取数据
func (g *Group) getLocally(ctx context.Context, key string) (ByteView, error) {
	var value []byte
	err := g.getter.Get(ctx, key, AllocatingByteSliceSink(&value))
	if err != nil {
		return ByteView{}, err
	}
	return ByteView{b: cloneBytes(value)}, nil
}

// getFromPeer 从远程节点获取
func (g *Group) getFromPeer(ctx context.Context, peer ProtoGetter, key string) (ByteView, error) {
	req := &pb.GetRequest{
		Group: g.name,
		Key:   key,
	}
	res := &pb.GetResponse{}
	
	err := peer.Get(ctx, req, res)
	if err != nil {
		return ByteView{}, err
	}
	
	value := ByteView{b: res.Value}
	
	// 填充本地缓存
	g.populateCache(key, value, &g.mainCache)
	
	return value, nil
}

// populateCache 填充缓存
func (g *Group) populateCache(key string, value ByteView, cache *cache) {
	if g.cacheBytes <= 0 {
		return
	}
	
	cache.add(key, value)
	
	// 控制缓存大小
	for cache.bytes() > g.cacheBytes {
		cache.removeOldest()
	}
}

// initPeers 初始化节点选择器
func (g *Group) initPeers() {
	if g.peers == nil {
		g.peers = getPeers(g.name)
	}
}

// ============================================================
// 服务器端接口 - 处理来自其他节点的请求
// ============================================================

// 服务HTTP请求（由http.go调用）
func (g *Group) ServeHTTP(ctx context.Context, key string) ([]byte, error) {
	g.stats.ServerRequests.Add(1)
	
	// 直接调用内部get方法
	return g.get(ctx, key)
}

// ============================================================
// Stats - 统计信息
// ============================================================

// Stats 包含缓存统计
type Stats struct {
	Gets           AtomicInt // 任何Get请求
	CacheHits      AtomicInt // 缓存命中
	PeerLoads      AtomicInt // 远程加载成功
	PeerErrors     AtomicInt // 远程加载失败
	Loads          AtomicInt // 加载数（去重前）
	LocalLoads     AtomicInt // 本地加载
	LocalLoadErrs  AtomicInt // 本地加载失败
	ServerRequests AtomicInt // 作为服务器收到的请求
}

// AtomicInt 是原子整数
type AtomicInt int64

func (i *AtomicInt) Add(n int64) {
	atomic.AddInt64((*int64)(i), n)
}

func (i *AtomicInt) Get() int64 {
	return atomic.LoadInt64((*int64)(i))
}

func (i *AtomicInt) String() string {
	return fmt.Sprintf("%d", i.Get())
}

// ============================================================
// Getter - 数据获取接口
// ============================================================

// Getter 加载不在缓存中的key的数据
type Getter interface {
	Get(ctx context.Context, key string, dest Sink) error
}

// GetterFunc 是Getter接口的函数适配器
type GetterFunc func(ctx context.Context, key string, dest Sink) error

func (f GetterFunc) Get(ctx context.Context, key string, dest Sink) error {
	return f(ctx, key, dest)
}

// ============================================================
// flightGroup - 防止缓存击穿接口
// ============================================================

type flightGroup interface {
	Do(key string, fn func() (interface{}, error)) (interface{}, error)
}
