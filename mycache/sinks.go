package mycache

import (
	"errors"

	"google.golang.org/protobuf/proto"
)

// ============================================================
// Sink 接口 - 数据接收器
// ============================================================
// Sink 从 Get 调用接收数据
//
// 设计理念：
// Getter 的实现必须在成功时调用 Sink 的某一个 Set 方法
//
// Sink 提供了多种 Set 方法，允许 Getter 选择最高效的方式设置数据：
// - SetString: 当数据源是 string 时
// - SetBytes: 当数据源是 []byte 时
// - SetProto: 当数据源是 proto.Message 时
//
// 这种设计避免了不必要的类型转换和内存拷贝
type Sink interface {
	// SetString 将值设置为字符串 s
	SetString(s string) error

	// SetBytes 将值设置为字节切片 v 的内容
	// 调用者保留 v 的所有权（Sink 会进行必要的拷贝）
	SetBytes(v []byte) error

	// SetProto 将值设置为编码后的 proto.Message m
	// 调用者保留 m 的所有权
	SetProto(m proto.Message) error

	// view 返回数据的冻结视图，用于缓存
	// 这是一个内部方法，用于从 Sink 中提取 ByteView
	view() (ByteView, error)
}

// ============================================================
// 辅助函数
// ============================================================

// ------------------------------------------------------------
// cloneBytes - 克隆字节切片
// ------------------------------------------------------------
// cloneBytes 创建字节切片的深拷贝
// 这确保了 Sink 拥有数据的独立副本，避免外部修改影响缓存
func cloneBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

// ------------------------------------------------------------
// setSinkView - 将 ByteView 设置到 Sink
// ------------------------------------------------------------
// setSinkView 是一个优化函数，用于将 ByteView 高效地设置到 Sink
//
// 优化路径：
// 如果 Sink 实现了 viewSetter 接口（私有接口），直接设置 ByteView
// 这避免了从 ByteView 转换为 string/[]byte 再转换回来的开销
func setSinkView(s Sink, v ByteView) error {
	// viewSetter 是一个私有接口，允许 Sink 直接接收 ByteView
	// 这是一个快速路径，最小化拷贝，当数据已经在本地内存中缓存时
	// （缓存为 ByteView 形式）
	type viewSetter interface {
		setView(v ByteView) error
	}

	// 检查 Sink 是否支持直接设置 ByteView
	if vs, ok := s.(viewSetter); ok {
		return vs.setView(v)
	}

	// 否则根据 ByteView 的存储类型选择相应的 Set 方法
	if v.b != nil {
		return s.SetBytes(v.b)
	}
	return s.SetString(v.s)
}

// ============================================================
// StringSink - 字符串接收器
// ============================================================
// StringSink 返回一个填充指定字符串指针的 Sink
//
// 使用场景：
// 当调用者需要将缓存值作为 string 使用时
func StringSink(sp *string) Sink {
	return &stringSink{sp: sp}
}

// stringSink 是 StringSink 的实现
type stringSink struct {
	sp *string  // 目标字符串指针
	v  ByteView // 内部缓存的 ByteView
	// TODO(bradfitz): 跟踪是否调用了任何 Set 方法
}

// view 返回缓存的 ByteView
func (s *stringSink) view() (ByteView, error) {
	// TODO(bradfitz): 如果没有调用 Set，返回错误
	return s.v, nil
}

// SetString 设置字符串值
func (s *stringSink) SetString(v string) error {
	s.v.b = nil
	s.v.s = v
	*s.sp = v
	return nil
}

// SetBytes 将字节切片转换为字符串后设置
func (s *stringSink) SetBytes(v []byte) error {
	return s.SetString(string(v))
}

// SetProto 编码 proto.Message 后设置
func (s *stringSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	s.v.b = b
	*s.sp = string(b)
	return nil
}

// ============================================================
// ByteViewSink - ByteView 接收器
// ============================================================
// ByteViewSink 返回一个填充 ByteView 的 Sink
//
// 使用场景：
// 当调用者需要保持数据的不可变性，或需要零拷贝访问时
func ByteViewSink(dst *ByteView) Sink {
	if dst == nil {
		panic("nil dst")
	}
	return &byteViewSink{dst: dst}
}

// byteViewSink 是 ByteViewSink 的实现
type byteViewSink struct {
	dst *ByteView

	// 注意：如果将来要跟踪至少调用了一次 set* 方法，
	// 不要将多次调用 set 方法视为错误。Lorry 的 payload.go
	// 这样做是有意义的。文件顶部关于"恰好一个 Set 方法"的
	// 注释过于严格。我们真正关心的是至少一次（在处理器中），
	// 但如果多个处理器失败（或程序中多个函数使用同一个 Sink），
	// 重用同一个是可以的。
}

// setView 实现 viewSetter 接口（快速路径）
func (s *byteViewSink) setView(v ByteView) error {
	*s.dst = v
	return nil
}

// view 返回设置的 ByteView
func (s *byteViewSink) view() (ByteView, error) {
	return *s.dst, nil
}

// SetProto 编码 proto.Message 后设置为 ByteView
func (s *byteViewSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	*s.dst = ByteView{b: b}
	return nil
}

// SetBytes 将字节切片克隆后设置为 ByteView
func (s *byteViewSink) SetBytes(b []byte) error {
	*s.dst = ByteView{b: cloneBytes(b)}
	return nil
}

// SetString 将字符串设置为 ByteView
func (s *byteViewSink) SetString(v string) error {
	*s.dst = ByteView{s: v}
	return nil
}

