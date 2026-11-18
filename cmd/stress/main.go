// 百万级压力测试：
//
//	阶段一（严格对账）：小规模键集合，逐步插入/删除/查找/前缀遍历，
//	  每一步都与排好序的切片参考实现比对，任何不一致立即失败。
//	阶段二（规模验证）：百万级键，map 做点查基准、排序快照做顺序基准，
//	  统计 ART 的节点规格分布、内存占用与各操作耗时，并和有序切片、map 对比。
//
// 用法：
//
//	go run ./cmd/stress -n 1000000 -ops 200000 -strict 30000 -seed 20261119
package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"

	"museumart/art"
	"museumart/bench"
	"museumart/ref"
	"museumart/sim"
)

func main() {
	n := flag.Int("n", 1_000_000, "大规模阶段的键总数")
	ops := flag.Int("ops", 200_000, "大规模阶段的随机操作数")
	strictN := flag.Int("strict", 30_000, "严格对账阶段的键池规模")
	strictOps := flag.Int("strict-ops", 200_000, "严格对账阶段的随机操作数")
	seed := flag.Int64("seed", 20261119, "随机种子，保证结果可复现")
	flag.Parse()

	fmt.Println("================ 阶段一：严格对账（有序切片参考实现） ================")
	runStrict(*strictN, *strictOps, *seed)
	fmt.Println("严格对账全部通过 ✓")

	fmt.Println()
	fmt.Println("================ 阶段二：百万级规模验证与内存/耗时对比 ================")
	runScale(*n, *ops, *seed)
}

// keySet 汇总四类仿真键，统一成 []byte。
type keySet struct {
	catalog [][]byte // 馆藏号
	collect [][]byte // 采集号
	sci     [][]byte // 学名
	uints   [][]byte // 无符号整数（保序编码）
	ints    [][]byte // 有符号整数（保序编码）
	all     [][]byte
}

func buildKeys(g *sim.Generator, n int) *keySet {
	ks := &keySet{}
	per := n / 4
	for i := 0; i < per; i++ {
		ks.catalog = append(ks.catalog, g.CatalogNo(i))
		ks.collect = append(ks.collect, g.CollectionNo(i))
		ks.sci = append(ks.sci, g.ScientificName(i))
	}
	for _, v := range g.UintKeys(per) {
		ks.uints = append(ks.uints, art.EncodeUint64(v))
	}
	for _, v := range g.IntKeys(per) {
		ks.ints = append(ks.ints, art.EncodeInt64(v))
	}
	ks.all = make([][]byte, 0, n)
	ks.all = append(ks.all, ks.catalog...)
	ks.all = append(ks.all, ks.collect...)
	ks.all = append(ks.all, ks.sci...)
	ks.all = append(ks.all, ks.uints...)
	ks.all = append(ks.all, ks.ints...)
	return ks
}

func valFor(i int) []byte {
	return []byte(fmt.Sprintf("specimen-%08d", i))
}

