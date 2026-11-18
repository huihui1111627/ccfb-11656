// Package sim 生成自然博物馆标本馆风格的仿真键：
// 馆藏号、采集号、学名（拉丁双名）以及保序编码后的有符号/无符号整数。
package sim

import (
	"fmt"
	"math/rand"
)

// 常见植物拉丁属名/种加词片段，组合出看起来合理的双名。
var genus = []string{
	"Quercus", "Rhododendron", "Magnolia", "Acer", "Pinus", "Cycas",
	"Camellia", "Rosa", "Primula", "Gentiana", "Saussurea", "Pedicularis",
	"Corydalis", "Impatiens", "Begonia", "Ficus", "Ilex", "Viburnum",
	"Lonicera", "Spiraea", "Rubus", "Carex", "Poa", "Festuca",
}

var epithet = []string{
	"sinensis", "yunnanensis", "tibetica", "chinensis", "japonica",
	"montana", "alba", "rubra", "glabra", "hirsuta", "lanceolata",
	"ovalifolia", "microphylla", "grandiflora", "longispica", "caerulea",
	"nitida", "pallida", "rufescens", "gracilis", "robusta", "tenuifolia",
}

var families = []string{
	"Fagaceae", "Ericaceae", "Magnoliaceae", "Sapindaceae", "Pinaceae",
	"Cycadaceae", "Theaceae", "Rosaceae", "Primulaceae", "Gentianaceae",
}

// Generator 仿真数据生成器。所有键池都保证唯一。
type Generator struct {
	rng *rand.Rand
}

func New(seed int64) *Generator {
	return &Generator{rng: rand.New(rand.NewSource(seed))}
}

// CatalogNo 馆藏号，形如 NHM-PB-0000001，定宽零填充，前缀/范围查询友好。
func (g *Generator) CatalogNo(i int) []byte {
	return []byte(fmt.Sprintf("NHM-PB-%08d", i))
}

// CollectionNo 采集号：队号缩写 + 年份 + 流水号。
func (g *Generator) CollectionNo(i int) []byte {
	team := []string{"XZ", "YN", "SC", "GS", "HLJ", "GX"}[i%6]
	year := 1980 + i%46
	return []byte(fmt.Sprintf("%s%d-%06d", team, year, i))
}

// ScientificName 学名：属名.种加词#流水号，#号保证唯一且不影响前缀浏览。
func (g *Generator) ScientificName(i int) []byte {
	gname := genus[g.rng.Intn(len(genus))]
	ep := epithet[g.rng.Intn(len(epithet))]
	fam := families[g.rng.Intn(len(families))]
	return []byte(fmt.Sprintf("%s|%s %s#%07d", fam, gname, ep, i))
}

// UintKeys 生成 n 个不重复的无符号整数键（数值域内随机抽样）。
func (g *Generator) UintKeys(n int) []uint64 {
	seen := make(map[uint64]struct{}, n)
	out := make([]uint64, 0, n)
	for len(out) < n {
		v := g.rng.Uint64()
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// IntKeys 生成 n 个不重复的有符号整数键（正负混合，专门检验保序编码）。
func (g *Generator) IntKeys(n int) []int64 {
	seen := make(map[int64]struct{}, n)
	out := make([]int64, 0, n)
	for len(out) < n {
		v := int64(g.rng.Uint64())
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// Prefixes 返回一组有真实命中率的前缀（压测前缀范围遍历用）。
func Prefixes() [][]byte {
	return [][]byte{
		[]byte("NHM-PB-000"),
		[]byte("NHM-PB-0001"),
		[]byte("XZ19"),
		[]byte("YN20"),
		[]byte("Ericaceae|"),
		[]byte("Rosaceae|Rosa"),
		[]byte("Pinaceae|Pinus"),
	}
}
