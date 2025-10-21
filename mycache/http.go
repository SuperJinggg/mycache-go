package mycache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"mycache/consistenthash"
	pb "mycache/mycachepb"

	"google.golang.org/protobuf/proto"
)

// 默认的 HTTP 基础路径和一致性哈希副本数
const defaultBasePath = "/_mycache/"
const defaultReplicas = 50

// ============================================================
// HTTPPool - 基于 HTTP 的节点池实现
// ============================================================
// HTTPPool 实现了 PeerPicker 接口，用于 HTTP 协议的节点池
//
// 核心职责：
// 1. 作为 HTTP 服务器：接收来自其他节点的缓存请求
// 2. 作为 HTTP 客户端：向其他节点发起缓存请求
// 3. 节点选择：使用一致性哈希选择 key 的拥有者节点
// ============================================================
type HTTPPool struct {
	// Context 可选地指定服务器接收请求时使用的 context
	// 如果为 nil，服务器使用请求自带的 context
	Context func(*http.Request) context.Context

	// Transport 可选地指定客户端发起请求时使用的 http.RoundTripper
	// 如果为 nil，客户端使用 http.DefaultTransport
	Transport func(context.Context) http.RoundTripper

	// 本节点的基础 URL，例如 "https://example.net:8000"
	self string

	// opts 指定配置选项
	opts HTTPPoolOptions

	// 保护 peers 和 httpGetters 的互斥锁
	mu sync.Mutex
	// 一致性哈希环，用于选择节点
	peers *consistenthash.Map
	// HTTP 客户端映射表，key 为节点的 URL（如 "http://10.0.0.2:8008"）
	httpGetters map[string]*httpGetter
}

// ============================================================
// HTTPPoolOptions - HTTP 池配置选项
// ============================================================
type HTTPPoolOptions struct {
	// BasePath 指定处理 mycache 请求的 HTTP 路径
	// 如果为空，默认为 "/_mycache/"
	BasePath string

	// Replicas 指定一致性哈希中每个真实节点的虚拟节点数
	// 如果为空，默认为 50
	// 虚拟节点越多，负载分布越均匀，但内存开销也越大
	Replicas int

	// HashFn 指定一致性哈希使用的哈希函数
	// 如果为空，默认使用 crc32.ChecksumIEEE
	HashFn consistenthash.Hash
}

// ============================================================
// NewHTTPPool - 创建 HTTP 节点池（便捷方法）
// ============================================================
// NewHTTPPool 初始化一个 HTTP 节点池，并将自己注册为 PeerPicker
// 为了方便，它还将自己注册为 http.DefaultServeMux 的处理器
//
// 参数 self 应该是指向当前服务器的有效基础 URL
// 例如 "http://example.net:8000"
func NewHTTPPool(self string) *HTTPPool {
	p := NewHTTPPoolOpts(self, nil)
	// 自动注册为 HTTP 处理器
	http.Handle(p.opts.BasePath, p)
	return p
}

// httpPoolMade 确保只创建一个 HTTPPool 实例
var httpPoolMade bool

// ============================================================
// NewHTTPPoolOpts - 创建 HTTP 节点池（带选项）
// ============================================================
// NewHTTPPoolOpts 使用给定选项初始化 HTTP 节点池
// 与 NewHTTPPool 不同，此函数不会自动注册为 HTTP 处理器
// 返回的 *HTTPPool 实现了 http.Handler，必须使用 http.Handle 手动注册
func NewHTTPPoolOpts(self string, o *HTTPPoolOptions) *HTTPPool {
	// 确保全局只创建一个 HTTPPool
	if httpPoolMade {
		panic("mycache: NewHTTPPool must be called only once")
	}
	httpPoolMade = true

	// 创建 HTTPPool 实例
	p := &HTTPPool{
		self:        self,
		httpGetters: make(map[string]*httpGetter),
	}

	// 应用选项
	if o != nil {
		p.opts = *o
	}
	// 设置默认值
	if p.opts.BasePath == "" {
		p.opts.BasePath = defaultBasePath
	}
	if p.opts.Replicas == 0 {
		p.opts.Replicas = defaultReplicas
	}

	// 初始化一致性哈希环
	p.peers = consistenthash.New(p.opts.Replicas, p.opts.HashFn)

	// 注册为全局的 PeerPicker
	RegisterPeerPicker(func() PeerPicker { return p })
	return p
}

// ============================================================
// Set - 更新节点池的节点列表
// ============================================================
// Set 更新节点池的节点列表
// 每个 peer 值应该是有效的基础 URL
// 例如 "http://example.net:8000"
//
// 注意：
// - 这个方法会完全替换之前的节点列表
// - 通常在启动时调用一次，或在节点拓扑变化时调用
func (p *HTTPPool) Set(peers ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 重新创建一致性哈希环
	p.peers = consistenthash.New(p.opts.Replicas, p.opts.HashFn)
	// 将所有节点添加到哈希环
	p.peers.Add(peers...)

	// 为每个节点创建 HTTP 客户端
	p.httpGetters = make(map[string]*httpGetter, len(peers))
	for _, peer := range peers {
		p.httpGetters[peer] = &httpGetter{
			transport: p.Transport,
			baseURL:   peer + p.opts.BasePath,
		}
	}
}