func runStrict(n, ops int, seed int64) {
	g := sim.New(seed)
	ks := buildKeys(g, n)
	rng := rand.New(rand.NewSource(seed + 1))

	tree := art.New()
	rf := ref.New()

	fmt.Printf("键池规模=%d（五类键各约 %d），随机操作=%d\n", len(ks.all), n/4, ops)

	// 打乱后插入，覆盖各种节点升级与路径压缩路径。
	insOrder := rng.Perm(len(ks.all))
	insTimed := bench.Measure(len(insOrder), func() {
		for _, idx := range insOrder {
			v := valFor(idx)
			rold, rrep := rf.Insert(ks.all[idx], v)
			told, trep := tree.Insert(ks.all[idx], v)
			if rrep != trep || (rrep && !bytes.Equal(rold, told)) {
				fatalf("插入不一致 key=% x", ks.all[idx])
			}
		}
	})
	fmt.Printf("插入 %d 键：%v（均 %d ns/op），参考切片 %d 键\n",
		tree.Len(), insTimed.Total, insTimed.Nanos, rf.Len())
	if tree.Len() != rf.Len() {
		fatalf("长度不一致 art=%d ref=%d", tree.Len(), rf.Len())
	}

	// 全量顺序对账。
	refAll := rf.All()
	pos := 0
	tree.Each(func(k, v []byte) bool {
		if pos >= len(refAll) || !bytes.Equal(k, refAll[pos].Key) || !bytes.Equal(v, refAll[pos].Val) {
			fatalf("全量顺序在第 %d 项不一致", pos)
		}
		pos++
		return true
	})
	if pos != len(refAll) {
		fatalf("全量顺序数量不一致 art=%d ref=%d", pos, len(refAll))
	}
	fmt.Println("全量有序遍历对账通过 ✓")

	// 前缀遍历对账（含零字节前缀，专门检验转义）。
	prefixes := append(sim.Prefixes(), []byte{0x00})
	for _, p := range prefixes {
		want := rf.Prefix(p)
		got := 0
		tree.PrefixScan(p, func(k, v []byte) bool {
			if got >= len(want) || !bytes.Equal(k, want[got].Key) {
				fatalf("前缀 % x 第 %d 项不一致", p, got)
			}
			got++
			return true
		})
		if got != len(want) {
			fatalf("前缀 % x 数量不一致 art=%d ref=%d", p, got, len(want))
		}
	}
	fmt.Println("前缀范围遍历对账通过 ✓")

	// 混合随机操作，每一步都对账。
	// 用 deleted 集合显式跟踪每个键的存活状态，删除/重插只针对真实状态，
	// 避免取模游标导致的重复删除与计数错位。
	deleted := make(map[int]bool)
	checkpoints := 0
	for step := 0; step < ops; step++ {
		switch rng.Intn(10) {
		case 0, 1, 2, 3: // 40% 查找：随机键，命中与否必须两边一致
			k := ks.all[rng.Intn(len(ks.all))]
			rv, rok := rf.Get(k)
			tv, tok := tree.Get(k)
			if rok != tok || (rok && !bytes.Equal(rv, tv)) {
				fatalf("查找不一致 key=% x", k)
			}
		case 4, 5, 6, 7: // 40% 插入：在已删键里挑一个重插，没有则插随机存活键（覆盖语义）
			var idx int
			if len(deleted) > 0 {
				for d := range deleted {
					idx = d
					break
				}
				delete(deleted, idx)
			} else {
				idx = rng.Intn(len(ks.all))
			}
			k := ks.all[idx]
			rold, rrep := rf.Insert(k, valFor(idx))
			told, trep := tree.Insert(k, valFor(idx))
			if rrep != trep || (rrep && !bytes.Equal(rold, told)) {
				fatalf("插入不一致 key=% x", k)
			}
		default: // 20% 删除：只删当前存活的键
			idx := rng.Intn(len(ks.all))
			if deleted[idx] {
				continue
			}
			k := ks.all[idx]
			rv, rok := rf.Delete(k)
			tv, tok := tree.Delete(k)
			if rok != tok || (rok && !bytes.Equal(rv, tv)) {
				fatalf("删除不一致 key=% x", k)
			}
			if rok {
				deleted[idx] = true
			}
		}
		if tree.Len() != rf.Len() {
			fatalf("第 %d 步长度不一致 art=%d ref=%d", step, tree.Len(), rf.Len())
		}
		// 定期全量顺序快照对账。
		if (step+1)%50000 == 0 {
			verifyOrder(tree, rf)
			checkpoints++
		}
	}
	fmt.Printf("混合操作 %d 步全部对账通过（%d 次全量快照检查点）✓\n", ops, checkpoints)

	// 收尾再做一次全量与前缀对账。
	verifyOrder(tree, rf)
	for _, p := range prefixes {
		want := rf.Prefix(p)
		got := 0
		tree.PrefixScan(p, func(k, v []byte) bool { got++; return true })
		if got != len(want) {
			fatalf("收尾前缀 % x 数量不一致", p)
		}
	}
}

func verifyOrder(tree *art.Tree, rf *ref.SortedSlice) {
	refAll := rf.All()
	// 先确认 ref 里的每个键 ART 都能 Get 到（区分丢键与遍历乱序）。
	for _, e := range refAll {
		if _, ok := tree.Get(e.Key); !ok {
			fatalf("ART 丢键 ref 有而 Get 无: % x", e.Key)
		}
	}
	pos := 0
	tree.Each(func(k, v []byte) bool {
		if pos >= len(refAll) || !bytes.Equal(k, refAll[pos].Key) {
			fatalf("快照顺序在第 %d 项不一致", pos)
		}
		pos++
		return true
	})
	if pos != len(refAll) {
		fatalf("快照数量不一致 art=%d ref=%d", pos, len(refAll))
	}
}

