// ============================================================
// Package mycache 提供了分布式缓存和数据加载机制
// ============================================================
// 核心功能：
// 1. 数据加载：通过 Getter 接口从数据源加载数据
// 2. 多级缓存：mainCache（主缓存）+ hotCache（热点缓存）
// 3. 去重：使用 singleflight 确保相同 key 只加载一次
// 4. 分布式：跨多个节点工作，每个 key 有唯一的拥有者节点
//
// 工作流程：
//  1. 首先查询本地缓存
//  2. 如果未命中，委托给该 key 的权威拥有者节点
//  3. 拥有者节点检查自己的缓存，或最终从数据源加载
//  4. 在常见场景下，多个节点对同一 key 的并发缓存未命中
//     只会导致一次实际的缓存填充操作
//
// ============================================================
package mycache

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"

	"mycache/lru"
	pb "mycache/mycachepb"
	"mycache/singleflight"
)

// ============================================================
// Getter 接口 - 数据加载器
// ============================================================
// Getter 负责为指定的 key 加载数据
type Getter interface {
	// Get 返回 key 对应的值，并填充到 dest 中
	//
	// 重要约束：
	// 返回的数据必须是无版本的。也就是说，key 必须唯一标识
	// 加载的数据，不依赖于隐式的当前时间，也不依赖于缓存
	// 过期机制。这确保了缓存的一致性。
	Get(ctx context.Context, key string, dest Sink) error
}

// ------------------------------------------------------------
// GetterFunc 是适配器类型，允许普通函数作为 Getter
// 类似于 http.HandlerFunc 的设计模式
// ------------------------------------------------------------
type GetterFunc func(ctx context.Context, key string, dest Sink) error

// Get 实现 Getter 接口
func (f GetterFunc) Get(ctx context.Context, key string, dest Sink) error {
	return f(ctx, key, dest)
}

// ============================================================
// 全局变量 - 管理所有的缓存组
// ============================================================
var (
	mu     sync.RWMutex              // 保护 groups map 的读写锁
	groups = make(map[string]*Group) // 所有缓存组的注册表

	// 用于延迟初始化 peer server 的机制
	initPeerServerOnce sync.Once
	initPeerServer     func()
)

// ------------------------------------------------------------
// GetGroup 根据名称获取已创建的缓存组
// 如果组不存在，返回 nil
// ------------------------------------------------------------
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// ------------------------------------------------------------
// NewGroup 创建一个新的协调式、组感知的 Getter
//
// 参数：
//
//	name: 组名，必须全局唯一
//	cacheBytes: 缓存大小限制（字节），包括 mainCache 和 hotCache
//	getter: 数据加载器，当缓存未命中时调用
//
// 返回的 Getter 会尝试（但不保证）确保对于给定的 key，
// 在整个节点集群中同时只运行一次 Get 调用。
// 无论是本地进程还是其他进程中的并发调用者，都会收到
// 原始 Get 完成后的结果副本。
// ------------------------------------------------------------
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	return newGroup(name, cacheBytes, getter, nil)
}

// ------------------------------------------------------------
// newGroup 是内部函数，支持传入自定义的 PeerPicker
// 如果 peers 为 nil，则在第一次需要时通过 sync.Once 初始化
// ------------------------------------------------------------
func newGroup(name string, cacheBytes int64, getter Getter, peers PeerPicker) *Group {
	if getter == nil {
		panic("nil Getter")
	}
	mu.Lock()
	defer mu.Unlock()

	// 确保 peer server 被初始化（只执行一次）
	initPeerServerOnce.Do(callInitPeerServer)

	// 检查组名是否已被注册
	if _, dup := groups[name]; dup {
		panic("duplicate registration of group " + name)
	}

	// 创建新组
	g := &Group{
		name:       name,
		getter:     getter,
		peers:      peers,
		cacheBytes: cacheBytes,
		loadGroup:  &singleflight.Group{}, // 用于请求去重
	}

	// 如果注册了创建钩子，调用它
	if fn := newGroupHook; fn != nil {
		fn(g)
	}

	// 注册组到全局 map
	groups[name] = g
	return g
}

