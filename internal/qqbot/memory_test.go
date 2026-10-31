package qqbot

import (
	"strings"
	"testing"
)

// 点评抽取：人设要求每轮收尾带〔点评：…〕，抽出来存进记忆。
func TestExtractVerdict(t *testing.T) {
	cases := map[string]string{
		"嗤，这都不会？〔点评：脑子慢但肯问〕": "脑子慢但肯问",
		"行吧。\n〔点评:还算识相〕":     "还算识相",
		"滚。（点评：嘴硬心虚）":        "嘴硬心虚",
		"【点评】胆子不小\n下一句":      "胆子不小",
		"没有标签的回复":            "",
	}
	for in, want := range cases {
		if got := extractVerdict(in); got != want {
			t.Errorf("extractVerdict(%q)=%q want %q", in, got, want)
		}
	}
}

// 点评入库后，下次描述会带上（人设连续性）。
func TestMemoryVerdictPersist(t *testing.T) {
	m, _ := OpenMemory("", 3)
	m.Observe(1, "怪我咯", "你好")
	m.SetVerdict(1, "脑子慢但肯问")
	d := m.Describe(1)
	if !strings.Contains(d, "上次对他的点评") || !strings.Contains(d, "脑子慢但肯问") {
		t.Fatalf("描述里应带上次点评: %s", d)
	}
}

// 总结指令：有记忆→按模板汇总（含次数/原话/点评）；无记忆→明确说没印象。
func TestSummaryOf(t *testing.T) {
	cfg := Default()
	cfg.MemoryFile = "" // 纯内存
	b := New(cfg)
	b.mem.Observe(1001, "怪我咯", "龙王你好")
	b.mem.SetVerdict(1001, "嘴硬心虚")

	ev := &Event{MessageType: "group", GroupID: 1, UserID: 1001, SelfID: 3467208422}
	got := b.summaryOf(ev, "", nil)
	for _, want := range []string{"怪我咯", "1001", "见过 1 次", "龙王你好", "嘴硬心虚"} {
		if !strings.Contains(got, want) {
			t.Errorf("总结缺 %q: %s", want, got)
		}
	}
	// 指定不存在的 QQ
	if s := b.summaryOf(ev, "9999", nil); !strings.Contains(s, "毫无印象") {
		t.Errorf("陌生人应说没印象: %s", s)
	}
	// @ 目标优先于参数
	if s := b.summaryOf(ev, "9999", []int64{1001}); !strings.Contains(s, "怪我咯") {
		t.Errorf("@ 目标应优先: %s", s)
	}
}
