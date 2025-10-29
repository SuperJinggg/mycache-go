package mycache

import (
	"context"
	
	pb "mycache/mycachepb"
)

// ============================================================
// 分布式节点接口
// ============================================================

// ProtoGetter 是节点必须实现的接口，用于从远程节点获取数据
type ProtoGetter interface {
	Get(ctx context.Context, in *pb.GetRequest, out *pb.GetResponse) error
}

// PeerPicker 必须实现以定位拥有特定key的节点
type PeerPicker interface {
	// PickPeer 返回拥有指定key的节点
	// 如果选中了远程节点，返回peer和true
	// 如果key属于当前节点，返回nil和false
	PickPeer(key string) (peer ProtoGetter, ok bool)
}

// NoPeers 是PeerPicker的一个空实现，永远不会找到远程节点
type NoPeers struct{}

// PickPeer 总是返回nil, false
func (NoPeers) PickPeer(key string) (peer ProtoGetter, ok bool) { return }

// ============================================================
// 全局节点选择器注册
// ============================================================

var (
	portPicker func(groupName string) PeerPicker
)

// RegisterPeerPicker 注册节点初始化函数
func RegisterPeerPicker(fn func() PeerPicker) {
	if portPicker != nil {
		panic("RegisterPeerPicker called more than once")
	}
	portPicker = func(_ string) PeerPicker { return fn() }
}

// RegisterPerGroupPeerPicker 注册按组的节点初始化函数
func RegisterPerGroupPeerPicker(fn func(groupName string) PeerPicker) {
	if portPicker != nil {
		panic("RegisterPeerPicker called more than once")
	}
	portPicker = fn
}

// getPeers 内部函数，用于获取group的PeerPicker
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

// ============================================================
// Group的Peers方法
// ============================================================

// RegisterPeers 允许组注册PeerPicker
func (g *Group) RegisterPeers(peers PeerPicker) {
	if g.peers != nil {
		panic("RegisterPeers called more than once")
	}
	g.peers = peers
}