// newGroupHook 如果非空，会在每个组创建后立即调用
// 用于测试或监控目的
var newGroupHook func(*Group)

// ------------------------------------------------------------
// RegisterNewGroupHook 注册组创建时的回调函数
// 该函数只能调用一次
// ------------------------------------------------------------
func RegisterNewGroupHook(fn func(*Group)) {
	if newGroupHook != nil {
		panic("RegisterNewGroupHook called more than once")
	}
	newGroupHook = fn
}

// ------------------------------------------------------------
// RegisterServerStart 注册在第一个组创建时执行的钩子
// 用于启动 HTTP 服务器等初始化操作
// ------------------------------------------------------------
func RegisterServerStart(fn func()) {
	if initPeerServer != nil {
		panic("RegisterServerStart called more than once")
	}
	initPeerServer = fn
}

// callInitPeerServer 调用 peer server 初始化函数（如果已注册）
func callInitPeerServer() {
	if initPeerServer != nil {
		initPeerServer()
	}
}

// ============================================================
// Group - 缓存组（核心数据结构）
// ============================================================
// Group 是一个缓存命名空间，以及分布在一组机器上的相关数据加载逻辑
//
// 架构设计：
// 1. mainCache: 存储本节点作为权威拥有者的 key
// 2. hotCache: 存储本节点不是拥有者但访问频繁的 key（热点数据）
// 3. loadGroup: 使用 singleflight 模式确保相同 key 只加载一次
// ============================================================
type Group struct {
	name       string     // 组名
	getter     Getter     // 数据加载器
	peersOnce  sync.Once  // 确保 peers 只初始化一次
	peers      PeerPicker // 节点选择器
	cacheBytes int64      // mainCache + hotCache 的总大小限制

	// mainCache 是本节点作为权威拥有者的 key 的缓存
	// 也就是说，这个缓存包含通过一致性哈希映射到本节点的 key
	mainCache cache

	// hotCache 包含本节点不是权威拥有者的 key，但因为
	// 足够热门而值得在本进程中镜像的 key/value
	//
	// hotCache 的作用：
	// - 避免网络热点：防止某个节点的网卡成为热门 key 的瓶颈
	// - 谨慎使用：保守地使用以最大化全局可存储的 key/value 对数量
	hotCache cache

	// loadGroup 确保每个 key 只被获取一次
	// （无论是本地还是远程），无论有多少并发调用者
	loadGroup flightGroup

	_ int32 // 强制 Stats 在 32 位平台上 8 字节对齐

	// Stats 是组的统计信息
	Stats Stats

	// rand 仅在测试时非空，用于获得可预测的结果
	rand *rand.Rand
}

// ============================================================
// flightGroup 接口 - 请求去重抽象
// ============================================================
// flightGroup 定义为接口，singleflight.Group 实现了这个接口
// 这样设计是为了在测试时可以使用替代实现
type flightGroup interface {
	// Do 执行并返回给定函数的结果
	// 确保对于给定的 key，同时只有一个执行在进行
	Do(key string, fn func() (interface{}, error)) (interface{}, error)
}

// ============================================================
// Stats - 每组统计信息
// ============================================================
// Stats 提供了详细的缓存性能指标
type Stats struct {
	Gets           AtomicInt // 任何 Get 请求，包括来自 peer 的
	CacheHits      AtomicInt // 缓存命中次数（mainCache 或 hotCache）
	PeerLoads      AtomicInt // 从 peer 加载的次数（远程加载或远程缓存命中）
	PeerErrors     AtomicInt // 从 peer 加载失败的次数
	Loads          AtomicInt // 加载次数 (gets - cacheHits)
	LoadsDeduped   AtomicInt // singleflight 去重后的加载次数
	LocalLoads     AtomicInt // 本地成功加载次数
	LocalLoadErrs  AtomicInt // 本地加载失败次数
	ServerRequests AtomicInt // 从 peer 通过网络接收的 get 请求数
}

