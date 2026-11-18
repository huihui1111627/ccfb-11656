package art

import "unsafe"

const maxPrefix = 10 // 压缩前缀最多内联保存的字节数，剩余部分到叶子再核对

type nodeKind uint16

const (
	kNode4   nodeKind = 4
	kNode16  nodeKind = 16
	kNode48  nodeKind = 48
	kNode256 nodeKind = 256
)

// node 是四种内部节点的公共接口。
type node interface {
	kind() nodeKind
	cnt() int
	prefixLen() int
	findChild(b byte) unsafe.Pointer // 未命中返回 nil
	// firstByte 返回最小的已占用分支字节；空节点行为未定义（调用方保证非空）。
	firstByte() byte
	// nextByte 返回严格大于 after 的下一个已占用字节；没有时第二个返回值为 false。
	// 遍历统一走字节序，与 kids[] 的物理槽位排列完全解耦。
	nextByte(after byte) (byte, bool)
	// lowerBoundByte 返回最小的已占用且 >= b 的分支字节；没有时 ok=false。
	lowerBoundByte(b byte) (byte, bool)
}

type nodeHeader struct {
	kind      nodeKind
	prefixLen byte // <= maxPrefix；实际压缩前缀可能更长
	prefix    [maxPrefix]byte
}

// node4：最多 4 个子节点，键与指针数组并行存放，线性查找。
type node4 struct {
	nodeHeader
	keys  [4]byte
	kids  [4]unsafe.Pointer
	count int
}

// node16：最多 16 个子节点，键按序存放，查找用一次 8 字节并行比较
// （SWAR，见 findChild），不依赖具体 CPU 指令集。
type node16 struct {
	nodeHeader
	keys  [16]byte
	kids  [16]unsafe.Pointer
	count int
}

// node48：最多 48 个子节点。keys 是一张 256 项下标表（0 表示空槽），
// 命中后按下标取 kids，避免像 node256 那样为每个字节都存一个指针。
type node48 struct {
	nodeHeader
	keys  [256]byte
	kids  [48]unsafe.Pointer
	count int
}

// node256：每个可能的字节直接对应一个指针，一次寻址完成查找。
type node256 struct {
	nodeHeader
	kids  [256]unsafe.Pointer
	count int
}

func (n *node4) kind() nodeKind   { return n.nodeHeader.kind }
func (n *node16) kind() nodeKind  { return n.nodeHeader.kind }
func (n *node48) kind() nodeKind  { return n.nodeHeader.kind }
func (n *node256) kind() nodeKind { return n.nodeHeader.kind }
func (n *node4) cnt() int         { return n.count }
func (n *node16) cnt() int        { return n.count }
func (n *node48) cnt() int        { return n.count }
func (n *node256) cnt() int       { return n.count }
func (n *node4) prefixLen() int   { return int(n.nodeHeader.prefixLen) }
func (n *node16) prefixLen() int  { return int(n.nodeHeader.prefixLen) }
func (n *node48) prefixLen() int  { return int(n.nodeHeader.prefixLen) }
func (n *node256) prefixLen() int { return int(n.nodeHeader.prefixLen) }

// node256 物理槽位即字节值，扫第一个/下一个非空字节。
func (n *node256) firstByte() byte {
	for bb, kid := range n.kids {
		if kid != nil {
			return byte(bb)
		}
	}
	return 0
}
func (n *node256) nextByte(after byte) (byte, bool) {
	for b := int(after) + 1; b < 256; b++ {
		if n.kids[b] != nil {
			return byte(b), true
		}
	}
	return 0, false
}
func (n *node256) lowerBoundByte(b byte) (byte, bool) {
	for c := int(b); c < 256; c++ {
		if n.kids[c] != nil {
			return byte(c), true
		}
	}
	return 0, false
}

