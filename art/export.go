package art

import "unsafe"

// 本文件把内部树结构导出成与具体节点类型解耦的 JSON 友好视图，
// 供 cmd/server 的“树结构”页面渲染。

// NodeView 节点的可序列化视图。字节字段以十六进制字符串表示，
// 既保序又避免 JSON base64 干扰前端展示（hex 字典序 == 原始字节序）。
type NodeView struct {
	Kind       string      `json:"kind"` // leaf / node4 / node16 / node48 / node256
	PrefixHex  string      `json:"prefixHex,omitempty"`
	PrefixText string      `json:"prefixText,omitempty"`
	PrefixLen  int         `json:"prefixLen,omitempty"`
	Count      int         `json:"count,omitempty"`
	KeyHex     string      `json:"keyHex,omitempty"`    // 仅叶子
	KeyText    string      `json:"keyText,omitempty"`   // 仅叶子，可读形式
	ValueSize  int         `json:"valueSize,omitempty"` // 仅叶子
	Children   []ChildView `json:"children,omitempty"`
}

// ChildView 带分支字节的孩子。
type ChildView struct {
	BranchHex string    `json:"branchHex"`
	Node      *NodeView `json:"node"`
}

// ExportTree 导出整棵树；maxDepth<0 表示不限制深度。
// 叶子键会做长度截断，避免超大页面。
func (t *Tree) ExportTree(maxDepth int) *NodeView {
	if t.root == nil {
		return &NodeView{Kind: "empty"}
	}
	return exportNode(t.root, 0, maxDepth)
}

const exportKeyLimit = 64

const hexDigits = "0123456789abcdef"

func exportNode(p unsafe.Pointer, depth, maxDepth int) *NodeView {
	if isLeaf(p) {
		l := asLeaf(p)
		raw := decodeKey(l.key)
		shown := raw
		if len(shown) > exportKeyLimit {
			shown = shown[:exportKeyLimit]
		}
		return &NodeView{
			Kind:      "leaf",
			KeyHex:    hexOf(shown),
			KeyText:   printableOf(shown),
			ValueSize: len(l.val),
		}
	}
	n := asInner(p)
	h := headerOf(n)
	plen := int(h.prefixLen)
	v := &NodeView{
		Kind:      KindLabel(n.kind()),
		PrefixLen: plen,
		Count:     n.cnt(),
	}
	if plen > 0 {
		shown := plen
		if shown > maxPrefix {
			shown = maxPrefix
		}
		pb := append([]byte(nil), h.prefix[:shown]...)
		v.PrefixHex = hexOf(pb)
		v.PrefixText = printableOf(pb)
	}
	if maxDepth >= 0 && depth >= maxDepth {
		v.Children = []ChildView{}
		return v
	}
	b := n.firstByte()
	for i := 0; i < n.cnt(); i++ {
		kid := n.findChild(b)
		v.Children = append(v.Children, ChildView{
			BranchHex: hexOf([]byte{b}),
			Node:      exportNode(kid, depth+1, maxDepth),
		})
		if i+1 < n.cnt() {
			if nb, ok := n.nextByte(b); ok {
				b = nb
			}
		}
	}
	return v
}

func hexOf(b []byte) string {
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i] = hexDigits[c>>4]
		out[2*i+1] = hexDigits[c&0x0f]
	}
	return string(out)
}

func printableOf(b []byte) string {
	var sb []byte
	for _, c := range b {
		if c >= 32 && c < 127 {
			sb = append(sb, c)
		} else {
			sb = append(sb, '\\', 'x')
			sb = append(sb, hexDigits[c>>4], hexDigits[c&0x0f])
		}
	}
	return string(sb)
}

// PageKeys 分页返回有序键，供“分键浏览”页面使用。
//
//	after 为空时从头开始；否则返回严格大于 after 的键。
//	limit 为每页条数。
//
// 注意：返回给调用方做下一页游标的 cursor 是“完整键”，不能用展示用的
// 截断键，否则 Seek 无法精确定位会导致翻页停在同一页。
func (t *Tree) PageKeys(after []byte, limit int) (keys [][]byte, valueSizes []int, cursor []byte, total int) {
	total = t.Len()
	if limit <= 0 {
		limit = 50
	}
	it := t.Iterator()
	if len(after) == 0 {
		it.First()
	} else {
		// Seek 到 > after：定位后若恰好相等则再 Next 一次。
		it.Seek(after)
		if it.Valid() && bytesEqual(it.Key(), after) {
			it.Next()
		}
	}
	for it.Valid() && len(keys) < limit {
		full := it.Key() // 完整键，游标用
		shown := full
		if len(shown) > exportKeyLimit {
			shown = shown[:exportKeyLimit]
		}
		keys = append(keys, shown)
		valueSizes = append(valueSizes, len(it.Value()))
		cursor = append([]byte(nil), full...)
		it.Next()
	}
	return keys, valueSizes, cursor, total
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
