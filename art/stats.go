package art

import "unsafe"

// NodeKind 导出节点规格类型，供外部（压测、页面）按规格遍历统计。
type NodeKind = nodeKind

// NodeKindView 是页面/压测里固定的四种规格视图，按容量从小到大。
type NodeKindView int

const (
	ViewNode4 NodeKindView = iota
	ViewNode16
	ViewNode48
	ViewNode256
)

// KindOrder 四种规格的固定展示顺序。
var KindOrder = []nodeKind{kNode4, kNode16, kNode48, kNode256}

// KindLabel 返回规格的可读名称。
func KindLabel(k nodeKind) string {
	switch k {
	case kNode4:
		return "node4"
	case kNode16:
		return "node16"
	case kNode48:
		return "node48"
	case kNode256:
		return "node256"
	}
	return "?"
}

// KindStats 某一种节点规格的数量与内存占用。
type KindStats struct {
	Kind         nodeKind
	Count        int
	BytesPerNode int // 单个节点结构的固定大小
	Bytes        int64
}

// Stats 全树统计快照（页面“节点规格分布”和“内存占用”两块的数据源）。
type Stats struct {
	InnerNodes int // 内部节点总数
	Leaves     int
	MaxDepth   int // 最深叶子的深度（根叶子深度为 0）
	LeafDepth  int // 所有叶子深度之和，除以 Leaves 得平均深度

	InnerBytes   int64 // 四种内部节点结构
	LeafHdrBytes int64 // leaf 结构头（含两个切片头）
	KeyBytesRaw  int64 // 原始用户键字节
	KeyBytesEnc  int64 // 实际存储的编码键字节（含转义与结束符）
	ValueBytes   int64 // 值字节
	TotalBytes   int64

	ByKind map[nodeKind]*KindStats
}

func (s *Stats) avgDepth() float64 {
	if s.Leaves == 0 {
		return 0
	}
	return float64(s.LeafDepth) / float64(s.Leaves)
}

// AvgDepth 导出平均叶子深度。
func (s *Stats) AvgDepth() float64 { return s.avgDepth() }

var leafHdrSize = int(unsafe.Sizeof(leaf{}))

// Collect 遍历整棵树统计节点数量与内存。
func (t *Tree) Collect() *Stats {
	s := &Stats{ByKind: map[nodeKind]*KindStats{
		kNode4:   {Kind: kNode4, BytesPerNode: int(unsafe.Sizeof(node4{}))},
		kNode16:  {Kind: kNode16, BytesPerNode: int(unsafe.Sizeof(node16{}))},
		kNode48:  {Kind: kNode48, BytesPerNode: int(unsafe.Sizeof(node48{}))},
		kNode256: {Kind: kNode256, BytesPerNode: int(unsafe.Sizeof(node256{}))},
	}}
	if t.root == nil {
		return s
	}
	s.walk(t.root, 0)

	s.KeyBytesRaw = int64(t.keyBytes)
	s.KeyBytesEnc = int64(t.encKeyBytes)
	s.ValueBytes = int64(t.valBytes)
	for _, k := range s.ByKind {
		s.InnerBytes += k.Bytes
		s.InnerNodes += k.Count
	}
	s.TotalBytes = s.InnerBytes + s.LeafHdrBytes + s.KeyBytesEnc + s.ValueBytes
	return s
}

func (s *Stats) walk(p unsafe.Pointer, depth int) {
	if isLeaf(p) {
		s.Leaves++
		s.LeafDepth += depth
		if depth > s.MaxDepth {
			s.MaxDepth = depth
		}
		s.LeafHdrBytes += int64(leafHdrSize)
		return
	}
	n := asInner(p)
	k := s.ByKind[n.kind()]
	k.Count++
	k.Bytes += int64(k.BytesPerNode)
	// 按分支字节序遍历孩子，不能按物理槽位（node48/node256 有空洞/乱序）。
	b := n.firstByte()
	for i := 0; i < n.cnt(); i++ {
		s.walk(n.findChild(b), depth+1)
		if i+1 < n.cnt() {
			nb, ok := n.nextByte(b)
			if !ok {
				break
			}
			b = nb
		}
	}
}