// ============================================================
// PickPeer - 选择 key 的拥有者节点
// ============================================================
// PickPeer 根据 key 选择拥有者节点
//
// 返回值：
//
//	peer: 拥有该 key 的远程节点的 ProtoGetter
//	ok: 如果选中了远程节点返回 true；如果是本节点则返回 false
//
// 一致性哈希的工作原理：
// 1. 计算 key 的哈希值
// 2. 在哈希环上顺时针查找最近的虚拟节点
// 3. 返回该虚拟节点对应的真实节点
func (p *HTTPPool) PickPeer(key string) (ProtoGetter, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 检查是否有可用的节点
	if p.peers.IsEmpty() {
		return nil, false
	}

	// 使用一致性哈希选择节点
	if peer := p.peers.Get(key); peer != p.self {
		// 选中的是远程节点，返回其 HTTP 客户端
		return p.httpGetters[peer], true
	}

	// 选中的是本节点，返回 nil, false
	return nil, false
}

// ============================================================
// ServeHTTP - 处理 HTTP 请求（服务器端）
// ============================================================
// ServeHTTP 实现 http.Handler 接口
// 处理来自其他节点的缓存查询请求
//
// URL 格式：BasePath/groupName/key
// 例如：/_mycache/mygroup/mykey
func (p *HTTPPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 解析请求
	// 检查 URL 路径前缀
	if !strings.HasPrefix(r.URL.Path, p.opts.BasePath) {
		panic("HTTPPool serving unexpected path: " + r.URL.Path)
	}

	// 分割路径：BasePath 后面应该是 "groupName/key"
	parts := strings.SplitN(r.URL.Path[len(p.opts.BasePath):], "/", 2)
	if len(parts) != 2 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	groupName := parts[0]
	key := parts[1]

	// 2. 获取指定的缓存组
	group := GetGroup(groupName)
	if group == nil {
		http.Error(w, "no such group: "+groupName, http.StatusNotFound)
		return
	}

	// 3. 准备 context
	var ctx context.Context
	if p.Context != nil {
		ctx = p.Context(r)
	} else {
		ctx = r.Context()
	}

	// 4. 统计服务器请求数
	group.Stats.ServerRequests.Add(1)

	// 5. 从缓存组获取数据
	var value []byte
	err := group.Get(ctx, key, AllocatingByteSliceSink(&value))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 6. 将值序列化为 protobuf 消息
	body, err := proto.Marshal(&pb.GetResponse{Value: value})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 7. 返回响应
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(body)
}

// ============================================================
// httpGetter - HTTP 客户端实现
// ============================================================
// httpGetter 实现 ProtoGetter 接口
// 负责通过 HTTP 从远程节点获取数据
type httpGetter struct {
	transport func(context.Context) http.RoundTripper
	baseURL   string // 例如 "http://10.0.0.2:8008/_mycache/"
}

// bufferPool 复用 bytes.Buffer，减少内存分配
var bufferPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}

// ============================================================
// Get - 从远程节点获取数据（客户端）
// ============================================================
// Get 通过 HTTP 请求从远程节点获取数据
//
// 工作流程：
// 1. 构造 URL：baseURL/group/key
// 2. 发起 HTTP GET 请求
// 3. 读取响应体（protobuf 格式）
// 4. 反序列化为 GetResponse
func (h *httpGetter) Get(ctx context.Context, in *pb.GetRequest, out *pb.GetResponse) error {
	// 1. 构造请求 URL
	// URL 格式：baseURL/group/key
	// QueryEscape 确保 group 和 key 中的特殊字符被正确编码
	u := fmt.Sprintf(
		"%v%v/%v",
		h.baseURL,
		url.QueryEscape(in.GetGroup()),
		url.QueryEscape(in.GetKey()),
	)

	// 2. 创建 HTTP 请求
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	// 将 context 附加到请求
	req = req.WithContext(ctx)

	// 3. 选择 Transport
	tr := http.DefaultTransport
	if h.transport != nil {
		tr = h.transport(ctx)
	}

	// 4. 发起请求
	res, err := tr.RoundTrip(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// 5. 检查状态码
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned: %v", res.Status)
	}

	// 6. 读取响应体
	// 从 pool 获取 buffer，减少内存分配
	b := bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	defer bufferPool.Put(b)

	_, err = io.Copy(b, res.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %v", err)
	}

	// 7. 反序列化 protobuf
	err = proto.Unmarshal(b.Bytes(), out)
	if err != nil {
		return fmt.Errorf("decoding response body: %v", err)
	}

	return nil
}
