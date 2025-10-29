package mycache

import "sync"

// ============================================================
// singleflight - 防止缓存击穿机制
// ============================================================

// singleflightGroup 确保对于相同的key，同时只有一个函数在执行
type singleflightGroup struct {
	mu sync.Mutex
	m  map[string]*call
}

// call 表示一个正在进行或已完成的函数调用
type call struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

// Do 执行并返回给定函数的结果，确保对于给定的key同时只有一个执行
func (g *singleflightGroup) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	
	// 如果已有相同key的调用在进行，等待其完成
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	
	// 创建新的调用
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()
	
	// 执行函数
	c.val, c.err = fn()
	c.wg.Done()
	
	// 清理
	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()
	
	return c.val, c.err
}
