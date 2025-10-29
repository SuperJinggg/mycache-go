package mycache

import (
	"context"
	"sync"
	"time"
	
	"golang.org/x/sync/errgroup"
)

// ============================================================
// 批量操作配置
// ============================================================

var (
	defaultConcurrency = 100
	defaultTimeout     = 10 * time.Second
)

// ============================================================
// 批量操作实现
// ============================================================

// gets 批量获取实现
func (g *Group) gets(ctx context.Context, keys []string) (map[string][]byte, map[string]error) {
	values := make(map[string][]byte, len(keys))
	errors := make(map[string]error)
	
	if len(keys) == 0 {
		return values, errors
	}
	
	// 使用互斥锁保护结果map
	var mu sync.Mutex
	
	// 创建带限制的errgroup
	eg, ctx := errgroup.WithContext(ctx)
	
	// 使用信号量控制并发度
	sem := make(chan struct{}, defaultConcurrency)
	
	for _, key := range keys {
		key := key // 捕获循环变量
		
		eg.Go(func() error {
			// 获取信号量
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				errors[key] = ctx.Err()
				mu.Unlock()
				return nil // 不返回错误，继续处理其他
			}
			
			// 设置超时
			getCtx := ctx
			if defaultTimeout > 0 {
				var cancel context.CancelFunc
				getCtx, cancel = context.WithTimeout(ctx, defaultTimeout)
				defer cancel()
			}
			
			// 执行单个get操作
			value, err := g.get(getCtx, key)
			
			// 记录结果
			mu.Lock()
			if err != nil {
				errors[key] = err
			} else {
				values[key] = value
			}
			mu.Unlock()
			
			return nil
		})
	}
	
	// 等待所有操作完成
	eg.Wait()
	
	return values, errors
}

// sets 批量设置实现
func (g *Group) sets(ctx context.Context, items map[string][]byte) map[string]error {
	errors := make(map[string]error)
	
	if len(items) == 0 {
		return errors
	}
	
	var mu sync.Mutex
	eg, ctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, defaultConcurrency)
	
	for key, value := range items {
		key, value := key, value // 捕获循环变量
		
		eg.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				errors[key] = ctx.Err()
				mu.Unlock()
				return nil
			}
			
			// 直接设置到缓存
			g.populateCache(key, ByteView{b: cloneBytes(value)}, &g.mainCache)
			
			// 更新热点检测
			if g.hotDetector != nil {
				g.hotDetector.Add(key)
			}
			
			return nil
		})
	}
	
	eg.Wait()
	return errors
}

// ============================================================
// 辅助函数
// ============================================================

// cloneBytes 克隆字节切片
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