// ------------------------------------------------------------
// Name 返回组的名称
// ------------------------------------------------------------
func (g *Group) Name() string {
	return g.name
}

// ------------------------------------------------------------
// initPeers 延迟初始化 peers（只执行一次）
// ------------------------------------------------------------
func (g *Group) initPeers() {
	if g.peers == nil {
		g.peers = getPeers(g.name)
	}
}

// ============================================================
// Get - 获取缓存数据的主要入口
// ============================================================
// Get 从缓存中获取 key 对应的值，并填充到 dest 中
//
// 工作流程：
// 1. 增加统计计数器
// 2. 检查本地缓存（mainCache 和 hotCache）
// 3. 如果缓存未命中，调用 load 方法
// 4. load 方法会决定是从 peer 获取还是本地加载
// ============================================================
func (g *Group) Get(ctx context.Context, key string, dest Sink) error {
	// 确保 peers 已初始化
	g.peersOnce.Do(g.initPeers)
	g.Stats.Gets.Add(1)

	// 检查 dest 是否为 nil
	if dest == nil {
		return errors.New("mycache: nil dest Sink")
	}

	// 先查询本地缓存
	value, cacheHit := g.lookupCache(key)

	if cacheHit {
		// 缓存命中，直接返回
		g.Stats.CacheHits.Add(1)
		return setSinkView(dest, value)
	}

	// 缓存未命中，需要加载数据
	//
	// 优化：避免双重反序列化或拷贝
	// 跟踪 dest 是否已被填充。只有一个调用者（如果是本地）
	// 会设置这个标志；失败的调用者不会设置。
	// 常见情况可能是只有一个调用者。
	destPopulated := false
	value, destPopulated, err := g.load(ctx, key, dest)
	if err != nil {
		return err
	}
	if destPopulated {
		// dest 已经被填充，无需再次设置
		return nil
	}
	return setSinkView(dest, value)
}

// ============================================================
// load - 加载数据的核心逻辑
// ============================================================
// load 通过本地调用 getter 或发送请求到其他机器来加载 key
//
// 返回值：
//
//	value: 加载的数据
//	destPopulated: dest 是否已被填充（优化标志）
//	err: 错误信息
//
// ============================================================
func (g *Group) load(ctx context.Context, key string, dest Sink) (value ByteView, destPopulated bool, err error) {
	g.Stats.Loads.Add(1)

	// 使用 singleflight 确保同一 key 只加载一次
	viewi, err := g.loadGroup.Do(key, func() (interface{}, error) {
		// 重要：再次检查缓存
		//
		// 原因：singleflight 只能去重并发重叠的调用。
		// 两个并发请求可能都未命中缓存，导致两次 load() 调用。
		// 不幸的 goroutine 调度可能导致这个回调被串行执行两次。
		// 如果我们不再次检查缓存，cache.nbytes 会被增加两次，
		// 但实际上只有一个条目。
		//
		// 考虑以下两个 goroutine 的序列化事件顺序：
		// 1: Get("key")
		// 2: Get("key")
		// 1: lookupCache("key")  -> 未命中
		// 2: lookupCache("key")  -> 未命中
		// 1: load("key")
		// 2: load("key")
		// 1: loadGroup.Do("key", fn)
		// 1: fn()
		// 2: loadGroup.Do("key", fn)
		// 2: fn()  <- 如果不再次检查缓存，这次调用会重复添加

		if value, cacheHit := g.lookupCache(key); cacheHit {
			g.Stats.CacheHits.Add(1)
			return value, nil
		}

		g.Stats.LoadsDeduped.Add(1)
		var value ByteView
		var err error

		// 判断是否应该从 peer 获取
		if peer, ok := g.peers.PickPeer(key); ok {
			// 从远程 peer 获取
			value, err = g.getFromPeer(ctx, peer, key)
			if err == nil {
				g.Stats.PeerLoads.Add(1)
				return value, nil
			}
			// 从 peer 获取失败，记录错误
			g.Stats.PeerErrors.Add(1)
			// TODO(bradfitz): 是否应该记录 peer 的错误？
			// 保留最近几次的错误日志供 /mycachez 查看？
			// 可能很无聊（正常的任务迁移），所以可能不值得记录。
		}

		// 从本地 getter 获取
		value, err = g.getLocally(ctx, key, dest)
		if err != nil {
			g.Stats.LocalLoadErrs.Add(1)
			return nil, err
		}
		g.Stats.LocalLoads.Add(1)
		destPopulated = true // 只有一个 load 调用者会得到这个返回值
		g.populateCache(key, value, &g.mainCache)
		return value, nil
	})

	if err == nil {
		value = viewi.(ByteView)
	}
	return
}

