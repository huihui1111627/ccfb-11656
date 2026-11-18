package art

import (
	"bytes"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"testing"

	"museumart/ref"
)

func dumpSorted(t *Tree) [][]byte {
	var out [][]byte
	t.Each(func(k, v []byte) bool {
		out = append(out, append([]byte(nil), k...))
		return true
	})
	return out
}

func TestBasic(t *testing.T) {
	tr := New()
	if _, ok := tr.Get([]byte("a")); ok {
		t.Fatal("empty tree hit")
	}
	tr.Insert([]byte("abc"), []byte("v1"))
	tr.Insert([]byte("abd"), []byte("v2"))
	tr.Insert([]byte("ab"), []byte("v3")) // 前缀关系：ab 是 abc 的前缀

	if v, ok := tr.Get([]byte("ab")); !ok || string(v) != "v3" {
		t.Fatalf("get ab = %q,%v", v, ok)
	}
	if v, ok := tr.Get([]byte("abc")); !ok || string(v) != "v1" {
		t.Fatalf("get abc = %q,%v", v, ok)
	}
	if v, ok := tr.Get([]byte("abd")); !ok || string(v) != "v2" {
		t.Fatalf("get abd = %q,%v", v, ok)
	}
	if _, ok := tr.Get([]byte("abe")); ok {
		t.Fatal("missing key should not hit")
	}
	if tr.Len() != 3 {
		t.Fatalf("len = %d", tr.Len())
	}

	got := dumpSorted(tr)
	want := [][]byte{[]byte("ab"), []byte("abc"), []byte("abd")}
	if len(got) != len(want) {
		t.Fatalf("order len %d", len(got))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("order[%d]=%q want %q", i, got[i], want[i])
		}
	}

	if old, rep := tr.Insert([]byte("abc"), []byte("v9")); !rep || string(old) != "v1" {
		t.Fatalf("replace wrong: %q %v", old, rep)
	}
	if v, _ := tr.Get([]byte("abc")); string(v) != "v9" {
		t.Fatalf("after replace %q", v)
	}
	if tr.Len() != 3 {
		t.Fatal("replace changed len")
	}

	if _, ok := tr.Delete([]byte("nope")); ok {
		t.Fatal("delete missing hit")
	}
	if _, ok := tr.Delete([]byte("abc")); !ok {
		t.Fatal("delete failed")
	}
	if _, ok := tr.Get([]byte("abc")); ok {
		t.Fatal("deleted key still found")
	}
	if tr.Len() != 2 {
		t.Fatalf("len after delete = %d", tr.Len())
	}
}

func TestLazyExpansionAndCollapse(t *testing.T) {
	tr := New()
	tr.Insert([]byte("only"), []byte("1"))
	if !isLeaf(tr.root) {
		t.Fatal("single-key tree must keep root as a leaf (lazy expansion)")
	}
	tr.Insert([]byte("onward"), []byte("2"))
	if isLeaf(tr.root) {
		t.Fatal("two keys should expand root")
	}
	tr.Delete([]byte("onward"))
	if !isLeaf(tr.root) {
		t.Fatal("back to one key must collapse to a leaf")
	}
	tr.Delete([]byte("only"))
	if tr.root != nil || tr.Len() != 0 {
		t.Fatal("tree not empty after deleting last key")
	}
}

func TestNodeGrowShrink(t *testing.T) {
	tr := New()
	// 插入 256 个仅末字节不同的键，强制根节点一路升级到 node256。
	for i := 0; i < 256; i++ {
		tr.Insert([]byte{0xAA, byte(i)}, []byte{byte(i)})
	}
	st := tr.Collect()
	if st.ByKind[kNode256].Count != 1 {
		t.Fatalf("want 1 node256, stats=%+v", st.ByKind)
	}
	for i := 0; i < 256; i++ {
		if v, ok := tr.Get([]byte{0xAA, byte(i)}); !ok || v[0] != byte(i) {
			t.Fatalf("key %d lost after grow", i)
		}
	}
	// 删到 36 以下应降级 node48，再删到 12 以下降 node16，再删到 3 以下降 node4。
	for i := 0; i < 220; i++ {
		tr.Delete([]byte{0xAA, byte(i)})
	}
	st = tr.Collect()
	if st.ByKind[kNode256].Count != 0 || st.ByKind[kNode48].Count != 1 {
		t.Fatalf("want node48, got %+v", st.ByKind)
	}
	for i := 220; i < 244; i++ {
		tr.Delete([]byte{0xAA, byte(i)})
	}
	st = tr.Collect()
	if st.ByKind[kNode48].Count != 0 || st.ByKind[kNode16].Count != 1 {
		t.Fatalf("want node16, got %+v", st.ByKind)
	}
	for i := 244; i < 253; i++ {
		tr.Delete([]byte{0xAA, byte(i)})
	}
	st = tr.Collect()
	if st.ByKind[kNode16].Count != 0 || st.ByKind[kNode4].Count != 1 {
		t.Fatalf("want node4, got %+v", st.ByKind)
	}
	// 剩 3 个键全部可读
	for i := 253; i < 256; i++ {
		if _, ok := tr.Get([]byte{0xAA, byte(i)}); !ok {
			t.Fatalf("key %d lost after shrink", i)
		}
	}
}

