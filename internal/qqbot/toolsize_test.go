package qqbot

import (
	"encoding/json"
	"testing"
)

// 工具 schema 是 prompt 的大头（19 个工具曾占 6,991 字符 ≈ 1,750 token，约整个 prompt 的 75%）。
// 这条测试把它钉成预算：新增工具或写长描述超预算就红，逼着人先想清楚值不值。
func TestToolSchemaBudget(t *testing.T) {
	c := Default()
	c.MemoryFile = ""
	b := New(c)
	defs := b.buildTools().defs
	raw, _ := json.Marshal(defs)
	const budget = 5200 // 字符；压完描述后实测 ~4.9k
	if len(raw) > budget {
		t.Fatalf("工具 schema %d 字符，超预算 %d（每个工具的描述都要短，别把使用说明写进 schema）", len(raw), budget)
	}
	t.Logf("工具数=%d schema=%d 字符（预算 %d）", len(defs), len(raw), budget)
	type row struct {
		name string
		n    int
	}
	var rows []row
	for _, d := range defs {
		j, _ := json.Marshal(d)
		rows = append(rows, row{d.Function.Name, len(j)})
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].n > rows[i].n {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	for i, r := range rows {
		if i >= 8 {
			break
		}
		t.Logf("  %-22s %5d", r.name, r.n)
	}
}
