# MyCache - 分布式缓存库
MyCache 是一个学习项目，最初是根据 [geecache](https://github.com/geektutu/7days-golang) 入门学习的。
MyCache 是基于 [groupcache](https://github.com/golang/groupcache) 的分布式缓存库，在保留原有优秀设计的基础上，升级了 Protocol Buffers 版本并添加了详细的中文注释。

## 项目来源

本项目 fork 自 Google 的 groupcache 项目，groupcache 是 memcached 作者 Brad Fitzpatrick 在 Google 工作时开发的缓存库。相比 memcached，groupcache 有以下特点：
- 没有缓存过期机制（适合不变数据）
- 没有显式的缓存删除接口
- 自动处理热点数据
- 自动去重并发请求

## 主要改动

### 1. Protocol Buffers 升级
- 从旧版 `github.com/golang/protobuf` 升级到新版 `google.golang.org/protobuf`
- 重新生成了 protobuf 代码，兼容最新的 protoc-gen-go

### 2. 详细中文注释
为所有核心组件添加了详细的中文注释，包括：
- 算法原理说明
- 使用场景描述
- 性能分析
- 可视化示例

## 核心特性

- **多级缓存**：mainCache（主缓存）+ hotCache（热点缓存）
- **一致性哈希**：使用虚拟节点实现负载均衡
- **请求去重**：singleflight 机制避免缓存击穿
- **分布式架构**：自动选择数据的拥有者节点
- **热点优化**：自动复制热门数据到多个节点

## 快速开始

### 安装

```bash
go get mycache
```

### 基本使用

```go
package main

import (
    "context"
    "mycache"
)

func main() {
    // 创建缓存组
    group := mycache.NewGroup("colors", 64<<20, mycache.GetterFunc(
        func(ctx context.Context, key string, dest mycache.Sink) error {
            // 从数据源加载数据
            value := getColorFromDB(key)
            dest.SetBytes([]byte(value))
            return nil
        },
    ))
    
    // 获取缓存
    var data []byte
    err := group.Get(context.Background(), "red", 
                     mycache.AllocatingByteSliceSink(&data))
}
```

## 运行示例

### 单机模式

```go
// main.go
package main

import (
    "context"
    "errors"
    "log"
    "net/http"
    "mycache"
)

var Store = map[string][]byte{
    "red":   []byte("#FF0000"),
    "green": []byte("#00FF00"),
    "blue":  []byte("#0000FF"),
}

var Group = mycache.NewGroup("colors", 64<<20, mycache.GetterFunc(
    func(ctx context.Context, key string, dest mycache.Sink) error {
        log.Println("looking up", key)
        v, ok := Store[key]
        if !ok {
            return errors.New("color not found")
        }
        dest.SetBytes(v)
        return nil
    },
))

func main() {
    http.HandleFunc("/color", func(w http.ResponseWriter, r *http.Request) {
        color := r.FormValue("name")
        var b []byte
        err := Group.Get(r.Context(), color, mycache.AllocatingByteSliceSink(&b))
        if err != nil {
            http.Error(w, err.Error(), http.StatusNotFound)
            return
        }
        w.Write(b)
    })
    
    log.Println("Server starting on :8080")
    http.ListenAndServe(":8080", nil)
}
```

### 分布式模式

启动三个节点：

```bash
# 终端 1
go run main.go -addr=:8080 \
  -pool=http://localhost:8080,http://localhost:8081,http://localhost:8082

# 终端 2
go run main.go -addr=:8081 \
  -pool=http://localhost:8081,http://localhost:8080,http://localhost:8082

# 终端 3
go run main.go -addr=:8082 \
  -pool=http://localhost:8082,http://localhost:8080,http://localhost:8081
```

测试请求：
```bash
# 请求任意节点，都能获取到数据
curl "http://localhost:8080/color?name=red"   # 输出: #FF0000
curl "http://localhost:8081/color?name=green" # 输出: #00FF00
curl "http://localhost:8082/color?name=blue"  # 输出: #0000FF
```

## TODO List

- [ ] LRU-k算法
- [ ] 热点检测算法改进（HeavyKeeper算法）
- [ ] 批量获取接口支持
- [ ] 支持 TTL
- [ ] 支持主动预热

## 架构设计

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Client    │────▶│  MyCache    │────▶│   Getter    │
└─────────────┘     └─────────────┘     └─────────────┘
                           │
                    ┌──────┴──────┐
                    ▼             ▼
              ┌──────────┐  ┌──────────┐
              │MainCache │  │ HotCache │
              └──────────┘  └──────────┘
                    │
                    ▼
            ┌──────────────┐
            │ PeerPicker   │
            └──────────────┘
                    │
        ┌───────────┼───────────┐
        ▼           ▼           ▼
    ┌────────┐  ┌────────┐  ┌────────┐
    │ Peer 1 │  │ Peer 2 │  │ Peer 3 │
    └────────┘  └────────┘  └────────┘
```
