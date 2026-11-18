package art

import (
	"bytes"
	"unsafe"
)

// 有序迭代器：沿最左路径入栈、逐槽推进，输出顺序即无符号字节字典序。
//
//	it := t.Iterator()
//	for it.First(); it.Valid(); it.Next() {
//	    key, val := it.Key(), it.Value()
//	}

// frame 遍历帧：b 是当前所在分支的字节值，按字节序推进，
// 与任何节点规格的物理槽位排列无关。
type frame struct {
	n node
	b byte
}

type Iterator struct {
	t     *Tree
	stack []frame
	leaf  *leaf
}

func (t *Tree) Iterator() *Iterator { return &Iterator{t: t} }

func (it *Iterator) Valid() bool { return it.leaf != nil }

// Key 返回还原后的用户键（每次调用会产生一次分配，遍历对账足够用）。
func (it *Iterator) Key() []byte { return decodeKey(it.leaf.key) }

func (it *Iterator) Value() []byte { return it.leaf.val }

// pushLeft 从 p 开始一路压入最左（最小字节）路径，落到叶子为止。
func (it *Iterator) pushLeft(p unsafe.Pointer) {
	for !isLeaf(p) {
		n := asInner(p)
		b := n.firstByte()
		it.stack = append(it.stack, frame{n: n, b: b})
		p = n.findChild(b)
	}
	it.leaf = asLeaf(p)
}

// First 定位到整棵树最小的键。
func (it *Iterator) First() {
	it.stack = it.stack[:0]
	it.leaf = nil
	if it.t.root == nil {
		return
	}
	it.pushLeft(it.t.root)
}

// Next 推进到下一个键；越过末尾后 Valid() 变 false。
// 统一按分支字节值升序，node48 经下标表、node256 直接扫字节，
// 都不会受 kids[] 物理插入顺序影响。
func (it *Iterator) Next() {
	for len(it.stack) > 0 {
		top := len(it.stack) - 1
		f := &it.stack[top]
		if nb, ok := f.n.nextByte(f.b); ok {
			f.b = nb
			it.pushLeft(f.n.findChild(nb))
			return
		}
		it.stack = it.stack[:top]
	}
	it.leaf = nil
}

// Seek 定位到第一个 >= target 的键（按用户键字典序）。
//
// 全程使用分支字节而非物理槽下标：精确下行时 lowerBoundByte 取字节，
// 回退时 nextByte 推进到下一个兄弟字节，findChild 取孩子。
// 下行过程同步维护 it.stack，命中叶子后 Next() 可直接继续遍历。
func (it *Iterator) Seek(target []byte) {
	it.stack = it.stack[:0]
	it.leaf = nil
	if it.t.root == nil {
		return
	}
	enc := encodeKey(target)
	p := it.t.root
	depth := 0

	for {
		if isLeaf(p) {
			l := asLeaf(p)
			if compareForSeek(l.key, enc) >= 0 {
				it.leaf = l
				return
			}
			it.seekFallback(enc)
			return
		}

		n := asInner(p)
		switch innerCmpTarget(n, depth, enc) {
		case 1:
			// 子树整体大于目标：取子树最小叶子，并把经过的节点压栈。
			it.pushLeft(p)
			return
		case -1:
			it.seekFallback(enc)
			return
		}

		depth += n.prefixLen()
		if depth >= len(enc) {
			it.pushLeft(p)
			return
		}
		b, ok := n.lowerBoundByte(enc[depth])
		if !ok {
			it.seekFallback(enc)
			return
		}
		if b != enc[depth] {
			// 严格大于目标字节的第一个分支：下界即该子树最小键。
			// 压入当前节点帧（分支字节 b），再走最左路径到最小叶子。
			it.stack = append(it.stack, frame{n: n, b: b})
			kid := n.findChild(b)
			if isLeaf(kid) {
				it.leaf = asLeaf(kid)
			} else {
				it.pushLeft(kid)
			}
			return
		}
		// 精确命中：压栈后继续下行校验后续字节。
		it.stack = append(it.stack, frame{n: n, b: b})
		p = n.findChild(b)
		depth++
	}
}

// seekFallback 回退裁决：从 it.stack 栈顶开始，用 nextByte 逐字节推进
// 兄弟分支，每个候选取其最小叶子的完整编码键裁决，第一个够格者即答案。
// 命中时把候选子树的最左路径补齐到栈中，保证后续 Next() 可继续。
func (it *Iterator) seekFallback(enc []byte) {
	for len(it.stack) > 0 {
		top := len(it.stack) - 1
		f := &it.stack[top]
		nb, ok := f.n.nextByte(f.b)
		if !ok {
			it.stack = it.stack[:top]
			continue
		}
		f.b = nb
		cand := f.n.findChild(nb)
		var l *leaf
		if isLeaf(cand) {
			l = asLeaf(cand)
		} else {
			l = tMinLeaf(asInner(cand))
		}
		if compareForSeek(l.key, enc) >= 0 {
			it.leaf = l
			// 候选若是内部节点，补齐其最左路径帧，使 Next 能继续。
			if !isLeaf(cand) {
				it.pushLeft(cand)
			}
			return
		}
	}
}