func runScale(n, ops int, seed int64) {
	g := sim.New(seed)
	ks := buildKeys(g, n)
	rng := rand.New(rand.NewSource(seed + 2))
	fmt.Printf("键总数=%d，随机操作=%d\n", len(ks.all), ops)

	// ---- ART 构建 ----
	tree := art.New()
	heapBefore := bench.HeapBytes()
	build := bench.Measure(len(ks.all), func() {
		for i, k := range ks.all {
			tree.Insert(k, valFor(i))
		}
	})
	heapART := bench.HeapBytes() - heapBefore
	fmt.Printf("\n[ART] 构建 %d 键：%v（均 %d ns/op）\n", tree.Len(), build.Total, build.Nanos)

	st := tree.Collect()
	printArtStats(st, heapART)

	// ---- map 参考结构构建（点查基准）----
	mp := make(map[string][]byte, len(ks.all))
	heapBefore = bench.HeapBytes()
	mapBuild := bench.Measure(len(ks.all), func() {
		for i, k := range ks.all {
			mp[string(k)] = valFor(i)
		}
	})
	heapMap := bench.HeapBytes() - heapBefore
	fmt.Printf("\n[map] 构建 %d 键：%v（均 %d ns/op），运行时堆增量 %s\n",
		len(mp), mapBuild.Total, mapBuild.Nanos, humanBytesU(heapMap))

	// ---- 有序切片参考（小规模即可，O(n) 插入不适合百万级）----
	sliceN := min(50_000, len(ks.all))
	rf := ref.New()
	sliceBuild := bench.Measure(sliceN, func() {
		for i := 0; i < sliceN; i++ {
			rf.Insert(ks.all[i], valFor(i))
		}
	})
	fmt.Printf("[有序切片] 构建 %d 键：%v（均 %d ns/op，O(n) 搬移，仅作小规模对照）\n",
		sliceN, sliceBuild.Total, sliceBuild.Nanos)

	// ---- 点查耗时：ART vs map ----
	probeIdx := rng.Perm(len(ks.all))[:min(ops, len(ks.all))]
	getArt := bench.Measure(len(probeIdx), func() {
		for _, idx := range probeIdx {
			if _, ok := tree.Get(ks.all[idx]); !ok {
				fatalf("ART 点查未命中 idx=%d", idx)
			}
		}
	})
	getMap := bench.Measure(len(probeIdx), func() {
		for _, idx := range probeIdx {
			if _, ok := mp[string(ks.all[idx])]; !ok {
				fatalf("map 点查未命中 idx=%d", idx)
			}
		}
	})
	fmt.Printf("\n点查 %d 次：ART %v（均 %d ns/op） | map %v（均 %d ns/op）\n",
		len(probeIdx), getArt.Total, getArt.Nanos, getMap.Total, getMap.Nanos)

	// ---- 前缀范围遍历耗时 ----
	prefixes := sim.Prefixes()
	pfxTotal := 0
	pfxTime := bench.Measure(len(prefixes), func() {
		for _, p := range prefixes {
			tree.PrefixScan(p, func(k, v []byte) bool {
				pfxTotal++
				return true
			})
		}
	})
	fmt.Printf("前缀遍历 %d 组共命中 %d 键：%v（均 %d ns/组）\n",
		len(prefixes), pfxTotal, pfxTime.Total, pfxTime.Nanos)

	// ---- 顺序快照：与排序切片基准核对 ----
	sortedKeys := make([][]byte, len(ks.all))
	copy(sortedKeys, ks.all)
	sortTimed := bench.Measure(1, func() {
		sort.Slice(sortedKeys, func(i, j int) bool { return bytes.Compare(sortedKeys[i], sortedKeys[j]) < 0 })
	})
	pos := 0
	scanTimed := bench.Measure(1, func() {
		tree.Each(func(k, v []byte) bool {
			if !bytes.Equal(k, sortedKeys[pos]) {
				fatalf("百万级顺序快照在第 %d 项不一致", pos)
			}
			pos++
			return true
		})
	})
	if pos != len(sortedKeys) {
		fatalf("百万级顺序快照数量不一致 %d vs %d", pos, len(sortedKeys))
	}
	fmt.Printf("顺序快照对账通过 ✓（排序基准 %v，ART 全量遍历 %v）\n", sortTimed.Total, scanTimed.Total)

	// ---- 删除 + 再插入，验证降级/塌缩在大规模下的正确性与耗时 ----
	delN := min(ops, len(ks.all)/2)
	delIdx := rng.Perm(len(ks.all))[:delN]
	delTime := bench.Measure(delN, func() {
		for _, idx := range delIdx {
			if _, ok := tree.Delete(ks.all[idx]); !ok {
				fatalf("大规模删除未命中 idx=%d", idx)
			}
		}
	})
	fmt.Printf("\n删除 %d 键：%v（均 %d ns/op），剩余 %d 键\n", delN, delTime.Total, delTime.Nanos, tree.Len())
	st2 := tree.Collect()
	printArtStats(st2, 0)

	reins := bench.Measure(delN, func() {
		for _, idx := range delIdx {
			tree.Insert(ks.all[idx], valFor(idx))
		}
	})
	fmt.Printf("重新插入 %d 键：%v（均 %d ns/op），恢复到 %d 键\n", delN, reins.Total, reins.Nanos, tree.Len())

	// 恢复后再次全量顺序对账。
	pos = 0
	tree.Each(func(k, v []byte) bool {
		if !bytes.Equal(k, sortedKeys[pos]) {
			fatalf("删除/重插后顺序在第 %d 项不一致", pos)
		}
		pos++
		return true
	})
	if pos != len(sortedKeys) {
		fatalf("删除/重插后数量不一致")
	}
	fmt.Println("删除降级与重新升级后的全量对账通过 ✓")

	// ---- 内存口径汇总 ----
	fmt.Println("\n================ 内存占用对比 ================")
	fmt.Printf("ART 解析口径（节点+叶子头+编码键+值）：%s\n", humanBytes(st.TotalBytes))
	fmt.Printf("ART 运行时堆增量口径：                %s\n", humanBytesU(heapART))
	fmt.Printf("map 运行时堆增量口径：                %s\n", humanBytesU(heapMap))
	fmt.Printf("有序切片解析口径（%d 键外推到 %d 键）：%s（估算）\n",
		sliceN, len(ks.all), humanBytes(extrapolateSlice(rf.Bytes(), sliceN, len(ks.all))))
	fmt.Printf("\n每键平均内存：ART 解析口径 %d B/键，map 堆口径 %d B/键\n",
		st.TotalBytes/int64(tree.Len()), heapMap/uint64(tree.Len()))
}

