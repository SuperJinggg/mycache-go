package mycache

import (
	"container/heap"
	"hash/fnv"
	"math"
	"math/rand"
	"sync"
	"time"
)

// ============================================================
// HeavyKeeper - 热点检测算法
// ============================================================

// HeavyKeeper 实现热点检测
type HeavyKeeper struct {
	mu sync.RWMutex
	
	// 核心参数
	width       int     // 桶的宽度
	depth       int     // 哈希函数的数量
	decayFactor float64 // 衰减因子
	minCount    int     // 成为热点的最小访问次数
	topK        int     // 维护的热点数量
	
	// Count-Min Sketch结构
	counters    [][]float64
	fingerprint [][]uint32
	
	// 热点追踪
	hotKeys   *MinHeap
	hotKeyMap map[string]*HeapItem
	
	// 哈希种子
	seeds []uint32
	
	// 时间衰减
	lastDecay     time.Time
	decayInterval time.Duration
	
	stopCh chan struct{}
}

// HeapItem 堆元素
type HeapItem struct {
	key   string
	count float64
	index int
}

// MinHeap 最小堆
type MinHeap []*HeapItem

func (h MinHeap) Len() int           { return len(h) }
func (h MinHeap) Less(i, j int) bool { return h[i].count < h[j].count }
func (h MinHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *MinHeap) Push(x interface{}) {
	n := len(*h)
	item := x.(*HeapItem)
	item.index = n
	*h = append(*h, item)
}

func (h *MinHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	item.index = -1
	*h = old[0 : n-1]
	return item
}

// NewHeavyKeeper 创建热点检测器
func NewHeavyKeeper(width, depth, topK int, decay float64) *HeavyKeeper {
	if width < 100 {
		width = 1000
	}
	if depth < 3 {
		depth = 4
	}
	if decay <= 0 || decay >= 1 {
		decay = 0.95
	}
	if topK < 10 {
		topK = 100
	}
	
	hk := &HeavyKeeper{
		width:         width,
		depth:         depth,
		decayFactor:   decay,
		topK:          topK,
		minCount:      10,
		counters:      make([][]float64, depth),
		fingerprint:   make([][]uint32, depth),
		hotKeyMap:     make(map[string]*HeapItem),
		seeds:         make([]uint32, depth),
		lastDecay:     time.Now(),
		decayInterval: 1 * time.Minute,
		stopCh:        make(chan struct{}),
	}
	
	// 初始化计数器矩阵
	for i := 0; i < depth; i++ {
		hk.counters[i] = make([]float64, width)
		hk.fingerprint[i] = make([]uint32, width)
		hk.seeds[i] = rand.Uint32()
	}
	
	// 初始化小顶堆
	h := make(MinHeap, 0, topK)
	heap.Init(&h)
	hk.hotKeys = &h
	
	// 启动定期衰减
	go hk.decayRoutine()
	
	return hk
}

// Add 记录访问
func (hk *HeavyKeeper) Add(key string) {
	hk.mu.Lock()
	defer hk.mu.Unlock()
	
	fp := hk.getFingerprint(key)
	minCount := math.MaxFloat64
	
	// 更新所有行的计数器
	for i := 0; i < hk.depth; i++ {
		pos := hk.hash(key, i)
		
		// 如果指纹匹配或位置为空，增加计数
		if hk.fingerprint[i][pos] == fp || hk.fingerprint[i][pos] == 0 {
			hk.fingerprint[i][pos] = fp
			hk.counters[i][pos]++
		} else {
			// 指纹不匹配，概率性减少计数（HeavyKeeper核心）
			prob := 1.0 / (hk.counters[i][pos] + 1)
			if rand.Float64() < prob {
				hk.counters[i][pos]--
				if hk.counters[i][pos] <= 0 {
					hk.fingerprint[i][pos] = fp
					hk.counters[i][pos] = 1
				}
			}
		}
		
		// 记录最小计数
		if hk.fingerprint[i][pos] == fp && hk.counters[i][pos] < minCount {
			minCount = hk.counters[i][pos]
		}
	}
	
	// 更新热点列表
	hk.updateHotKeys(key, minCount)
}

// Get 获取key的估计频率
func (hk *HeavyKeeper) Get(key string) float64 {
	hk.mu.RLock()
	defer hk.mu.RUnlock()
	
	fp := hk.getFingerprint(key)
	minCount := math.MaxFloat64
	
	for i := 0; i < hk.depth; i++ {
		pos := hk.hash(key, i)
		if hk.fingerprint[i][pos] == fp {
			if hk.counters[i][pos] < minCount {
				minCount = hk.counters[i][pos]
			}
		}
	}
	
	if minCount == math.MaxFloat64 {
		return 0
	}
	return minCount
}

