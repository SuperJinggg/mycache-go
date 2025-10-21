package mycache

import (
	"bytes"
	"errors"
	"io"
	"strings"
)

// ============================================================
// ByteView - 不可变字节视图
// ============================================================
// ByteView 是对字节数据的不可变视图封装。
// 它内部可以包装 []byte 或 string，但这个细节对调用者是透明的。
//
// 设计理念：
// 1. 值类型使用：ByteView 应该作为值类型使用，而不是指针（类似 time.Time）
// 2. 不可变性：一旦创建，内容不可更改，保证并发安全
// 3. 零拷贝优化：当数据源是 string 时，避免不必要的内存拷贝
// ============================================================
type ByteView struct {
	// 内部存储：使用两个字段实现灵活存储
	// 如果 b 非空，使用 b；否则使用 s
	// 这种设计允许我们根据数据来源选择最优存储方式
	b []byte // 字节切片存储
	s string // 字符串存储
}

// ------------------------------------------------------------
// Len 返回视图的长度（字节数）
// ------------------------------------------------------------
func (v ByteView) Len() int {
	// 优先检查字节切片
	if v.b != nil {
		return len(v.b)
	}
	// 否则返回字符串长度
	return len(v.s)
}

// ------------------------------------------------------------
// ByteSlice 返回数据的字节切片副本
// 注意：这是一个副本，修改返回值不会影响原始数据
// ------------------------------------------------------------
func (v ByteView) ByteSlice() []byte {
	if v.b != nil {
		// 如果是字节切片存储，返回克隆副本
		return cloneBytes(v.b)
	}
	// 如果是字符串存储，转换为字节切片（会产生拷贝）
	return []byte(v.s)
}

// ------------------------------------------------------------
// String 将数据作为字符串返回，必要时创建副本
// ------------------------------------------------------------
func (v ByteView) String() string {
	if v.b != nil {
		// 字节切片转字符串（产生拷贝）
		return string(v.b)
	}
	// 字符串存储，直接返回
	return v.s
}

// ------------------------------------------------------------
// At 返回索引 i 处的字节
// 类似于数组索引操作 v[i]
// ------------------------------------------------------------
func (v ByteView) At(i int) byte {
	if v.b != nil {
		return v.b[i]
	}
	return v.s[i]
}

// ------------------------------------------------------------
// Slice 对视图进行切片操作，返回 [from:to) 范围的新视图
// 注意：这是浅拷贝，新视图与原视图共享底层数据
// ------------------------------------------------------------
func (v ByteView) Slice(from, to int) ByteView {
	if v.b != nil {
		// 对字节切片进行切片
		return ByteView{b: v.b[from:to]}
	}
	// 对字符串进行切片
	return ByteView{s: v.s[from:to]}
}

// ------------------------------------------------------------
// SliceFrom 从指定索引切片到末尾
// 等价于 Slice(from, v.Len())
// ------------------------------------------------------------
func (v ByteView) SliceFrom(from int) ByteView {
	if v.b != nil {
		return ByteView{b: v.b[from:]}
	}
	return ByteView{s: v.s[from:]}
}

// ------------------------------------------------------------
// Copy 将视图的内容拷贝到目标切片 dest 中
// 返回实际拷贝的字节数（遵循 io.Reader 的语义）
// ------------------------------------------------------------
func (v ByteView) Copy(dest []byte) int {
	if v.b != nil {
		// 从字节切片拷贝
		return copy(dest, v.b)
	}
	// 从字符串拷贝
	return copy(dest, v.s)
}

// ------------------------------------------------------------
// Equal 判断两个 ByteView 的内容是否相等
// 这是高效的相等性检查，会根据存储类型选择最优比较方式
// ------------------------------------------------------------
func (v ByteView) Equal(b2 ByteView) bool {
	if b2.b == nil {
		// b2 是字符串存储，使用字符串比较
		return v.EqualString(b2.s)
	}
	// b2 是字节切片存储，使用字节切片比较
	return v.EqualBytes(b2.b)
}

// ------------------------------------------------------------
// EqualString 判断视图内容是否与字符串 s 相等
// ------------------------------------------------------------
func (v ByteView) EqualString(s string) bool {
	if v.b == nil {
		// 两者都是字符串，直接比较
		return v.s == s
	}
	// v 是字节切片，需要逐字节比较
	l := v.Len()
	if len(s) != l {
		// 长度不同，必然不相等
		return false
	}
	// 逐字节比较
	for i, bi := range v.b {
		if bi != s[i] {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------
// EqualBytes 判断视图内容是否与字节切片 b2 相等
// ------------------------------------------------------------
func (v ByteView) EqualBytes(b2 []byte) bool {
	if v.b != nil {
		// 两者都是字节切片，使用标准库的高效比较
		return bytes.Equal(v.b, b2)
	}
	// v 是字符串存储，需要逐字节比较
	l := v.Len()
	if len(b2) != l {
		// 长度不同，必然不相等
		return false
	}
	// 逐字节比较（注意迭代顺序）
	for i, bi := range b2 {
		if bi != v.s[i] {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------
// Reader 返回一个 io.ReadSeeker，用于读取视图中的字节
// 返回的 Reader 支持 Seek 操作，可以随机访问
// ------------------------------------------------------------
func (v ByteView) Reader() io.ReadSeeker {
	if v.b != nil {
		// 使用 bytes.Reader，支持从字节切片读取
		return bytes.NewReader(v.b)
	}
	// 使用 strings.Reader，支持从字符串读取
	return strings.NewReader(v.s)
}

// ------------------------------------------------------------
// ReadAt 实现 io.ReaderAt 接口
// 从偏移量 off 开始读取数据到 p 中
// 这允许并发的随机读取操作
// ------------------------------------------------------------
func (v ByteView) ReadAt(p []byte, off int64) (n int, err error) {
	// 检查偏移量有效性
	if off < 0 {
		return 0, errors.New("view: invalid offset")
	}
	if off >= int64(v.Len()) {
		// 偏移量超出范围，返回 EOF
		return 0, io.EOF
	}
	// 从偏移量开始切片，然后拷贝数据
	n = v.SliceFrom(int(off)).Copy(p)
	if n < len(p) {
		// 读取的字节数少于请求的字节数，返回 EOF
		err = io.EOF
	}
	return
}

// ------------------------------------------------------------
// WriteTo 实现 io.WriterTo 接口
// 将视图的所有内容写入 Writer w
// 返回写入的字节数和可能的错误
// ------------------------------------------------------------
func (v ByteView) WriteTo(w io.Writer) (n int64, err error) {
	var m int
	if v.b != nil {
		// 写入字节切片
		m, err = w.Write(v.b)
	} else {
		// 写入字符串（使用 io.WriteString 可能更高效）
		m, err = io.WriteString(w, v.s)
	}
	if err == nil && m < v.Len() {
		// 没有错误但写入不完整，返回短写错误
		err = io.ErrShortWrite
	}
	n = int64(m)
	return
}
