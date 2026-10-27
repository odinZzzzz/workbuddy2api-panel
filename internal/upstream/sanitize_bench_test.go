package upstream

import (
	"strings"
	"testing"
)

// 热路径回归基准（不进默认 CI，手动跑）：
//
//	go test ./internal/upstream/ -run XXX -bench Sanitize -benchtime 300x -benchmem
//
// 基线（i5-1334U / 80KB system prompt，2026-10）：
//
//	                    优化前      优化后
//	干净文本（无指纹）  3.04 ms     ~61 µs（0 alloc）
//	含两句 DSH 指纹     4.34 ms     ~207 µs（165KB / 8 alloc）
//
// 优化前后的差距全部来自「预检用 (?i) 正则」：RE2 在 (?i) 下拿不到字面量前缀，
// 只能在每个字节位置跑自动机（~100 MB/s）。现在的预检是单遍字节折叠（~1.2 GB/s），
// 且命中后才按位门控跑对应规则。

// benchCleanPrompt 无任何指纹的 80KB 中文 system prompt（绝大多数请求的真实形态）。
func benchCleanPrompt() string {
	var sb strings.Builder
	for i := 0; i < 700; i++ {
		sb.WriteString("工具说明：读取文件、写入文件、运行命令；这一行用于把 system prompt 撑到真实规模。\n")
	}
	return sb.String()
}

// benchHitPrompt 同样规模，但开头就是 DSH 的两句指纹（命中路径）。
func benchHitPrompt() string {
	var sb strings.Builder
	sb.WriteString(dshIdentity)
	sb.WriteString("\n")
	sb.WriteString(dshCoding)
	sb.WriteString("\n")
	sb.WriteString(benchCleanPrompt())
	return sb.String()
}

func BenchmarkSanitizeClean80K(b *testing.B) {
	s := benchCleanPrompt()
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeText(s)
	}
}

func BenchmarkSanitizeHit80K(b *testing.B) {
	s := benchHitPrompt()
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeText(s)
	}
}

func BenchmarkProbeMaskClean80K(b *testing.B) {
	s := benchCleanPrompt()
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = probeMask(s)
	}
}