func extrapolateSlice(sampleBytes int64, sampleN, totalN int) int64 {
	return sampleBytes * int64(totalN) / int64(sampleN)
}

func printArtStats(s *art.Stats, heap uint64) {
	fmt.Println("节点规格分布：")
	for _, k := range art.KindOrder {
		ks := s.ByKind[k]
		fmt.Printf("  %-7s 数量=%-8d 单节点=%4d B 小计=%s\n",
			art.KindLabel(ks.Kind), ks.Count, ks.BytesPerNode, humanBytes(ks.Bytes))
	}
	fmt.Printf("内部节点=%d 叶子=%d 最大深度=%d 平均深度=%.2f\n",
		s.InnerNodes, s.Leaves, s.MaxDepth, s.AvgDepth())
	fmt.Printf("内部节点结构 %s | 叶子头 %s | 编码键 %s（原始键 %s）| 值 %s\n",
		humanBytes(s.InnerBytes), humanBytes(s.LeafHdrBytes),
		humanBytes(s.KeyBytesEnc), humanBytes(s.KeyBytesRaw), humanBytes(s.ValueBytes))
	if heap > 0 {
		fmt.Printf("运行时堆增量（GC 口径）：%s\n", humanBytesU(heap))
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanBytesU(b uint64) string { return humanBytes(int64(b)) }

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "致命错误："+format+"\n", args...)
	os.Exit(1)
}