// ------------------------------------------------------------
// getLocally 从本地 getter 获取数据
// ------------------------------------------------------------
func (g *Group) getLocally(ctx context.Context, key string, dest Sink) (ByteView, error) {
	// 调用用户提供的 getter
	err := g.getter.Get(ctx, key, dest)
	if err != nil {
		return ByteView{}, err
	}
	// 从 dest 中提取 view
	return dest.view()
}

// ------------------------------------------------------------
// getFromPeer 从远程 peer 获取数据
// ------------------------------------------------------------
func (g *Group) getFromPeer(ctx context.Context, peer ProtoGetter, key string) (ByteView, error) {
	// 构造 protobuf 请求
	req := &pb.GetRequest{
		Group: g.name,
		Key:   key,
	}
	res := &pb.GetResponse{}

	// 通过网络获取数据
	err := peer.Get(ctx, req, res)
	if err != nil {
		return ByteView{}, err
	}

	value := ByteView{b: res.Value}

	// TODO(bradfitz): 使用 res.MinuteQps 或其他智能指标来
	// 有条件地填充 hotCache。现在只是按一定概率填充。

	// 随机决定是否将这个 key 添加到 hotCache
	// 10% 的概率（默认情况下）
	var pop bool
	if g.rand != nil {
		// 测试模式：使用提供的随机数生成器
		pop = g.rand.Intn(10) == 0
	} else {
		// 生产模式：使用全局随机数
		pop = rand.Intn(10) == 0
	}

	if pop {
		// 添加到热点缓存
		g.populateCache(key, value, &g.hotCache)
	}

	return value, nil
}

// ------------------------------------------------------------
// lookupCache 在 mainCache 和 hotCache 中查找 key
// ------------------------------------------------------------
func (g *Group) lookupCache(key string) (value ByteView, ok bool) {
	// 如果缓存大小为 0，禁用缓存
	if g.cacheBytes <= 0 {
		return
	}

	// 先查 mainCache
	value, ok = g.mainCache.get(key)
	if ok {
		return
	}

	// 再查 hotCache
	value, ok = g.hotCache.get(key)
	return
}

// ------------------------------------------------------------
// populateCache 填充缓存，并处理缓存驱逐
// ------------------------------------------------------------
func (g *Group) populateCache(key string, value ByteView, cache *cache) {
	// 如果缓存大小为 0，禁用缓存
	if g.cacheBytes <= 0 {
		return
	}

	// 添加到指定的缓存
	cache.add(key, value)

	// 如果缓存超出限制，驱逐旧条目
	// 循环直到总大小在限制内
	for {
		mainBytes := g.mainCache.bytes()
		hotBytes := g.hotCache.bytes()
		if mainBytes+hotBytes <= g.cacheBytes {
			// 大小在限制内，完成
			return
		}

		// TODO(bradfitz): 这是目前足够好的逻辑。
		// 应该基于测量和/或考虑不同资源的成本。

		// 选择驱逐目标：如果 hotCache 过大，驱逐 hotCache
		victim := &g.mainCache
		if hotBytes > mainBytes/8 {
			victim = &g.hotCache
		}
		victim.removeOldest()
	}
}

// ============================================================
// CacheType - 缓存类型枚举
// ============================================================
type CacheType int