// TestRandomAgainstRef 随机混合操作，每一步都与有序切片参考实现对账。
func TestRandomAgainstRef(t *testing.T) {
	const seed = 42
	const initial = 4000
	const ops = 30000

	rng := rand.New(rand.NewSource(seed))
	tr := New()
	rf := ref.New()
	allKeys := make([][]byte, 0, initial*2)

	mkKey := func() []byte {
		switch rng.Intn(4) {
		case 0:
			return []byte(fmt.Sprintf("NHM-PB-%08d", rng.Intn(initial*3)))
		case 1:
			team := []string{"XZ", "YN", "SC"}[rng.Intn(3)]
			return []byte(fmt.Sprintf("%s%d-%06d", team, 1980+rng.Intn(40), rng.Intn(initial*2)))
		case 2:
			g := []string{"Quercus", "Rosa", "Pinus", "Acer"}[rng.Intn(4)]
			return []byte(fmt.Sprintf("%s %s#%05d", g, "sp", rng.Intn(initial*3)))
		default:
			// 故意制造大量公共前缀与包含零字节的键，考验转义和路径压缩
			b := make([]byte, 1+rng.Intn(24))
			for i := range b {
				b[i] = byte(rng.Intn(4)) // 含大量 0x00
			}
			return b
		}
	}

	check := func(k []byte) {
		rv, rok := rf.Get(k)
		tv, tok := tr.Get(k)
		if rok != tok || (rok && !bytes.Equal(rv, tv)) {
			t.Fatalf("diverge on key % x: ref(%q,%v) art(%q,%v)", k, rv, rok, tv, tok)
		}
	}

	for i := 0; i < initial; i++ {
		k := mkKey()
		v := []byte(fmt.Sprintf("val-%d", i))
		rold, rrep := rf.Insert(k, v)
		told, trep := tr.Insert(k, v)
		if rrep != trep || (rrep && !bytes.Equal(rold, told)) {
			t.Fatalf("insert diverge key=% x", k)
		}
		if !rrep {
			allKeys = append(allKeys, append([]byte(nil), k...))
		}
	}

	for i := 0; i < ops; i++ {
		switch rng.Intn(10) {
		case 0, 1, 2, 3: // 40% 查找
			if len(allKeys) > 0 {
				check(allKeys[rng.Intn(len(allKeys))])
			}
			check(mkKey())
		case 4, 5, 6, 7: // 40% 插入
			k := mkKey()
			v := []byte(fmt.Sprintf("v-%d-%d", i, rng.Intn(999)))
			rold, rrep := rf.Insert(k, v)
			told, trep := tr.Insert(k, v)
			if rrep != trep || (rrep && !bytes.Equal(rold, told)) {
				t.Fatalf("insert diverge key=% x", k)
			}
			if !rrep {
				allKeys = append(allKeys, append([]byte(nil), k...))
			}
		default: // 20% 删除
			if len(allKeys) == 0 {
				continue
			}
			idx := rng.Intn(len(allKeys))
			k := allKeys[idx]
			rv, rok := rf.Delete(k)
			tv, tok := tr.Delete(k)
			if rok != tok || (rok && !bytes.Equal(rv, tv)) {
				t.Fatalf("delete diverge key=% x", k)
			}
			if rok {
				allKeys[idx] = allKeys[len(allKeys)-1]
				allKeys = allKeys[:len(allKeys)-1]
			}
		}
		if tr.Len() != rf.Len() {
			t.Fatalf("len diverge at op %d: art=%d ref=%d", i, tr.Len(), rf.Len())
		}
	}

	// 全量顺序对账
	refAll := rf.All()
	got := dumpSorted(tr)
	if len(got) != len(refAll) {
		t.Fatalf("final order len art=%d ref=%d", len(got), len(refAll))
	}
	for i, e := range refAll {
		if !bytes.Equal(got[i], e.Key) {
			t.Fatalf("order diverge at %d: %q vs %q", i, got[i], e.Key)
		}
	}

	// 前缀遍历对账
	for _, p := range [][]byte{[]byte("NHM-PB-0"), []byte("XZ"), []byte("Rosa"), {0}} {
		var artKeys [][]byte
		tr.PrefixScan(p, func(k, v []byte) bool {
			artKeys = append(artKeys, append([]byte(nil), k...))
			return true
		})
		refKeys := rf.Prefix(p)
		if len(artKeys) != len(refKeys) {
			t.Fatalf("prefix % x count art=%d ref=%d", p, len(artKeys), len(refKeys))
		}
		for j, e := range refKeys {
			if !bytes.Equal(artKeys[j], e.Key) {
				t.Fatalf("prefix order diverge p=% x at %d", p, j)
			}
		}
	}
}

