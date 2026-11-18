package art

import (
	"bytes"
	"unsafe"
)

// Tree 自适应基数树。零值不可用，请用 New 构造。
type Tree struct {
	root        unsafe.Pointer // 带标记的叶子指针或内部节点指针
	size        int
	keyBytes    int // 全部叶子的“原始键”字节总数（对账口径）
	encKeyBytes int // 全部叶子实际存储的编码键字节总数
	valBytes    int // 全部值字节总数
}

func New() *Tree { return &Tree{} }

func (t *Tree) Len() int { return t.size }

// headerOf 取内部节点的公共头部。
func headerOf(n node) *nodeHeader {
	switch x := n.(type) {
	case *node4:
		return &x.nodeHeader
	case *node16:
		return &x.nodeHeader
	case *node48:
		return &x.nodeHeader
	case *node256:
		return &x.nodeHeader
	}
	return nil
}

// minLeaf 沿最小分支字节一路向下取最小叶子——长压缩前缀的“事实来源”。
// 用 firstByte + findChild，避免 node48/node256 物理 0 槽为空的问题。
func (t *Tree) minLeaf(n node) *leaf {
	for {
		p := n.findChild(n.firstByte())
		if isLeaf(p) {
			return asLeaf(p)
		}
		n = asInner(p)
	}
}

// childSlot 返回某分支字节对应的指针槽地址，供递归插入/删除就地改写。
func childSlot(n node, b byte) *unsafe.Pointer {
	switch x := n.(type) {
	case *node4:
		for i := 0; i < x.count; i++ {
			if x.keys[i] == b {
				return &x.kids[i]
			}
		}
	case *node16:
		for i := 0; i < x.count; i++ {
			if x.keys[i] == b {
				return &x.kids[i]
			}
		}
	case *node48:
		if idx := x.keys[b]; idx != 0 {
			return &x.kids[idx-1]
		}
	case *node256:
		return &x.kids[b]
	}
	return nil
}

// lcpLen 计算两个键从 depth 起的最长公共前缀长度。
// 编码键自带结束符，两个不同键必然在某字节处分叉，不会越界。
func lcpLen(a, b []byte, depth int) int {
	i := depth
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i - depth
}

// prefixMismatch 返回存储前缀与 key[depth:] 第一个不一致的下标；
// 完全一致返回 -1。长前缀超出内联容量的部分去最小叶子的完整键里核对。
func (t *Tree) prefixMismatch(n node, key []byte, depth int) int {
	h := headerOf(n)
	plen := int(h.prefixLen)
	idx := 0
	for ; idx < plen && idx < maxPrefix; idx++ {
		if depth+idx >= len(key) || h.prefix[idx] != key[depth+idx] {
			return idx
		}
	}
	if plen > maxPrefix {
		ml := t.minLeaf(n)
		for ; idx < plen; idx++ {
			if depth+idx >= len(key) || depth+idx >= len(ml.key) ||
				ml.key[depth+idx] != key[depth+idx] {
				return idx
			}
		}
	}
	return -1
}

// Insert 插入或覆盖。返回被覆盖的旧值；新插入时 replaced=false。
func (t *Tree) Insert(key, val []byte) (old []byte, replaced bool) {
	enc := encodeKey(key)
	nv := append([]byte(nil), val...)
	l := &leaf{key: enc, val: nv}

	if t.root == nil {
		t.root = tagLeaf(l) // 懒展开：整棵树只有一个键时直接挂叶子
		t.size = 1
		t.keyBytes += len(key)
		t.encKeyBytes += len(enc)
		t.valBytes += len(nv)
		return nil, false
	}

	replacedLeaf := t.recursiveInsert(&t.root, 0, l)
	if replacedLeaf != nil {
		t.valBytes += len(nv) - len(replacedLeaf.val)
		return replacedLeaf.val, true
	}
	t.size++
	t.keyBytes += len(key)
	t.encKeyBytes += len(enc)
	t.valBytes += len(nv)
	return nil, false
}