const (
	// MainCache 是本节点作为拥有者的 key 的缓存
	MainCache CacheType = iota + 1

	// HotCache 是足够热门而复制到本节点的 key 的缓存
	// （即使本节点不是拥有者）
	HotCache
)

// ------------------------------------------------------------
// CacheStats 返回指定缓存的统计信息
// ------------------------------------------------------------
func (g *Group) CacheStats(which CacheType) CacheStats {
	switch which {
	case MainCache:
		return g.mainCache.stats()
	case HotCache:
		return g.hotCache.stats()
	default:
		return CacheStats{}
	}
}

// ============================================================
// cache - LRU 缓存包装器
// ============================================================
// cache 是对 *lru.Cache 的包装，添加了：
// 1. 同步机制（互斥锁）
// 2. 值类型固定为 ByteView
// 3. 统计所有 key 和 value 的大小
type cache struct {
	mu         sync.RWMutex // 保护下面的字段
	nbytes     int64        // 所有 key 和 value 的总字节数
	lru        *lru.Cache   // 底层 LRU 缓存
	nhit, nget int64        // 命中数和查询数
	nevict     int64        // 驱逐数
}

// ------------------------------------------------------------
// stats 返回缓存统计信息
// ------------------------------------------------------------
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

// ------------------------------------------------------------
// add 向缓存添加 key/value
// ------------------------------------------------------------
func (c *cache) add(key string, value ByteView) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 延迟初始化 LRU 缓存
	if c.lru == nil {
		c.lru = &lru.Cache{
			OnEvicted: func(key lru.Key, value interface{}) {
				// 驱逐回调：更新字节计数和驱逐统计
				val := value.(ByteView)
				c.nbytes -= int64(len(key.(string))) + int64(val.Len())
				c.nevict++
			},
		}
	}

	// 添加到 LRU
	c.lru.Add(key, value)
	// 更新字节计数
	c.nbytes += int64(len(key)) + int64(value.Len())
}

// ------------------------------------------------------------
// get 从缓存获取 key 的值
// ------------------------------------------------------------
func (c *cache) get(key string) (value ByteView, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 增加查询计数
	c.nget++

	if c.lru == nil {
		return
	}

	// 从 LRU 查询
	vi, ok := c.lru.Get(key)
	if !ok {
		return
	}

	// 命中，增加命中计数
	c.nhit++
	return vi.(ByteView), true
}

// ------------------------------------------------------------
// removeOldest 移除最旧的条目
// ------------------------------------------------------------
func (c *cache) removeOldest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru != nil {
		c.lru.RemoveOldest()
	}
}

// ------------------------------------------------------------
// bytes 返回缓存的字节大小
// ------------------------------------------------------------
func (c *cache) bytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nbytes
}

// ------------------------------------------------------------
// items 返回缓存的条目数
// ------------------------------------------------------------
func (c *cache) items() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.itemsLocked()
}

// itemsLocked 在已持有锁的情况下返回条目数
func (c *cache) itemsLocked() int64 {
	if c.lru == nil {
		return 0
	}
	return int64(c.lru.Len())
}

// ============================================================
// AtomicInt - 原子整数
// ============================================================
// AtomicInt 是一个可原子访问的 int64
// 用于并发安全的统计计数
type AtomicInt int64

// Add 原子地将 n 加到 i
func (i *AtomicInt) Add(n int64) {
	atomic.AddInt64((*int64)(i), n)
}

// Get 原子地获取 i 的值
func (i *AtomicInt) Get() int64 {
	return atomic.LoadInt64((*int64)(i))
}

// String 返回 i 的字符串表示
func (i *AtomicInt) String() string {
	return strconv.FormatInt(i.Get(), 10)
}

// ============================================================
// CacheStats - 缓存统计信息
// ============================================================
// CacheStats 由 Group 的 stats 访问器返回
type CacheStats struct {
	Bytes     int64 // 缓存的总字节数
	Items     int64 // 缓存的条目数
	Gets      int64 // 查询次数
	Hits      int64 // 命中次数
	Evictions int64 // 驱逐次数
}