func TestOrderPreservingInt(t *testing.T) {
	tr := New()
	rng := rand.New(rand.NewSource(7))
	nums := make([]int64, 2000)
	seen := map[int64]bool{}
	for i := range nums {
		v := int64(rng.Uint64())
		for seen[v] {
			v = int64(rng.Uint64())
		}
		seen[v] = true
		nums[i] = v
		tr.Insert(EncodeInt64(v), []byte("x"))
	}
	sorted := append([]int64(nil), nums...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	got := dumpSorted(tr)
	if len(got) != len(sorted) {
		t.Fatalf("len %d", len(got))
	}
	for i, v := range sorted {
		want := EncodeInt64(v)
		if !bytes.Equal(got[i], want) {
			t.Fatalf("order-preserving failed at %d", i)
		}
		if DecodeInt64(got[i]) != v {
			t.Fatal("roundtrip failed")
		}
	}

	// 无符号与浮点各做一次往返与顺序检查
	u := EncodeUint64(123456789)
	if DecodeUint64(u) != 123456789 {
		t.Fatal("uint roundtrip")
	}
	fl := EncodeFloat64(-2.5)
	fh := EncodeFloat64(1e9)
	if bytes.Compare(fl, fh) >= 0 {
		t.Fatal("float order wrong")
	}
}

func TestSeek(t *testing.T) {
	tr := New()
	var keys [][]byte
	rng := rand.New(rand.NewSource(99))
	seen := map[string]bool{}
	for i := 0; i < 3000; i++ {
		b := make([]byte, rng.Intn(12)+1)
		for j := range b {
			b[j] = byte(rng.Intn(6))
		}
		if seen[string(b)] {
			continue
		}
		seen[string(b)] = true
		tr.Insert(b, []byte("v"))
		keys = append(keys, append([]byte(nil), b...))
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })

	for trial := 0; trial < 500; trial++ {
		target := make([]byte, rng.Intn(14))
		for j := range target {
			target[j] = byte(rng.Intn(8))
		}
		it := tr.Iterator()
		it.Seek(target)
		idx := sort.Search(len(keys), func(i int) bool { return bytes.Compare(keys[i], target) >= 0 })
		if idx == len(keys) {
			if it.Valid() {
				t.Fatalf("seek past end still valid, target=% x got=% x", target, it.Key())
			}
			continue
		}
		if !it.Valid() || !bytes.Equal(it.Key(), keys[idx]) {
			got := []byte(nil)
			if it.Valid() {
				got = it.Key()
			}
			t.Fatalf("seek target=% x want=% x got=% x", target, keys[idx], got)
		}
	}

	// Seek 第一个键 / 不存在的超小键
	it := tr.Iterator()
	it.First()
	first := append([]byte(nil), it.Key()...)
	it.Seek([]byte{})
	if !it.Valid() || !bytes.Equal(it.Key(), first) {
		t.Fatal("seek empty must land on first key")
	}
}

func TestLongCompressedPrefix(t *testing.T) {
	tr := New()
	// 远超 maxPrefix 的公共前缀，在尾部才分叉
	base := bytes.Repeat([]byte("Z"), 50)
	k1 := append(append([]byte(nil), base...), 'A')
	k2 := append(append([]byte(nil), base...), 'B')
	k3 := append(append([]byte(nil), base...), 'B', '1')
	tr.Insert(k1, []byte("1"))
	tr.Insert(k2, []byte("2"))
	tr.Insert(k3, []byte("3"))
	for _, k := range [][]byte{k1, k2, k3} {
		if _, ok := tr.Get(k); !ok {
			t.Fatalf("long prefix key lost: %q", k)
		}
	}
	tr.Delete(k2)
	if _, ok := tr.Get(k2); ok {
		t.Fatal("k2 should be gone")
	}
	if _, ok := tr.Get(k3); !ok {
		t.Fatal("k3 must survive")
	}
	got := dumpSorted(tr)
	if len(got) != 2 || !bytes.Equal(got[0], k1) || !bytes.Equal(got[1], k3) {
		t.Fatalf("long prefix order wrong: %q", got)
	}
}

func TestGCWithTaggedPointers(t *testing.T) {
	tr := New()
	for i := 0; i < 10000; i++ {
		tr.Insert([]byte(fmt.Sprintf("key-%08d", i)), []byte(fmt.Sprintf("val-%d", i)))
	}
	for g := 0; g < 5; g++ {
		runtime.GC()
	}
	for i := 0; i < 10000; i++ {
		if v, ok := tr.Get([]byte(fmt.Sprintf("key-%08d", i))); !ok || string(v) != fmt.Sprintf("val-%d", i) {
			t.Fatalf("GC corrupted tree at %d", i)
		}
	}
}