// IsHot 判断是否为热点
func (hk *HeavyKeeper) IsHot(key string) bool {
	hk.mu.RLock()
	defer hk.mu.RUnlock()
	
	_, exists := hk.hotKeyMap[key]
	return exists
}

// TopK 获取TopK热点
func (hk *HeavyKeeper) TopK() []string {
	hk.mu.RLock()
	defer hk.mu.RUnlock()
	
	result := make([]string, 0, hk.hotKeys.Len())
	for _, item := range *hk.hotKeys {
		result = append(result, item.key)
	}
	
	// 按访问频率排序
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if hk.hotKeyMap[result[i]].count < hk.hotKeyMap[result[j]].count {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	
	return result
}

// Stop 停止衰减协程
func (hk *HeavyKeeper) Stop() {
	close(hk.stopCh)
}

// ============================================================
// 内部方法
// ============================================================

// hash 计算哈希值
func (hk *HeavyKeeper) hash(key string, i int) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	h.Write([]byte{byte(hk.seeds[i]), byte(hk.seeds[i] >> 8), 
		byte(hk.seeds[i] >> 16), byte(hk.seeds[i] >> 24)})
	return int(h.Sum32() % uint32(hk.width))
}

// getFingerprint 计算指纹
func (hk *HeavyKeeper) getFingerprint(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32()
}

// updateHotKeys 更新热点列表
func (hk *HeavyKeeper) updateHotKeys(key string, count float64) {
	// 忽略低频访问
	if count < float64(hk.minCount) {
		return
	}
	
	if item, exists := hk.hotKeyMap[key]; exists {
		// 已在热点列表中，更新计数
		item.count = count
		heap.Fix(hk.hotKeys, item.index)
	} else if hk.hotKeys.Len() < hk.topK {
		// 热点列表未满，直接添加
		item := &HeapItem{
			key:   key,
			count: count,
		}
		heap.Push(hk.hotKeys, item)
		hk.hotKeyMap[key] = item
	} else if count > (*hk.hotKeys)[0].count {
		// 新key的频率高于堆顶（最小值），替换
		oldItem := heap.Pop(hk.hotKeys).(*HeapItem)
		delete(hk.hotKeyMap, oldItem.key)
		
		item := &HeapItem{
			key:   key,
			count: count,
		}
		heap.Push(hk.hotKeys, item)
		hk.hotKeyMap[key] = item
	}
}

// decay 执行衰减
func (hk *HeavyKeeper) decay() {
	hk.mu.Lock()
	defer hk.mu.Unlock()
	
	// 对所有计数器进行衰减
	for i := 0; i < hk.depth; i++ {
		for j := 0; j < hk.width; j++ {
			hk.counters[i][j] *= hk.decayFactor
			if hk.counters[i][j] < 1 {
				hk.counters[i][j] = 0
				hk.fingerprint[i][j] = 0
			}
		}
	}
	
	// 更新热点列表中的计数
	newHeap := make(MinHeap, 0, hk.topK)
	newMap := make(map[string]*HeapItem)
	
	for _, item := range *hk.hotKeys {
		newCount := hk.getCount(item.key)
		if newCount >= float64(hk.minCount) {
			newItem := &HeapItem{
				key:   item.key,
				count: newCount,
			}
			newHeap = append(newHeap, newItem)
			newMap[item.key] = newItem
		}
	}
	
	heap.Init(&newHeap)
	hk.hotKeys = &newHeap
	hk.hotKeyMap = newMap
	
	hk.lastDecay = time.Now()
}

// getCount 内部获取计数（不加锁）
func (hk *HeavyKeeper) getCount(key string) float64 {
	fp := hk.getFingerprint(key)
	minCount := math.MaxFloat64
	
	for i := 0; i < hk.depth; i++ {
		pos := hk.hash(key, i)
		if hk.fingerprint[i][pos] == fp {
			if hk.counters[i][pos] < minCount {
				minCount = hk.counters[i][pos]
			}
		}
	}
	
	if minCount == math.MaxFloat64 {
		return 0
	}
	return minCount
}

// decayRoutine 定期衰减协程
func (hk *HeavyKeeper) decayRoutine() {
	ticker := time.NewTicker(hk.decayInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			hk.decay()
		case <-hk.stopCh:
			return
		}
	}
}
