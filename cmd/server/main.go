// 标本馆 ART 内存索引的查看页面服务。
//
// 启动时按 -n 生成仿真馆藏数据并建树，页面分四块：
//  1. 分键浏览（分页 + 前缀过滤）
//  2. 树结构（可限深度的交互式树）
//  3. 节点规格分布（node4/16/48/256 数量与占比）
//  4. 内存占用与耗时（解析口径 + GC 口径，对照 map 与有序切片）
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"museumart/art"
	"museumart/bench"
	"museumart/sim"
)

//go:embed web
var webFS embed.FS

type server struct {
	tree    *art.Tree
	stats   *art.Stats
	heap    uint64
	timings map[string]int64 // ns
	compare compareInfo
}

type compareInfo struct {
	MapHeap       uint64
	SliceEstimate int64
	ARTPerKey     int64
	MapPerKey     uint64
}

func main() {
	n := flag.Int("n", 200_000, "页面演示用的键总数")
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	seed := flag.Int64("seed", 20261119, "仿真数据随机种子")
	flag.Parse()

	srv := buildServer(*n, *seed)

	mux := http.NewServeMux()
	// embed 的根含 web/ 目录，用 fs.Sub 切进去，使 URL 根直接对应 index.html。
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/keys", srv.handleKeys)
	mux.HandleFunc("/api/tree", srv.handleTree)
	mux.HandleFunc("/api/stats", srv.handleStats)
	mux.HandleFunc("/api/memory", srv.handleMemory)

	log.Printf("标本馆 ART 索引页面已启动：http://localhost%s （%d 键）", *addr, *n)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func buildServer(n int, seed int64) *server {
	g := sim.New(seed)
	per := n / 4
	tree := art.New()

	// 计时：构建
	build := bench.Measure(n, func() {
		for i := 0; i < per; i++ {
			tree.Insert(g.CatalogNo(i), []byte(fmt.Sprintf("cat-%d", i)))
			tree.Insert(g.CollectionNo(i), []byte(fmt.Sprintf("col-%d", i)))
			tree.Insert(g.ScientificName(i), []byte(fmt.Sprintf("sci-%d", i)))
		}
		for _, v := range g.UintKeys(per) {
			tree.Insert(art.EncodeUint64(v), []byte("u"))
		}
		for _, v := range g.IntKeys(per) {
			tree.Insert(art.EncodeInt64(v), []byte("i"))
		}
	})

	// 点查耗时（抽样 1 万次）
	sample := 10000
	probe := make([][]byte, sample)
	for i := range probe {
		probe[i] = g.CatalogNo((i * 7919) % per)
	}
	get := bench.Measure(sample, func() {
		for _, k := range probe {
			tree.Get(k)
		}
	})

	// 前缀遍历耗时
	pfx := bench.Measure(1, func() {
		tree.PrefixScan([]byte("NHM-PB-0"), func(k, v []byte) bool { return true })
	})

	stats := tree.Collect()
	heap := bench.HeapBytes()

	// map 对照
	mp := make(map[string][]byte, n)
	heapBefore := bench.HeapBytes()
	for i := 0; i < per; i++ {
		mp[string(g.CatalogNo(i))] = []byte("x")
		mp[string(g.CollectionNo(i))] = []byte("x")
		mp[string(g.ScientificName(i))] = []byte("x")
	}
	mapHeap := bench.HeapBytes() - heapBefore

	return &server{
		tree:  tree,
		stats: stats,
		heap:  heap,
		timings: map[string]int64{
			"buildNs":  build.Nanos,
			"getNs":    get.Nanos,
			"prefixNs": pfx.Total.Nanoseconds(),
		},
		compare: compareInfo{
			MapHeap:       mapHeap,
			SliceEstimate: estimateSlice(stats, tree.Len()),
			ARTPerKey:     stats.TotalBytes / int64(tree.Len()),
			MapPerKey:     mapHeap / uint64(tree.Len()),
		},
	}
}

// estimateSlice 用与压测一致的口径估算有序切片外推内存。
func estimateSlice(s *art.Stats, n int) int64 {
	// 切片：24B 头/槽 + 48B 条目头 + 键负载 + 值负载
	return int64(n)*(24+48) + s.KeyBytesRaw + s.ValueBytes
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// keyView 是键在 JSON 里的表示：hex 保序且可直接当分页游标，text 供人读。
// 十六进制字典序与原始字节序完全一致，避免 base64 打乱顺序。
type keyView struct {
	Hex  string `json:"hex"`
	Text string `json:"text"`
	Size int    `json:"size"`
}

func toKeyView(k, v []byte) keyView {
	return keyView{Hex: hexEncode(k), Text: printable(k), Size: len(v)}
}

func hexEncode(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i] = hexd[c>>4]
		out[2*i+1] = hexd[c&0x0f]
	}
	return string(out)
}

