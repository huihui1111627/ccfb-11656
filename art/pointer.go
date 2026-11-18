package art

import "unsafe"

// 指针标记（题目第 6 条）：
//
// 子节点指针与叶子共用同一个槽位，靠指针最低位区分，不额外占用空间：
//
//	最低位 = 1 -> 叶子 (*leaf)
//	最低位 = 0 -> 内部节点 (*node4/*node16/*node48/*node256) 或 nil
//
// Go 堆分配的对象至少按 2 字节对齐，真实指针最低位恒为 0，标记可随时清除。
// 槽位始终以 unsafe.Pointer 类型保存（绝不保存 uintptr），GC 仍能识别：
// 标记后的地址 = 对象基址+1，落在叶子对象内部，GC 的 interior pointer
// 机制照样能让叶子保活。这与社区主流 Go ART 实现（plar/go-art）的做法一致。

// tagLeaf 给叶子指针打上最低位标记。
func tagLeaf(l *leaf) unsafe.Pointer {
	return unsafe.Pointer(uintptr(unsafe.Pointer(l)) | 1)
}

// isLeaf 判断槽位里挂的是不是叶子。
func isLeaf(p unsafe.Pointer) bool { return uintptr(p)&1 == 1 }

// asLeaf 清除标记并还原成叶子指针。
func asLeaf(p unsafe.Pointer) *leaf {
	return (*leaf)(unsafe.Pointer(uintptr(p) &^ 1))
}

// asInner 把无标记指针还原成具体规格的内部节点。
func asInner(p unsafe.Pointer) node {
	// 所有节点结构第一个字段都是 nodeHeader，可直接读 kind。
	h := (*nodeHeader)(p)
	switch h.kind {
	case kNode4:
		return (*node4)(p)
	case kNode16:
		return (*node16)(p)
	case kNode48:
		return (*node48)(p)
	default:
		return (*node256)(p)
	}
}
