package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"mycache"
)

// 模拟数据库
var db = map[string]string{
	"user:1":    `{"id":1,"name":"Tom","age":18}`,
	"user:2":    `{"id":2,"name":"Jack","age":19}`,
	"user:3":    `{"id":3,"name":"Sam","age":20}`,
	"product:1": `{"id":1,"name":"iPhone","price":999}`,
	"product:2": `{"id":2,"name":"iPad","price":599}`,
	"product:3": `{"id":3,"name":"MacBook","price":1999}`,
}

func main() {
	// 创建缓存组
	cache := mycache.NewGroup("example", 10<<20, // 10MB缓存
		mycache.GetterFunc(func(ctx context.Context, key string, dest mycache.Sink) error {
			log.Printf("[DB] 查询: %s", key)
			time.Sleep(100 * time.Millisecond) // 模拟慢查询

			if v, ok := db[key]; ok {
				dest.SetBytes([]byte(v))
				return nil
			}
			return fmt.Errorf("key %s not found", key)
		}),
	)

	fmt.Println("=== MyCache 改进版示例 ===")
	fmt.Println("API简化：Gets, Sets, Deletes, Stats, HotKeys")
	fmt.Println()

	// 1. 批量获取
	fmt.Println("1. 批量获取（Gets）")

	keys := []string{"user:1", "user:2", "product:1"}
	start := time.Now()
	values, errors := cache.Gets(keys)
	duration := time.Since(start)

	fmt.Printf("获取 %d 个key，耗时: %v\n", len(keys), duration)
	for key, value := range values {
		fmt.Printf("  ✓ %s: %d bytes\n", key, len(value))
	}
	for key, err := range errors {
		fmt.Printf("  ✗ %s: %v\n", key, err)
	}
	fmt.Println()

	// 2. 再次获取（测试缓存）
	fmt.Println("2. 再次获取（从缓存）")

	start = time.Now()
	values, _ = cache.Gets(keys)
	duration = time.Since(start)

	fmt.Printf("从缓存获取 %d 个key，耗时: %v（更快！）\n", len(keys), duration)
	fmt.Println()

	// 3. 批量设置
	fmt.Println("3. 批量设置（Sets）")

	newItems := map[string][]byte{
		"custom:1": []byte(`{"custom":"value1"}`),
		"custom:2": []byte(`{"custom":"value2"}`),
	}

	errs := cache.Sets(newItems)
	if len(errs) == 0 {
		fmt.Println("✓ 批量设置成功")
	}

	// 验证设置
	values, _ = cache.Gets([]string{"custom:1", "custom:2"})
	for key := range values {
		fmt.Printf("  确认已设置: %s\n", key)
	}
	fmt.Println()

	// 4. 模拟热点访问
	fmt.Println("4. 模拟热点访问")

	hotKey := "user:1"
	for i := 0; i < 20; i++ {
		cache.Gets([]string{hotKey})
	}

	// 偶尔访问其他key
	cache.Gets([]string{"user:2", "user:3"})

	fmt.Printf("频繁访问 %s 20次\n", hotKey)
	fmt.Println()

	// 5. 查看热点
	fmt.Println("5. 热点Key（HotKeys）")

	hotKeys := cache.HotKeys()
	if len(hotKeys) > 0 {
		fmt.Println("检测到的热点key:")
		for i, key := range hotKeys {
			fmt.Printf("  %d. %s\n", i+1, key)
		}
	} else {
		fmt.Println("暂无热点key（需要更多访问）")
	}
	fmt.Println()

	// 6. 查看统计
	fmt.Println("6. 缓存统计（Stats）")

	stats := cache.Stats()
	fmt.Printf("缓存大小: %d bytes\n", stats.Bytes)
	fmt.Printf("缓存条目: %d\n", stats.Items)
	fmt.Printf("总请求数: %d\n", stats.Gets)
	fmt.Printf("缓存命中: %d\n", stats.Hits)
	fmt.Printf("命中率: %.1f%%\n", stats.HitRate)
	fmt.Println()

	// 7. 批量删除
	fmt.Println("7. 批量删除（Deletes）")

	deleteKeys := []string{"custom:1", "custom:2"}
	cache.Deletes(deleteKeys)
	fmt.Printf("删除keys: %v\n", deleteKeys)

	// 验证删除
	values, _ = cache.Gets(deleteKeys)
	if len(values) == 0 {
		fmt.Println("✓ 确认已删除")
	}
	fmt.Println()

	// 8. 带超时的操作
	fmt.Println("8. 带超时的操作（GetsWithContext）")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// 这会因为超时而失败（DB查询需要100ms）
	values, errors = cache.GetsWithContext(ctx, []string{"newkey"})

	if len(errors) > 0 {
		fmt.Println("✓ 超时控制生效")
		for key, err := range errors {
			fmt.Printf("  %s: %v\n", key, err)
		}
	}
	fmt.Println()

	// 总结
	fmt.Println("=== 总结 ===")
	fmt.Println("简化后的API:")
	fmt.Println("  • Gets()    - 批量获取")
	fmt.Println("  • Sets()    - 批量设置")
	fmt.Println("  • Deletes() - 批量删除")
	fmt.Println("  • Stats()   - 获取统计")
	fmt.Println("  • HotKeys() - 热点列表")
	fmt.Println()
	fmt.Println("内部自动：")
	fmt.Println("  • 并发执行")
	fmt.Println("  • SingleFlight防击穿")
	fmt.Println("  • LRU-K缓存")
	fmt.Println("  • HeavyKeeper热点检测")
}

// 辅助函数
func repeat(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

func init() {
	// 减少日志输出
	log.SetFlags(log.Ltime)
}