// node4/node16 键数组按字节序存放，槽位顺序即遍历顺序。
func (n *node4) firstByte() byte { return n.keys[0] }
func (n *node4) nextByte(after byte) (byte, bool) {
	for i := 0; i < n.count; i++ {
		if n.keys[i] > after {
			return n.keys[i], true
		}
	}
	return 0, false
}
func (n *node16) firstByte() byte { return n.keys[0] }
func (n *node16) nextByte(after byte) (byte, bool) {
	for i := 0; i < n.count; i++ {
		if n.keys[i] > after {
			return n.keys[i], true
		}
	}
	return 0, false
}
func (n *node4) lowerBoundByte(b byte) (byte, bool) {
	for i := 0; i < n.count; i++ {
		if n.keys[i] >= b {
			return n.keys[i], true
		}
	}
	return 0, false
}
func (n *node16) lowerBoundByte(b byte) (byte, bool) {
	lo, hi := 0, n.count
	for lo < hi {
		mid := (lo + hi) / 2
		if n.keys[mid] < b {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < n.count {
		return n.keys[lo], true
	}
	return 0, false
}

// node48 遍历完全经 256 项下标表按字节值推进，与 kids[] 插入顺序无关。
func (n *node48) firstByte() byte {
	for b, idx := range n.keys {
		if idx != 0 {
			return byte(b)
		}
	}
	return 0
}
func (n *node48) nextByte(after byte) (byte, bool) {
	for b := int(after) + 1; b < 256; b++ {
		if n.keys[b] != 0 {
			return byte(b), true
		}
	}
	return 0, false
}
func (n *node48) lowerBoundByte(b byte) (byte, bool) {
	for c := int(b); c < 256; c++ {
		if n.keys[c] != 0 {
			return byte(c), true
		}
	}
	return 0, false
}

// findChild 各规格的查找入口。
func (n *node4) findChild(b byte) unsafe.Pointer {
	for i := 0; i < n.count; i++ {
		if n.keys[i] == b {
			return n.kids[i]
		}
	}
	return nil
}

// hasZeroByte：SWAR 技巧，判断一个 64 位字里是否含 0 字节。
func hasZeroByte(x uint64) bool {
	return (x-0x0101010101010101)&^x&0x8080808080808080 != 0
}

func (n *node16) findChild(b byte) unsafe.Pointer {
	// 并行比较：把 8 个槽的键与目标字节做异或，相等的位置变成 0，
	// 一次 hasZeroByte 同时检查 8 个位置（相当于 SIMD 的 PCMPEQB + PMOVMSKB）。
	broadcast := uint64(b) * 0x0101010101010101
	validMask := uint64(1<<(n.count&7) - 1)
	if n.count >= 8 {
		validMask = ^uint64(0)
	}
	eq := uint64(0)
	// 用安全的固定长度读取拿到槽位数组的 8 字节视图。
	first := *(*[8]byte)(unsafe.Pointer(&n.keys[0]))
	x := uint64(first[0]) | uint64(first[1])<<8 | uint64(first[2])<<16 |
		uint64(first[3])<<24 | uint64(first[4])<<32 | uint64(first[5])<<40 |
		uint64(first[6])<<48 | uint64(first[7])<<56
	eq = x ^ broadcast
	if hasZeroByte(eq & validMask) {
		for i := 0; i < min(8, n.count); i++ {
			if n.keys[i] == b {
				return n.kids[i]
			}
		}
	}
	if n.count > 8 {
		second := *(*[8]byte)(unsafe.Pointer(&n.keys[8]))
		y := uint64(second[0]) | uint64(second[1])<<8 | uint64(second[2])<<16 |
			uint64(second[3])<<24 | uint64(second[4])<<32 | uint64(second[5])<<40 |
			uint64(second[6])<<48 | uint64(second[7])<<56
		secondCount := n.count - 8
		secondMask := uint64(1<<secondCount - 1)
		if !hasZeroByte((y ^ broadcast) & secondMask) {
			return nil
		}
		for i := 8; i < n.count; i++ {
			if n.keys[i] == b {
				return n.kids[i]
			}
		}
	}
	return nil
}

func (n *node48) findChild(b byte) unsafe.Pointer {
	idx := n.keys[b] // 下标表：0=空，实际槽位 = idx-1
	if idx == 0 {
		return nil
	}
	return n.kids[idx-1]
}

func (n *node256) findChild(b byte) unsafe.Pointer {
	return n.kids[b] // 空槽本身就是 nil
}

// addChild 在指定字节上挂子节点；槽位满时就地升级节点，返回（可能是新的）节点。
// *slot 是该节点在父节点（或树根）中的指针槽，升级后需要写回。
func addChild(n node, b byte, child unsafe.Pointer, slot *unsafe.Pointer) node {
	switch x := n.(type) {
	case *node4:
		if x.count < 4 {
			// 保持键有序，整体右移。
			pos := 0
			for pos < x.count && x.keys[pos] < b {
				pos++
			}
			for i := x.count; i > pos; i-- {
				x.keys[i] = x.keys[i-1]
				x.kids[i] = x.kids[i-1]
			}
			x.keys[pos] = b
			x.kids[pos] = child
			x.count++
			return x
		}
		grown := grow16(x)
		*slot = unsafe.Pointer(grown)
		return addChild(grown, b, child, slot)

	case *node16:
		if x.count < 16 {
			pos := 0
			for pos < x.count && x.keys[pos] < b {
				pos++
			}
			for i := x.count; i > pos; i-- {
				x.keys[i] = x.keys[i-1]
				x.kids[i] = x.kids[i-1]
			}
			x.keys[pos] = b
			x.kids[pos] = child
			x.count++
			return x
		}
		grown := grow48(x)
		*slot = unsafe.Pointer(grown)
		return addChild(grown, b, child, slot)

	case *node48:
		if x.count < 48 {
			pos := x.count // 新槽位
			x.kids[pos] = child
			x.keys[b] = byte(pos + 1)
			x.count++
			return x
		}
		grown := grow256(x)
		*slot = unsafe.Pointer(grown)
		return addChild(grown, b, child, slot)

	case *node256:
		if x.kids[b] == nil {
			x.count++
		}
		x.kids[b] = child
		return x
	}
	panic("art: unknown node kind")
}

func grow16(old *node4) *node16 {
	n := &node16{nodeHeader: old.nodeHeader, count: old.count}
	n.nodeHeader.kind = kNode16
	for i := 0; i < old.count; i++ {
		n.keys[i] = old.keys[i]
		n.kids[i] = old.kids[i]
	}
	return n
}

func grow48(old *node16) *node48 {
	n := &node48{nodeHeader: old.nodeHeader, count: old.count}
	n.nodeHeader.kind = kNode48
	for i := 0; i < old.count; i++ {
		n.keys[old.keys[i]] = byte(i + 1)
		n.kids[i] = old.kids[i]
	}
	return n
}

func grow256(old *node48) *node256 {
	n := &node256{nodeHeader: old.nodeHeader, count: old.count}
	n.nodeHeader.kind = kNode256
	for b, idx := range old.keys {
		if idx != 0 {
			n.kids[b] = old.kids[idx-1]
		}
	}
	return n
}

// removeChild 摘掉一个子节点；低于阈值时立即降级（题目第 7 条），
// 降级后的节点写回 *slot。
func removeChild(n node, b byte, slot *unsafe.Pointer) node {
	switch x := n.(type) {
	case *node4:
		for i := 0; i < x.count; i++ {
			if x.keys[i] == b {
				for j := i; j < x.count-1; j++ {
					x.keys[j] = x.keys[j+1]
					x.kids[j] = x.kids[j+1]
				}
				x.count--
				x.keys[x.count] = 0
				x.kids[x.count] = nil
			}
		}
		return x // node4 不向更小的内部节点降级，剩 1 个时由删除流程整体塌缩

	case *node16:
		for i := 0; i < x.count; i++ {
			if x.keys[i] == b {
				for j := i; j < x.count-1; j++ {
					x.keys[j] = x.keys[j+1]
					x.kids[j] = x.kids[j+1]
				}
				x.count--
				x.keys[x.count] = 0
				x.kids[x.count] = nil
			}
		}
		if x.count == 3 { // 留 1 的余量，避免在阈值上反复升降抖动
			shrunk := shrink4(x)
			*slot = unsafe.Pointer(shrunk)
			return shrunk
		}
		return x

	case *node48:
		idx := x.keys[b]
		if idx == 0 {
			return x
		}
		pos := int(idx - 1)
		x.kids[pos] = nil
		x.keys[b] = 0
		x.count--
		// 末尾槽挪到被删槽，更新下标表
		if pos < x.count {
			last := x.kids[x.count]
			x.kids[pos] = last
			for bb, ii := range x.keys {
				if int(ii) == x.count+1 {
					x.keys[bb] = byte(pos + 1)
					break
				}
			}
			x.kids[x.count] = nil
		}
		if x.count == 12 {
			shrunk := shrink16(x)
			*slot = unsafe.Pointer(shrunk)
			return shrunk
		}
		return x

	case *node256:
		if x.kids[b] != nil {
			x.kids[b] = nil
			x.count--
		}
		if x.count == 36 {
			shrunk := shrink48(x)
			*slot = unsafe.Pointer(shrunk)
			return shrunk
		}
		return x
	}
	panic("art: unknown node kind")
}

func shrink4(old *node16) *node4 {
	n := &node4{nodeHeader: old.nodeHeader, count: old.count}
	n.nodeHeader.kind = kNode4
	for i := 0; i < old.count; i++ {
		n.keys[i] = old.keys[i]
		n.kids[i] = old.kids[i]
	}
	return n
}

func shrink16(old *node48) *node16 {
	n := &node16{nodeHeader: old.nodeHeader, count: old.count}
	n.nodeHeader.kind = kNode16
	i := 0
	for b, idx := range old.keys {
		if idx != 0 {
			n.keys[i] = byte(b)
			n.kids[i] = old.kids[idx-1]
			i++
		}
	}
	return n
}

func shrink48(old *node256) *node48 {
	n := &node48{nodeHeader: old.nodeHeader, count: old.count}
	n.nodeHeader.kind = kNode48
	pos := 0
	for b, kid := range old.kids {
		if kid != nil {
			n.keys[b] = byte(pos + 1)
			n.kids[pos] = kid
			pos++
		}
	}
	return n
}

// onlyChild 返回唯一子节点及其分支字节；调用方保证 count==1。
func onlyChild(n node) (byte, unsafe.Pointer) {
	switch x := n.(type) {
	case *node4:
		return x.keys[0], x.kids[0]
	case *node16:
		return x.keys[0], x.kids[0]
	case *node48:
		for b, idx := range x.keys {
			if idx != 0 {
				return byte(b), x.kids[idx-1]
			}
		}
	case *node256:
		for b, kid := range x.kids {
			if kid != nil {
				return byte(b), kid
			}
		}
	}
	panic("art: not an only child")
}
