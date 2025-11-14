# MyCache-Go

[![Go Version](https://img.shields.io/badge/Go-1.25-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

MyCache-Go 是一个高性能的分布式缓存库，基于 Go 语言实现，提供了类似 GroupCache 的功能，支持分布式节点间的缓存共享和一致性哈希负载均衡。

## ✨ 主要特性

- **多种缓存算法**：支持 LRU（Least Recently Used）和 LRU2 双层缓存算法
- **分布式支持**：通过 gRPC 实现节点间通信，支持多节点部署
- **一致性哈希**：使用一致性哈希算法进行负载均衡，支持动态节点扩缩容
- **服务发现**：集成 etcd 进行服务注册与发现
- **防缓存击穿**：使用 Singleflight 机制防止缓存雪崩
- **内存管理**：精确的内存使用控制，支持设置最大内存限制
- **过期策略**：支持键值对过期时间设置和自动清理
- **统计监控**：提供详细的缓存命中率、加载次数等统计信息
- **灵活配置**：支持自定义驱逐回调、清理间隔等配置

## 📁 项目结构

```
mycache-go/
├── cache.go                 # 核心缓存实现
├── group.go                 # 缓存组管理
├── server.go               # gRPC 服务器
├── client.go               # 分布式客户端
├── peers.go                # 节点管理器
├── byteview.go             # 不可变字节视图
├── utils.go                # 工具函数
├── consistenthash/         # 一致性哈希实现
│   ├── con_hash.go         # 哈希环实现
│   └── config.go           # 配置定义
├── store/                  # 存储引擎
│   ├── store.go            # 存储接口定义
│   ├── lru.go              # LRU 算法实现
│   ├── lru2.go             # LRU2 算法实现
│   └── lru2_test.go        # 单元测试
├── singleflight/           # 防缓存击穿
│   └── singleflight.go     # Singleflight 实现
├── registry/               # 服务注册发现
│   └── register.go         # etcd 注册实现
├── pb/                     # Protocol Buffers
│   ├── mycache.proto       # gRPC 接口定义
│   ├── mycache.pb.go       # 生成的 protobuf 代码
│   └── mycache_grpc.pb.go  # 生成的 gRPC 代码
└── example/                # 使用示例
    └── test.go             # 示例代码
```

## 🚀 快速开始

### 安装

```bash
go get github.com/SuperJinggg/mycache-go
```

### 基础使用

#### 1. 单机模式

```go
package main

import (
    "context"
    "fmt"
    "log"
    
    cache "github.com/SuperJinggg/mycache-go"
)

func main() {
    ctx := context.Background()
    
    // 创建缓存组，设置 2MB 的内存限制
    group := cache.NewGroup("my-cache", 2<<20, cache.GetterFunc(
        func(ctx context.Context, key string) ([]byte, error) {
            // 当缓存未命中时，从数据源加载数据
            log.Printf("从数据源加载: %s", key)
            return []byte(fmt.Sprintf("value-%s", key)), nil
        }),
    )
    
    // 获取值（首次会触发数据源加载）
    val, err := group.Get(ctx, "key1")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("获取到值: %s\n", val.String())
    
    // 再次获取（从缓存中读取）
    val, err = group.Get(ctx, "key1")
    fmt.Printf("从缓存获取: %s\n", val.String())
}
```

#### 2. 分布式模式

启动多个节点来构建分布式缓存集群：

**节点 A (端口 8001)**
```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"
    
    cache "github.com/SuperJinggg/mycache-go"
)

func main() {
    // 创建服务器节点
    server, err := cache.NewServer(":8001", "mycache-cluster",
        cache.WithEtcdEndpoints([]string{"localhost:2379"}),
        cache.WithDialTimeout(5*time.Second),
    )
    if err != nil {
        log.Fatal(err)
    }
    
    // 创建客户端选择器
    picker, err := cache.NewClientPicker(":8001")
    if err != nil {
        log.Fatal(err)
    }
    
    // 创建缓存组
    group := cache.NewGroup("distributed-cache", 2<<20, cache.GetterFunc(
        func(ctx context.Context, key string) ([]byte, error) {
            return []byte(fmt.Sprintf("节点A的值-%s", key)), nil
        }),
    )
    
    // 注册节点选择器
    group.RegisterPeers(picker)
    
    // 启动服务
    log.Fatal(server.Start())
}
```

**节点 B (端口 8002)**
```go
// 类似配置，修改端口为 :8002
```

### 高级配置

#### 自定义缓存选项

```go
// 创建自定义缓存配置
cacheOpts := cache.CacheOptions{
    CacheType:    store.LRU2,           // 使用 LRU2 算法
    MaxBytes:     8 * 1024 * 1024,      // 8MB 内存限制
    BucketCount:  16,                    // 缓存桶数量
    CapPerBucket: 512,                   // 每个桶的容量
    Level2Cap:    256,                   // 二级缓存容量
    CleanupTime:  time.Minute,          // 清理间隔
    OnEvicted: func(key string, value store.Value) {
        log.Printf("键 %s 被驱逐", key)
    },
}

// 使用自定义配置创建缓存组
group := cache.NewGroup("custom-cache", 0, getter,
    cache.WithCacheOptions(cacheOpts),
    cache.WithExpiration(10*time.Minute),  // 设置过期时间
)
```

#### 设置和删除操作

```go
ctx := context.Background()

// 设置键值对
err := group.Set(ctx, "user:123", []byte(`{"name":"Alice","age":25}`))
if err != nil {
    log.Printf("设置失败: %v", err)
}

// 删除键
err = group.Delete(ctx, "user:123")
if err != nil {
    log.Printf("删除失败: %v", err)
}
```

## 🏗 架构设计

### 核心组件

1. **Cache**: 底层缓存存储的封装，管理内存使用和驱逐策略
2. **Group**: 缓存命名空间，提供高层 API 和分布式协调
3. **Store**: 存储引擎接口，支持 LRU、LRU2 等多种算法
4. **PeerPicker**: 节点选择器，使用一致性哈希选择合适的节点
5. **Server/Client**: gRPC 服务端和客户端，处理节点间通信
6. **Registry**: 服务注册与发现，基于 etcd 实现

### 数据流程

```
用户请求 -> Group.Get()
    ├── 检查本地缓存 (mainCache)
    │   ├── 命中 -> 返回结果
    │   └── 未命中 ↓
    ├── 选择节点 (PeerPicker)
    │   ├── 本节点 -> 从数据源加载 (Getter)
    │   └── 远程节点 -> gRPC 请求
    └── 写入缓存 -> 返回结果
```

### 一致性哈希

系统使用一致性哈希算法来分配缓存键到不同节点，特点包括：

- **虚拟节点**: 每个物理节点映射多个虚拟节点，提高负载均衡
- **动态调整**: 支持根据负载动态调整虚拟节点数量
- **最小影响**: 节点加入/离开时，只影响相邻节点的缓存

## 📊 性能优化

- **Singleflight**: 防止缓存击穿，相同的键只会有一个加载请求
- **内存池**: 使用 ByteView 减少内存分配和拷贝
- **并发控制**: 使用读写锁和原子操作优化并发性能
- **批量操作**: 支持批量获取和设置，减少网络往返

## 🔧 配置参数

### CacheOptions

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| CacheType | CacheType | LRU2 | 缓存算法类型 |
| MaxBytes | int64 | 8MB | 最大内存使用量 |
| BucketCount | uint16 | 16 | LRU2 缓存桶数量 |
| CapPerBucket | uint16 | 512 | 每个桶的容量 |
| Level2Cap | uint16 | 256 | 二级缓存容量 |
| CleanupTime | Duration | 1min | 过期清理间隔 |
| OnEvicted | func | nil | 驱逐回调函数 |

### ServerOptions

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| EtcdEndpoints | []string | ["localhost:2379"] | etcd 端点列表 |
| DialTimeout | Duration | 5s | 连接超时时间 |
| MaxMsgSize | int | 4MB | 最大消息大小 |
| TLS | bool | false | 是否启用 TLS |

## 📈 监控指标

系统提供丰富的统计信息：

```go
stats := group.Stats()
fmt.Printf("本地命中率: %.2f%%\n", stats.LocalHitRate()*100)
fmt.Printf("总请求数: %d\n", stats.TotalLoads())
fmt.Printf("平均加载时间: %v\n", stats.AverageLoadTime())
```

## 🧪 测试

运行单元测试：

```bash
go test ./...
```

运行基准测试：

```bash
go test -bench=. ./store
```

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

## 📝 许可证

本项目采用 MIT 许可证，详见 [LICENSE](LICENSE) 文件。

## 🙏 致谢

- 灵感来自 [groupcache](https://github.com/golang/groupcache)
- 使用了 [etcd](https://github.com/etcd-io/etcd) 进行服务发现
- 基于 [gRPC](https://grpc.io/) 实现节点通信

## 📧 联系方式

- 作者：SuperJinggg
- GitHub：[https://github.com/SuperJinggg/mycache-go](https://github.com/SuperJinggg/mycache-go)

---

如有任何问题或建议，欢迎提交 Issue 或联系作者！