// ============================================================
// ProtoSink - Protocol Buffer 接收器
// ============================================================
// ProtoSink 返回一个将二进制 proto 值反序列化到 m 的 Sink
//
// 使用场景：
// 当缓存的值是 Protocol Buffer 消息，并且需要直接反序列化为结构体时
func ProtoSink(m proto.Message) Sink {
	return &protoSink{
		dst: m,
	}
}

// protoSink 是 ProtoSink 的实现
type protoSink struct {
	dst proto.Message // 权威值（反序列化的目标）
	typ string        // 类型标识（未使用）

	v ByteView // 编码后的值（用于缓存）
}

// view 返回编码后的 ByteView
func (s *protoSink) view() (ByteView, error) {
	return s.v, nil
}

// SetBytes 反序列化字节切片到 proto.Message
func (s *protoSink) SetBytes(b []byte) error {
	err := proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = cloneBytes(b)
	s.v.s = ""
	return nil
}

// SetString 反序列化字符串到 proto.Message
func (s *protoSink) SetString(v string) error {
	b := []byte(v)
	err := proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

// SetProto 设置 proto.Message
// 注意：这里会先序列化再反序列化，以确保数据一致性
func (s *protoSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	// TODO(bradfitz): 针对相同任务的情况进行优化，直接写入？
	// 需要同时记录所有权规则。但那样我们可以直接赋值 *dst = *m
	// 目前这样工作：
	err = proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

// ============================================================
// AllocatingByteSliceSink - 分配字节切片接收器
// ============================================================
// AllocatingByteSliceSink 返回一个分配字节切片来保存接收值的 Sink
// 并将其赋值给 *dst。mycache 不保留这块内存。
//
// 使用场景：
// 当调用者需要拥有数据的独立副本，可以自由修改时
func AllocatingByteSliceSink(dst *[]byte) Sink {
	return &allocBytesSink{dst: dst}
}

// allocBytesSink 是 AllocatingByteSliceSink 的实现
type allocBytesSink struct {
	dst *[]byte
	v   ByteView
}

// view 返回内部的 ByteView
func (s *allocBytesSink) view() (ByteView, error) {
	return s.v, nil
}

// setView 实现 viewSetter 接口（快速路径）
func (s *allocBytesSink) setView(v ByteView) error {
	if v.b != nil {
		*s.dst = cloneBytes(v.b)
	} else {
		*s.dst = []byte(v.s)
	}
	s.v = v
	return nil
}

// SetProto 编码 proto.Message 后分配字节切片
func (s *allocBytesSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return s.setBytesOwned(b)
}

// SetBytes 克隆字节切片后分配
func (s *allocBytesSink) SetBytes(b []byte) error {
	return s.setBytesOwned(cloneBytes(b))
}

// setBytesOwned 设置拥有所有权的字节切片
func (s *allocBytesSink) setBytesOwned(b []byte) error {
	if s.dst == nil {
		return errors.New("nil AllocatingByteSliceSink *[]byte dst")
	}
	*s.dst = cloneBytes(b) // 再次拷贝，保护只读的 s.v.b 视图
	s.v.b = b
	s.v.s = ""
	return nil
}

// SetString 将字符串转换为字节切片后分配
func (s *allocBytesSink) SetString(v string) error {
	if s.dst == nil {
		return errors.New("nil AllocatingByteSliceSink *[]byte dst")
	}
	*s.dst = []byte(v)
	s.v.b = nil
	s.v.s = v
	return nil
}

// ============================================================
// TruncatingByteSliceSink - 截断字节切片接收器
// ============================================================
// TruncatingByteSliceSink 返回一个写入最多 len(*dst) 字节到 *dst 的 Sink
// 如果可用字节更多，它们会被静默截断。
// 如果可用字节少于 len(*dst)，*dst 会被缩小以适应可用字节数。
//
// 使用场景：
// 当有预分配的固定大小缓冲区，需要将数据写入其中时
// 例如：避免内存分配，或实现固定大小的缓存槽
func TruncatingByteSliceSink(dst *[]byte) Sink {
	return &truncBytesSink{dst: dst}
}

// truncBytesSink 是 TruncatingByteSliceSink 的实现
type truncBytesSink struct {
	dst *[]byte
	v   ByteView
}

// view 返回内部的 ByteView
func (s *truncBytesSink) view() (ByteView, error) {
	return s.v, nil
}

// SetProto 编码后写入，可能截断
func (s *truncBytesSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return s.setBytesOwned(b)
}

// SetBytes 写入字节，可能截断
func (s *truncBytesSink) SetBytes(b []byte) error {
	return s.setBytesOwned(cloneBytes(b))
}

// setBytesOwned 将字节写入目标，处理截断和缩小
func (s *truncBytesSink) setBytesOwned(b []byte) error {
	if s.dst == nil {
		return errors.New("nil TruncatingByteSliceSink *[]byte dst")
	}
	// 拷贝到目标缓冲区，最多拷贝 len(*s.dst) 字节
	n := copy(*s.dst, b)
	if n < len(*s.dst) {
		// 可用数据少于缓冲区大小，缩小切片
		*s.dst = (*s.dst)[:n]
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

// SetString 将字符串写入目标，可能截断
func (s *truncBytesSink) SetString(v string) error {
	if s.dst == nil {
		return errors.New("nil TruncatingByteSliceSink *[]byte dst")
	}
	// 拷贝字符串到目标缓冲区
	n := copy(*s.dst, v)
	if n < len(*s.dst) {
		// 可用数据少于缓冲区大小，缩小切片
		*s.dst = (*s.dst)[:n]
	}
	s.v.b = nil
	s.v.s = v
	return nil
}
