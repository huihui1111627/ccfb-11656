package art

import (
	"bytes"
	"encoding/binary"
	"math"
)

// 本文件实现“保序编码”：
//
//  1. 整数/浮点数编码后，按无符号字节做字典序比较的结果与数值大小顺序一致；
//  2. 任意用户键统一转义并追加结束符，保证树内任意两个键都不构成前缀关系，
//     这样路径压缩与叶子懒展开不需要额外的“键结束”分支。
//
// 转义规则（保持字典序）：
//
//	数据字节 0x00      -> 0x00 0x01
//	键结束（结束符）   -> 0x00 0x00
//
// 因为数据 0x00 被转成 (0x00,0x01)，结束符 (0x00,0x00) 永远排在它前面，
// 且不会出现在任何数据中，所以短键一定先于以它为前缀的长键，顺序与原始字节序完全一致。

// EncodeUint64 无符号 64 位整数：大端字节序本身即保序。
func EncodeUint64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// DecodeUint64 解回无符号整数。
func DecodeUint64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

// EncodeInt64 有符号整数：翻转符号位后负数变小、正数变大，再按大端存储。
func EncodeInt64(v int64) []byte {
	return EncodeUint64(uint64(v) + (1 << 63))
}

// DecodeInt64 解回有符号整数。
func DecodeInt64(b []byte) int64 {
	return int64(DecodeUint64(b) - (1 << 63))
}

// EncodeUint 平台无符号整数，统一按 64 位编码。
func EncodeUint(v uint) []byte { return EncodeUint64(uint64(v)) }

// EncodeInt 平台有符号整数，统一按 64 位编码。
func EncodeInt(v int) []byte { return EncodeInt64(int64(v)) }

// EncodeFloat64 IEEE-754 双精度浮点数保序编码（含 NaN，按位模式处理）。
func EncodeFloat64(f float64) []byte {
	bits := math.Float64bits(f)
	if bits&(1<<63) != 0 {
		bits = ^bits // 负数：全部翻转，绝对值越大编码越小
	} else {
		bits ^= 1 << 63 // 非负数：只翻符号位
	}
	return EncodeUint64(bits)
}

// encodePrefix 只做 0x00 转义、不加结束符，供前缀查找使用。
func encodePrefix(prefix []byte) []byte {
	zeros := bytes.Count(prefix, []byte{0})
	out := make([]byte, 0, len(prefix)+zeros)
	for _, b := range prefix {
		if b == 0 {
			out = append(out, 0x00, 0x01)
		} else {
			out = append(out, b)
		}
	}
	return out
}

// encodeKey 树内存储的完整键：转义数据 + 结束符 (0x00,0x00)。
func encodeKey(key []byte) []byte {
	out := encodePrefix(key)
	return append(out, 0x00, 0x00)
}

// decodeKey 把树内键还原成用户键（页面展示、对账时使用）。
func decodeKey(enc []byte) []byte {
	out := make([]byte, 0, len(enc))
	for i := 0; i < len(enc); {
		if enc[i] != 0 {
			out = append(out, enc[i])
			i++
			continue
		}
		// 0x00 后面必跟 0x01（数据零）或 0x00（结束符）。
		if i+1 >= len(enc) || enc[i+1] == 0 {
			break
		}
		out = append(out, 0)
		i += 2
	}
	return out
}

// rawKeyLen 不分配内存，统计内部键对应的原始键长度（内存对账用）。
func rawKeyLen(enc []byte) int {
	n := 0
	for i := 0; i < len(enc); {
		if enc[i] != 0 {
			n++
			i++
			continue
		}
		if i+1 >= len(enc) || enc[i+1] == 0 {
			break
		}
		n++
		i += 2
	}
	return n
}
