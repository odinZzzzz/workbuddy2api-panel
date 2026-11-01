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
	// 预算 6500 字符：4.9k 是"只有短描述"时的水位；draw_sticker（画板）带一份
	// 525 字符的 SVG 绘制规范，是唯一一个"长描述物有所值"的工具——少了它模型会写
	// <text>/<g transform> 这类渲染不出来的元素，白画一张空图再重试，比占 schema 更亏。
	// 其余工具仍要求短描述：新增工具若把预算顶穿，先想清楚值不值。
	const budget = 6500
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
