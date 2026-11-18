// Package ref 用排好序的切片实现同样的键值操作，作为 ART 的行为基准：
// 压力测试里每一步 ART 的结果都必须和它一致。
//
// 注意它的插入/删除是 O(n) 搬移，只适合严格对账阶段（几万级）；
// 百万级阶段改用 map 做点查基准 + 排序快照做顺序基准。
package ref

import (
	"bytes"
	"sort"
)

type Entry struct {
	Key []byte
	Val []byte
}

type SortedSlice struct {
	entries []Entry
}

func New() *SortedSlice { return &SortedSlice{} }

func (s *SortedSlice) Len() int { return len(s.entries) }

// index 返回 key 的位置；不存在时返回应插入的下标和 false。
func (s *SortedSlice) index(key []byte) (int, bool) {
	i := sort.Search(len(s.entries), func(i int) bool {
		return bytes.Compare(s.entries[i].Key, key) >= 0
	})
	if i < len(s.entries) && bytes.Equal(s.entries[i].Key, key) {
		return i, true
	}
	return i, false
}

func (s *SortedSlice) Insert(key, val []byte) (old []byte, replaced bool) {
	i, found := s.index(key)
	k := append([]byte(nil), key...)
	v := append([]byte(nil), val...)
	if found {
		old = s.entries[i].Val
		s.entries[i].Val = v
		return old, true
	}
	s.entries = append(s.entries, Entry{})
	copy(s.entries[i+1:], s.entries[i:])
	s.entries[i] = Entry{Key: k, Val: v}
	return nil, false
}

func (s *SortedSlice) Get(key []byte) ([]byte, bool) {
	i, found := s.index(key)
	if !found {
		return nil, false
	}
	return s.entries[i].Val, true
}

func (s *SortedSlice) Delete(key []byte) (val []byte, ok bool) {
	i, found := s.index(key)
	if !found {
		return nil, false
	}
	v := s.entries[i].Val
	s.entries = append(s.entries[:i], s.entries[i+1:]...)
	return v, true
}

// All 返回按字典序排列的全部 (key,val)。
func (s *SortedSlice) All() []Entry { return s.entries }

// Prefix 按前缀取有序结果。
func (s *SortedSlice) Prefix(prefix []byte) []Entry {
	lo, _ := s.index(prefix)
	var out []Entry
	for i := lo; i < len(s.entries); i++ {
		if !bytes.HasPrefix(s.entries[i].Key, prefix) {
			break
		}
		out = append(out, s.entries[i])
	}
	return out
}

// Bytes 估算参考实现的占用：切片头 + 每个条目头（两个切片头）+ 键值负载。
func (s *SortedSlice) Bytes() int64 {
	var n int64 = 24 * int64(cap(s.entries))
	for _, e := range s.entries {
		n += 48
		n += int64(cap(e.Key)) + int64(cap(e.Val))
	}
	return n
}
