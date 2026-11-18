package art

// leaf 叶子节点：保存树内编码键与值。
// 值在 Insert 时被复制，树不引用调用方的底层数组。
type leaf struct {
	key []byte
	val []byte
}
