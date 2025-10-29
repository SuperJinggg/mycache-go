# MyCache - 高性能分布式缓存库

[![Go Version](https://img.shields.io/badge/Go-1.25.3-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

MyCache 是一个高性能的分布式缓存库，采用先进的缓存算法和热点检测机制，提供简洁易用的批量操作 API。

## ✨ 核心特性

### 🚀 性能优化
- **LRU-K 缓存算法** (K=2): 相比传统 LRU，能更准确识别真正的热点数据，有效减少缓存污染
- **HeavyKeeper 热点检测**: 实时识别热点 Key，自动维护热点缓存层
- **SingleFlight 防击穿**: 同一 Key 并发请求合并，防止缓存击穿

### 🛠 易用性
- **简洁的批量 API**: Gets、Sets、Deletes 等批量操作接口
- **自动并发控制**: 内置并发度限制，默认 100 并发
- **Context 支持**: 完整的超时控制和取消机制

### 📊 可观测性
- **实时统计**: 命中率、请求数、驱逐次数等关键指标
- **热点追踪**: 实时获取 Top-K 热点 Key 列表

### 🌐 分布式
- **节点间通信**: 基于 Protobuf 的高效序列化
- **一致性哈希**: 支持节点动态扩缩容（需配合 HTTP 服务器实现）
- **多种数据接收器**: 支持 String、ByteView、Proto 等多种数据格式

## 🏗 架构设计

```
┌─────────────────────────────────────────────────────┐
│                    应用层 API                        │
│         Gets() | Sets() | Deletes() | Stats()       │
└──────────────────┬──────────────────────────────────┘
                   │
┌──────────────────▼──────────────────────────────────┐
│                  Group 缓存组                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────────┐     │
│  │ 热点缓存  │  │  主缓存   │  │ HeavyKeeper  │     │
│  │ (LRU-K)  │  │  (LRU-K)  │  │  热点检测     │     │
│  └──────────┘  └──────────┘  └──────────────┘     │
└──────────────────┬──────────────────────────────────┘
                   │
┌──────────────────▼──────────────────────────────────┐
│              SingleFlight 防击穿层                   │
└──────────────────┬──────────────────────────────────┘
                   │
        ┌──────────┴──────────┐
        ▼                     ▼
┌───────────────┐      ┌──────────────┐
│  Peer 远程获取 │      │  Getter 本地  │
│   (Protobuf)  │      │    数据源     │
└───────────────┘      └──────────────┘
```

## 📦 安装

```bash
go get github.com/yourusername/mycache
```

## 🔧 快速开始

### 基础使用

```go
package main

import (
    "context"
    "fmt"
    "log"
    "mycache"
)

func main() {
    // 创建缓存组
    cache := mycache.NewGroup("example", 
        10<<20, // 10MB 缓存限制
        mycache.GetterFunc(func(ctx context.Context, key string, dest mycache.Sink) error {
            // 模拟从数据库获取数据
            log.Printf("[DB] 查询: %s", key)
            data := getFromDatabase(key)
            return dest.SetBytes(data)
        }),
    )

    // 批量获取
    keys := []string{"user:1", "user:2", "product:1"}
    values, errors := cache.Gets(keys)
    
    for key, value := range values {
        fmt.Printf("%s: %s\n", key, value)
    }
}
```

### 带热点缓存的高级配置

```go
// 创建带热点缓存的组（20% 容量用于热点）
cache := mycache.NewGroupWithHotCache("advanced", 
    100<<20,  // 100MB
    getter,
    0.2,      // 20% 热点缓存比例
)

// 查看热点 Key
hotKeys := cache.HotKeys()
fmt.Println("热点 Keys:", hotKeys)

// 查看统计信息
stats := cache.Stats()
fmt.Printf("缓存命中率: %.2f%%\n", stats.HitRate)
```

## 📖 API 文档

### 核心接口

#### `Group` - 缓存组

```go
// 批量获取
func (g *Group) Gets(keys []string) (map[string][]byte, map[string]error)

// 带 Context 的批量获取
func (g *Group) GetsWithContext(ctx context.Context, keys []string) (map[string][]byte, map[string]error)

// 批量设置
func (g *Group) Sets(items map[string][]byte) map[string]error

// 批量删除  
func (g *Group) Deletes(keys []string) map[string]error

// 获取热点 Key 列表
func (g *Group) HotKeys() []string

// 获取统计信息
func (g *Group) Stats() *CacheStats
```

#### `CacheStats` - 统计信息

```go
type CacheStats struct {
    Bytes     int64   // 缓存占用字节数
    Items     int64   // 缓存条目数
    Gets      int64   // 总请求数
    Hits      int64   // 命中次数
    HitRate   float64 // 命中率百分比
    Evictions int64   // 驱逐次数
}
```

### 数据接收器 (Sink)

```go
// 字符串接收器
mycache.StringSink(&result)

// 字节切片接收器
mycache.AllocatingByteSliceSink(&bytes)

// Protobuf 接收器
mycache.ProtoSink(&pbMessage)

// ByteView 接收器
mycache.ByteViewSink(&view)
```

## 🎯 核心算法

### LRU-K 算法 (K=2)

传统 LRU 的问题在于偶发性的数据访问可能将热点数据挤出缓存。LRU-K 通过记录最近 K 次访问时间来解决这个问题：

- **历史队列**: 记录访问次数 < K 的数据
- **缓存队列**: 存储访问次数 ≥ K 的数据
- 只有访问 K 次以上的数据才被认为是热点，进入主缓存队列

### HeavyKeeper 热点检测

HeavyKeeper 是一种高效的 Top-K 热点检测算法：

- **Count-Min Sketch 结构**: 使用多个哈希函数和计数器矩阵
- **指纹机制**: 通过指纹减少哈希冲突
- **概率衰减**: 对低频 Key 进行概率性计数衰减
- **时间衰减**: 定期衰减所有计数，识别实时热点

配置参数：
- `width`: 桶宽度（默认 1000）
- `depth`: 哈希函数数量（默认 4）
- `topK`: 维护的热点数量（默认 100）
- `decayFactor`: 衰减因子（默认 0.95）

### SingleFlight 防击穿

对于同一个 Key 的并发请求，只允许第一个请求去加载数据，其他请求等待结果：

```go
type singleflightGroup struct {
    mu sync.Mutex
    m  map[string]*call  // 正在进行的调用
}
```

## ⚡ 性能优化

### 批量操作并发控制

```go
// 默认并发度为 100
var defaultConcurrency = 100

// 使用信号量控制并发
sem := make(chan struct{}, defaultConcurrency)
```

### 内存管理

- **ByteView 不可变设计**: 避免数据竞争，减少拷贝
- **缓存大小限制**: 自动驱逐最旧数据
- **分层缓存**: 热点缓存 + 主缓存，提高命中率

## 🔐 线程安全

所有公开接口都是线程安全的：

- `cache` 结构使用 `sync.RWMutex` 保护
- `HeavyKeeper` 使用读写锁控制并发
- `singleflightGroup` 使用互斥锁防止重复执行

## 📈 监控指标

通过 `Stats()` 方法获取的关键指标：

| 指标 | 说明 | 用途 |
|------|------|------|
| HitRate | 缓存命中率 | 评估缓存效果 |
| Gets/Hits | 请求数/命中数 | 了解访问模式 |
| Evictions | 驱逐次数 | 判断缓存容量是否合适 |
| Bytes/Items | 内存使用 | 容量规划 |

## 🚀 最佳实践

1. **合理设置缓存大小**: 根据内存和数据特征调整 `cacheBytes`
2. **使用批量操作**: 减少网络往返，提高吞吐量
3. **配置热点缓存**: 对于有明显热点的场景，启用热点缓存层
4. **监控关键指标**: 定期查看 `Stats()`，及时调整配置
5. **设置合理超时**: 使用 `Context` 控制操作超时

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

## 📄 许可

MIT License

## 🙏 致谢

本项目受 [groupcache](https://github.com/golang/groupcache) 启发，在其基础上进行了以下改进：

- 采用 LRU-K 算法替代传统 LRU
- 添加 HeavyKeeper 热点检测
- 简化批量操作 API
- 增强统计和监控能力