// Package bench 存放压测与页面共用的计时、内存测量工具。
package bench

import (
	"runtime"
	"time"
)

// Timed 测量 fn 的墙钟耗时与平均每操作纳秒数。
type Timed struct {
	Total time.Duration
	Nanos int64
	Ops   int
}

func Measure(ops int, fn func()) Timed {
	// 先 GC 一次，尽量减少后台回收对计时的干扰。
	runtime.GC()
	start := time.Now()
	fn()
	d := time.Since(start)
	t := Timed{Total: d, Ops: ops}
	if ops > 0 {
		t.Nanos = d.Nanoseconds() / int64(ops)
	}
	return t
}

// HeapBytes 强制 GC 后返回当前堆上存活字节数，用于结构间内存占用对比。
// 注意这是 Go 运行时口径（含分配器碎片与 span 元数据），与 art.Stats 的
// 结构解析口径不同，两个口径都会在报告里给出。
func HeapBytes() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}