// compareForSeek 下界语义比较：
//
//	enc 是 leafKey 的严格前缀 -> 返回 0（该叶子就是 enc 的下界键）
//	否则按无符号字节字典序比较。
//
// 编码键以 (0x00,0x00) 收尾，普通字节比较在“目标更短”时会得到反号。
func compareForSeek(leafKey, enc []byte) int {
	c := bytes.Compare(leafKey, enc)
	if c > 0 && bytes.HasPrefix(leafKey, enc) {
		return 0
	}
	return c
}

// innerCmpTarget 比较内部节点整棵子树的“最小键”与目标：
//
//	<0 子树所有键都小于目标（应跳过整棵子树）
//	=0 目标与子树公共前缀一致，可继续下行
//	>0 子树最小键已大于目标（下界即子树最小键）
//
// 节点压缩前缀与目标逐字节比较即可代表整棵子树；
// 长前缀超出内联容量的部分从最小叶子的完整键取。
func innerCmpTarget(n node, depth int, enc []byte) int {
	plen := n.prefixLen()
	if plen == 0 {
		return 0
	}
	ml := tMinLeaf(n)
	for i := 0; i < plen; i++ {
		pos := depth + i
		var kb byte
		if pos < len(ml.key) {
			kb = ml.key[pos]
		}
		var tb byte
		if pos < len(enc) {
			tb = enc[pos]
		}
		if kb != tb {
			if kb < tb {
				return -1
			}
			return 1
		}
	}
	return 0
}

// PrefixScan 按前缀做范围遍历，按字典序回调；回调返回 false 可提前终止。
// 前缀按原始用户键匹配，内部自动做 0x00 转义（不加结束符）。
//
// 三种命中终止位置都要处理：
//  1. 前缀在节点压缩前缀中途分叉 -> 整棵子树无命中；
//  2. 前缀在压缩前缀区间内耗尽 -> 整棵子树都在前缀下，有序遍历即可；
//  3. 前缀跨过压缩前缀，在分支字节处耗尽或继续精确下行。
func (t *Tree) PrefixScan(prefix []byte, fn func(key, val []byte) bool) {
	if t.root == nil {
		return
	}
	penc := encodePrefix(prefix)
	p := t.root
	depth := 0

	for !isLeaf(p) {
		n := asInner(p)
		plen := n.prefixLen()
		if plen > 0 {
			ml := tMinLeaf(n)
			i := depth
			limit := depth + plen
			for ; i < limit; i++ {
				if i >= len(penc) {
					// 情形 2：前缀在压缩前缀区间耗尽，整棵子树都在范围内。
					it := t.Iterator()
					it.stack = append(it.stack, frame{n: n, b: n.firstByte()})
					it.descendToLeaf()
					for it.Valid() {
						if !fn(decodeKey(it.leaf.key), it.leaf.val) {
							return
						}
						it.Next()
					}
					return
				}
				var kb byte
				if i < len(ml.key) {
					kb = ml.key[i]
				}
				if kb != penc[i] {
					return // 情形 1：分叉，无命中
				}
			}
			depth = limit
		}
		if depth >= len(penc) {
			// 前缀恰好在压缩前缀末尾耗尽：整棵子树命中。
			it := t.Iterator()
			it.stack = append(it.stack, frame{n: n, b: n.firstByte()})
			it.descendToLeaf()
			for it.Valid() {
				if !fn(decodeKey(it.leaf.key), it.leaf.val) {
					return
				}
				it.Next()
			}
			return
		}
		kid := n.findChild(penc[depth])
		if kid == nil {
			return
		}
		p = kid
		depth++
	}

	// 落到叶子：单键直接判断（目标是该叶子键的前缀才算命中）。
	l := asLeaf(p)
	k := decodeKey(l.key)
	if hasPrefix(k, prefix) {
		fn(k, l.val)
	}
}

// descendToLeaf 从栈顶帧的当前分支字节一路走最左路径落到叶子。
func (it *Iterator) descendToLeaf() {
	for len(it.stack) > 0 {
		top := len(it.stack) - 1
		f := &it.stack[top]
		p := f.n.findChild(f.b)
		if isLeaf(p) {
			it.leaf = asLeaf(p)
			return
		}
		child := asInner(p)
		it.stack = append(it.stack, frame{n: child, b: child.firstByte()})
	}
}

// Each 全量有序遍历。
func (t *Tree) Each(fn func(key, val []byte) bool) {
	it := t.Iterator()
	for it.First(); it.Valid(); it.Next() {
		if !fn(decodeKey(it.leaf.key), it.leaf.val) {
			return
		}
	}
}

// tMinLeaf 沿最小分支字节一路向下取最小叶子。
// 用 firstByte + findChild 而非物理 0 槽，node48/node256 的 0 槽可能为空。
func tMinLeaf(n node) *leaf {
	for {
		p := n.findChild(n.firstByte())
		if isLeaf(p) {
			return asLeaf(p)
		}
		n = asInner(p)
	}
}

func hasPrefix(s, prefix []byte) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}
