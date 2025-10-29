package mycache

import (
	"errors"
	
	"google.golang.org/protobuf/proto"
)

// ============================================================
// Sink - 数据接收器接口
// ============================================================

// Sink 接收Get调用返回的数据
type Sink interface {
	// SetString 设置值为字符串s
	SetString(s string) error
	
	// SetBytes 设置值为字节切片v的内容
	SetBytes(v []byte) error
	
	// SetProto 设置值为编码后的proto.Message
	SetProto(m proto.Message) error
	
	// view 返回数据的冻结视图
	view() (ByteView, error)
}

// ============================================================
// 各种Sink实现
// ============================================================

// stringSink 实现了填充字符串指针的Sink
type stringSink struct {
	sp *string
	v  ByteView
}

// StringSink 返回一个填充字符串指针的Sink
func StringSink(sp *string) Sink {
	return &stringSink{sp: sp}
}

func (s *stringSink) view() (ByteView, error) {
	return s.v, nil
}

func (s *stringSink) SetString(v string) error {
	s.v.b = nil
	s.v.s = v
	*s.sp = v
	return nil
}

func (s *stringSink) SetBytes(v []byte) error {
	return s.SetString(string(v))
}

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
// ByteViewSink
// ============================================================

// byteViewSink 填充ByteView
type byteViewSink struct {
	dst *ByteView
}

// ByteViewSink 返回一个填充ByteView的Sink
func ByteViewSink(dst *ByteView) Sink {
	if dst == nil {
		panic("nil dst")
	}
	return &byteViewSink{dst: dst}
}

func (s *byteViewSink) setView(v ByteView) error {
	*s.dst = v
	return nil
}

func (s *byteViewSink) view() (ByteView, error) {
	return *s.dst, nil
}

func (s *byteViewSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	*s.dst = ByteView{b: b}
	return nil
}

func (s *byteViewSink) SetBytes(b []byte) error {
	*s.dst = ByteView{b: cloneBytes(b)}
	return nil
}

func (s *byteViewSink) SetString(v string) error {
	*s.dst = ByteView{s: v}
	return nil
}

// ============================================================
// ProtoSink
// ============================================================

// protoSink 将二进制proto值反序列化到m
type protoSink struct {
	dst proto.Message
	v   ByteView
}

// ProtoSink 返回一个将二进制proto值反序列化到m的Sink
func ProtoSink(m proto.Message) Sink {
	return &protoSink{
		dst: m,
	}
}

func (s *protoSink) view() (ByteView, error) {
	return s.v, nil
}

func (s *protoSink) SetBytes(b []byte) error {
	err := proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = cloneBytes(b)
	s.v.s = ""
	return nil
}

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

func (s *protoSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	err = proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

// ============================================================
// AllocatingByteSliceSink
// ============================================================

// allocBytesSink 分配字节切片来保存接收值
type allocBytesSink struct {
	dst *[]byte
	v   ByteView
}

// AllocatingByteSliceSink 返回一个分配字节切片的Sink
func AllocatingByteSliceSink(dst *[]byte) Sink {
	return &allocBytesSink{dst: dst}
}

func (s *allocBytesSink) view() (ByteView, error) {
	return s.v, nil
}

func (s *allocBytesSink) setView(v ByteView) error {
	if v.b != nil {
		*s.dst = cloneBytes(v.b)
	} else {
		*s.dst = []byte(v.s)
	}
	s.v = v
	return nil
}

func (s *allocBytesSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return s.setBytesOwned(b)
}

func (s *allocBytesSink) SetBytes(b []byte) error {
	return s.setBytesOwned(cloneBytes(b))
}

func (s *allocBytesSink) setBytesOwned(b []byte) error {
	if s.dst == nil {
		return errors.New("nil AllocatingByteSliceSink dst")
	}
	*s.dst = cloneBytes(b)
	s.v.b = b
	s.v.s = ""
	return nil
}

func (s *allocBytesSink) SetString(v string) error {
	if s.dst == nil {
		return errors.New("nil AllocatingByteSliceSink dst")
	}
	*s.dst = []byte(v)
	s.v.b = nil
	s.v.s = v
	return nil
}

// ============================================================
// TruncatingByteSliceSink
// ============================================================

// truncBytesSink 写入最多len(*dst)字节到*dst
type truncBytesSink struct {
	dst *[]byte
	v   ByteView
}

// TruncatingByteSliceSink 返回一个截断字节切片的Sink
func TruncatingByteSliceSink(dst *[]byte) Sink {
	return &truncBytesSink{dst: dst}
}

func (s *truncBytesSink) view() (ByteView, error) {
	return s.v, nil
}

func (s *truncBytesSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return s.setBytesOwned(b)
}

func (s *truncBytesSink) SetBytes(b []byte) error {
	return s.setBytesOwned(cloneBytes(b))
}

func (s *truncBytesSink) setBytesOwned(b []byte) error {
	if s.dst == nil {
		return errors.New("nil TruncatingByteSliceSink dst")
	}
	n := copy(*s.dst, b)
	if n < len(*s.dst) {
		*s.dst = (*s.dst)[:n]
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

func (s *truncBytesSink) SetString(v string) error {
	if s.dst == nil {
		return errors.New("nil TruncatingByteSliceSink dst")
	}
	n := copy(*s.dst, v)
	if n < len(*s.dst) {
		*s.dst = (*s.dst)[:n]
	}
	s.v.b = nil
	s.v.s = v
	return nil
}

// ============================================================
// 辅助函数
// ============================================================

// setSinkView 是一个优化函数，用于将ByteView高效地设置到Sink
func setSinkView(s Sink, v ByteView) error {
	type viewSetter interface {
		setView(v ByteView) error
	}
	
	if vs, ok := s.(viewSetter); ok {
		return vs.setView(v)
	}
	
	if v.b != nil {
		return s.SetBytes(v.b)
	}
	return s.SetString(v.s)
}
