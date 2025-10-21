// peers.go 定义了进程如何查找和与其他节点通信
package mycache

import (
	"context"

	pb "mycache/mycachepb"
)

// ============================================================
// Context - context.Context 的别名
// ============================================================
// Context 是 context.Context 的别名，用于向后兼容
type Context = context.Context

// ============================================================
// ProtoGetter - 节点通信接口
// ============================================================
// ProtoGetter 是必须由节点实现的接口
// 它定义了如何从远程节点获取数据
//
// 使用 Protocol Buffers 作为序列化格式的原因：
// 1. 高效的二进制序列化
// 2. 跨语言兼容性
// 3. 向后兼容的版本演进
type ProtoGetter interface {
	// Get 从远程节点获取数据
	//
	// 参数：
	//   ctx: 请求的 context，用于超时控制和取消
	//   in: 请求消息，包含 group 名称和 key
	//   out: 响应消息，用于接收返回的 value
	//
	// 返回值：
	//   error: 获取过程中的错误
	Get(ctx context.Context, in *pb.GetRequest, out *pb.GetResponse) error
}

// ============================================================
// PeerPicker - 节点选择器接口
// ============================================================
// PeerPicker 必须实现以定位拥有特定 key 的节点
//
// 核心职责：
// 根据 key 使用一致性哈希算法选择负责该 key 的节点
type PeerPicker interface {
	// PickPeer 返回拥有指定 key 的节点
	//
	// 返回值：
	//   peer: 远程节点的 ProtoGetter（如果选中了远程节点）
	//   ok: true 表示选中了远程节点；false 表示 key 属于当前节点
	//
	// 一致性哈希的好处：
	// 1. 当节点增减时，只有少量 key 需要重新分配
	// 2. 负载相对均衡
	// 3. 相同的 key 总是映射到相同的节点（在拓扑不变的情况下）
	PickPeer(key string) (peer ProtoGetter, ok bool)
}

// ============================================================
// NoPeers - 无节点实现
// ============================================================
// NoPeers 是 PeerPicker 的一个实现，永远不会找到远程节点
// 用于单机模式或测试场景
type NoPeers struct{}

// PickPeer 总是返回 nil, false，表示所有 key 都属于本地节点
func (NoPeers) PickPeer(key string) (peer ProtoGetter, ok bool) { return }

// ============================================================
// 全局节点选择器注册机制
// ============================================================
var (
	// portPicker 是全局的节点选择器工厂函数
	portPicker func(groupName string) PeerPicker
)

// ------------------------------------------------------------
// RegisterPeerPicker - 注册节点选择器（简单版本）
// ------------------------------------------------------------
// RegisterPeerPicker 注册节点初始化函数
// 它在第一个 group 创建时被调用一次
//
// 注意：
// RegisterPeerPicker 和 RegisterPerGroupPeerPicker 只能调用其中一个
func RegisterPeerPicker(fn func() PeerPicker) {
	if portPicker != nil {
		panic("RegisterPeerPicker called more than once")
	}
	// 包装为忽略 groupName 的版本
	portPicker = func(_ string) PeerPicker { return fn() }
}

// ------------------------------------------------------------
// RegisterPerGroupPeerPicker - 注册节点选择器（按组版本）
// ------------------------------------------------------------
// RegisterPerGroupPeerPicker 注册节点初始化函数
// 该函数接收 groupName 参数，可以为不同的 group 使用不同的 PeerPicker
//
// 使用场景：
// - 不同的缓存组可能部署在不同的节点集群上
// - 可以为不同的 group 配置不同的一致性哈希参数
func RegisterPerGroupPeerPicker(fn func(groupName string) PeerPicker) {
	if portPicker != nil {
		panic("RegisterPeerPicker called more than once")
	}
	portPicker = fn
}

// ------------------------------------------------------------
// getPeers - 获取指定 group 的节点选择器
// ------------------------------------------------------------
// getPeers 是内部函数，用于获取 group 的 PeerPicker
// 如果没有注册 PeerPicker，返回 NoPeers{}
func getPeers(groupName string) PeerPicker {
	if portPicker == nil {
		return NoPeers{}
	}
	pk := portPicker(groupName)
	if pk == nil {
		pk = NoPeers{}
	}
	return pk
}