// recursiveInsert 返回被替换的旧叶子；新插入返回 nil。
func (t *Tree) recursiveInsert(slot *unsafe.Pointer, depth int, l *leaf) *leaf {
	p := *slot

	// 1) 槽位上已是叶子：同键替换，否则懒展开成 node4。
	if isLeaf(p) {
		cur := asLeaf(p)
		if bytes.Equal(cur.key, l.key) {
			*slot = tagLeaf(l)
			return cur
		}
		// 懒展开：公共字节收进压缩前缀，只在分叉字节处分两个孩子。
		lcp := lcpLen(cur.key, l.key, depth)
		n4 := &node4{nodeHeader: nodeHeader{kind: kNode4}, count: 2}
		n4.nodeHeader.prefixLen = byte(lcp)
		copy(n4.nodeHeader.prefix[:], cur.key[depth:depth+min(lcp, maxPrefix)])
		b0, b1 := cur.key[depth+lcp], l.key[depth+lcp]
		n4.keys[0], n4.kids[0] = b0, p
		n4.keys[1], n4.kids[1] = b1, tagLeaf(l)
		if b0 > b1 {
			n4.keys[0], n4.keys[1] = n4.keys[1], n4.keys[0]
			n4.kids[0], n4.kids[1] = n4.kids[1], n4.kids[0]
		}
		*slot = unsafe.Pointer(n4)
		return nil
	}

	// 2) 内部节点：先过压缩前缀。
	n := asInner(p)
	plen := n.prefixLen()
	if plen != 0 {
		mismatch := t.prefixMismatch(n, l.key, depth)
		if mismatch >= 0 {
			// 前缀中途分叉：拆出一个新 node4，原节点保留为其一个孩子。
			ml := t.minLeaf(n)
			h := headerOf(n)
			oldPlen := int(h.prefixLen)

			new4 := &node4{nodeHeader: nodeHeader{kind: kNode4}, count: 2}
			new4.nodeHeader.prefixLen = byte(mismatch)
			if mismatch > 0 {
				cp := min(mismatch, maxPrefix)
				copy(new4.prefix[:cp], ml.key[depth:depth+cp])
			}

			// 原节点的新前缀 = 分叉字节之后的剩余部分，字节来源是最小叶子的完整键。
			restLen := oldPlen - mismatch - 1
			h.prefixLen = byte(restLen)
			for i := range h.prefix {
				h.prefix[i] = 0
			}
			if restLen > 0 {
				src := depth + mismatch + 1
				copy(h.prefix[:min(restLen, maxPrefix)], ml.key[src:src+min(restLen, maxPrefix)])
			}

			bOld, bNew := ml.key[depth+mismatch], l.key[depth+mismatch]
			if bOld < bNew {
				new4.keys[0], new4.kids[0] = bOld, p
				new4.keys[1], new4.kids[1] = bNew, tagLeaf(l)
			} else {
				new4.keys[0], new4.kids[0] = bNew, tagLeaf(l)
				new4.keys[1], new4.kids[1] = bOld, p
			}
			*slot = unsafe.Pointer(new4)
			return nil
		}
		depth += plen
	}

	// 3) 前缀匹配，按分支字节继续。
	b := l.key[depth]
	kid := n.findChild(b)
	if kid == nil {
		addChild(n, b, tagLeaf(l), slot)
		return nil
	}
	return t.recursiveInsert(childSlot(n, b), depth+1, l)
}

// Get 精确查找。
func (t *Tree) Get(key []byte) ([]byte, bool) {
	if t.root == nil {
		return nil, false
	}
	enc := encodeKey(key)
	p := t.root
	depth := 0
	for {
		if isLeaf(p) {
			l := asLeaf(p)
			if bytes.Equal(l.key, enc) {
				return l.val, true
			}
			return nil, false
		}
		n := asInner(p)
		if plen := n.prefixLen(); plen != 0 {
			if t.prefixMismatch(n, enc, depth) >= 0 {
				return nil, false
			}
			depth += plen
		}
		if depth >= len(enc) {
			return nil, false
		}
		p = n.findChild(enc[depth])
		if p == nil {
			return nil, false
		}
		depth++
	}
}

// Delete 删除一个键，返回被删的值；键不存在时 ok=false。
func (t *Tree) Delete(key []byte) (val []byte, ok bool) {
	if t.root == nil {
		return nil, false
	}
	enc := encodeKey(key)
	removed := t.recursiveDelete(&t.root, 0, enc)
	if removed == nil {
		return nil, false
	}
	t.size--
	t.keyBytes -= len(key)
	t.encKeyBytes -= len(enc)
	t.valBytes -= len(removed.val)
	if t.size == 0 {
		t.root = nil
	}
	return removed.val, true
}

func (t *Tree) recursiveDelete(slot *unsafe.Pointer, depth int, enc []byte) *leaf {
	p := *slot

	if isLeaf(p) {
		l := asLeaf(p)
		if bytes.Equal(l.key, enc) {
			return l
		}
		return nil
	}

	n := asInner(p)
	if plen := n.prefixLen(); plen != 0 {
		if t.prefixMismatch(n, enc, depth) >= 0 {
			return nil
		}
		depth += plen
	}

	b := enc[depth]
	kid := n.findChild(b)
	if kid == nil {
		return nil
	}

	var removed *leaf
	if isLeaf(kid) {
		l := asLeaf(kid)
		if !bytes.Equal(l.key, enc) {
			return nil
		}
		removed = l
	} else {
		// 子节点是内部节点：递归删除。内部节点只会降级/塌缩，不会变空，
		// 因此本节点保留这个槽，不能摘分支。
		removed = t.recursiveDelete(childSlot(n, b), depth+1, enc)
		if removed == nil {
			return nil
		}
		return removed
	}

	// 直接孩子就是被删叶子：摘掉子节点，低于阈值的节点就地降级。
	n = removeChild(n, b, slot)

	// 只剩一条路：并回压缩路径（含直接塌缩成叶子的懒展开逆操作）。
	if n.cnt() == 1 {
		t.collapse(n, slot, depth)
	}
	return removed
}

// collapse 把只剩一个孩子的内部节点并回压缩路径（删除后路径压缩的逆展开）。
// depth 是唯一分支字节在叶子完整键中的下标（本节点压缩前缀之后）。
func (t *Tree) collapse(n node, slot *unsafe.Pointer, depth int) {
	_, only := onlyChild(n)
	if isLeaf(only) {
		// 唯一孩子就是叶子：整个节点退化成叶子，完整路径本来就保存在叶子键里。
		*slot = only
		return
	}
	inner := asInner(only)
	h := headerOf(inner)
	ml := t.minLeaf(inner)
	// 新前缀 = 本节点前缀 + 唯一分支字节 + 原子树前缀；
	// 以最小叶子的完整键为事实来源重建，长前缀也不会丢字节。
	newLen := n.prefixLen() + 1 + int(h.prefixLen)
	start := depth - n.prefixLen()
	h.prefixLen = byte(newLen)
	for i := range h.prefix {
		h.prefix[i] = 0
	}
	copy(h.prefix[:min(newLen, maxPrefix)], ml.key[start:start+min(newLen, maxPrefix)])
	*slot = only
}