func hexDecode(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := range out {
		hi := unhex(s[2*i])
		lo := unhex(s[2*i+1])
		out[i] = hi<<4 | lo
	}
	return out
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// printable 把可打印 ASCII/UTF-8 原样显示，其余字节转 \xNN。
func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 32 && c < 127 {
			sb.WriteByte(c)
		} else {
			sb.WriteString(fmt.Sprintf("\\x%02x", c))
		}
	}
	return sb.String()
}

func (s *server) handleKeys(w http.ResponseWriter, r *http.Request) {
	after := r.URL.Query().Get("after")
	prefix := r.URL.Query().Get("prefix")
	limit := 50

	if prefix != "" {
		// 前缀模式：返回该前缀下的前 limit 个键。
		var views []keyView
		s.tree.PrefixScan([]byte(prefix), func(k, v []byte) bool {
			if len(views) >= limit {
				return false
			}
			views = append(views, toKeyView(k, v))
			return true
		})
		writeJSON(w, map[string]any{
			"keys": views, "total": s.tree.Len(), "prefix": prefix,
		})
		return
	}

	keys, sizes, cursor, total := s.tree.PageKeys(hexDecode(after), limit)
	views := make([]keyView, len(keys))
	for i := range keys {
		views[i] = keyView{Hex: hexEncode(keys[i]), Text: printable(keys[i]), Size: sizes[i]}
	}
	writeJSON(w, map[string]any{
		"keys": views, "total": total,
		"next": map[bool]string{true: hexEncode(cursor), false: ""}[len(cursor) > 0],
	})
}

func (s *server) handleTree(w http.ResponseWriter, r *http.Request) {
	maxDepth := 4
	if d := r.URL.Query().Get("depth"); d != "" {
		fmt.Sscan(d, &maxDepth)
	}
	writeJSON(w, s.tree.ExportTree(maxDepth))
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	st := s.stats
	type kindRow struct {
		Kind         string  `json:"kind"`
		Count        int     `json:"count"`
		BytesPerNode int     `json:"bytesPerNode"`
		Bytes        int64   `json:"bytes"`
		Pct          float64 `json:"pct"`
	}
	rows := make([]kindRow, 0, 4)
	for _, k := range art.KindOrder {
		ks := st.ByKind[k]
		pct := 0.0
		if st.InnerNodes > 0 {
			pct = float64(ks.Count) * 100 / float64(st.InnerNodes)
		}
		rows = append(rows, kindRow{
			Kind: art.KindLabel(k), Count: ks.Count,
			BytesPerNode: ks.BytesPerNode, Bytes: ks.Bytes, Pct: pct,
		})
	}
	writeJSON(w, map[string]any{
		"kinds":      rows,
		"innerNodes": st.InnerNodes,
		"leaves":     st.Leaves,
		"maxDepth":   st.MaxDepth,
		"avgDepth":   st.AvgDepth(),
	})
}

func (s *server) handleMemory(w http.ResponseWriter, r *http.Request) {
	st := s.stats
	writeJSON(w, map[string]any{
		"art": map[string]any{
			"innerBytes":   st.InnerBytes,
			"leafHdrBytes": st.LeafHdrBytes,
			"keyBytesEnc":  st.KeyBytesEnc,
			"keyBytesRaw":  st.KeyBytesRaw,
			"valueBytes":   st.ValueBytes,
			"totalBytes":   st.TotalBytes,
			"heapBytes":    s.heap,
			"perKey":       s.compare.ARTPerKey,
		},
		"map": map[string]any{
			"heapBytes": s.compare.MapHeap,
			"perKey":    s.compare.MapPerKey,
		},
		"sortedSlice": map[string]any{
			"estimatedBytes": s.compare.SliceEstimate,
			"note":           "有序切片 O(n) 插入，按解析口径外推估算",
		},
		"timings": s.timings,
		"keys":    s.tree.Len(),
	})
}
