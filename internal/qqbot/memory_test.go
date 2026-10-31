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

// 好感度：增减、夹紧、档位标签、注入描述。
func TestAffinityClampAndTier(t *testing.T) {
	m, _ := OpenMemory("", 3)
	if got := m.AddAffinity(1, AffinityPerChat); got != 1 {
		t.Errorf("聊一轮应 +1，得到 %d", got)
	}
	if got := m.AddAffinity(1, AffinityPerCalm); got != -7 {
		t.Errorf("冷静应 -8，得到 %d", got)
	}
	// 上夹紧
	for i := 0; i < 200; i++ {
		m.AddAffinity(1, 10)
	}
	if u, _ := m.Lookup(1); u.Affinity != 100 {
		t.Errorf("上限应夹到 100，得到 %d", u.Affinity)
	}
	// 下夹紧
	for i := 0; i < 200; i++ {
		m.AddAffinity(1, -10)
	}
	if u, _ := m.Lookup(1); u.Affinity != -100 {
		t.Errorf("下限应夹到 -100，得到 %d", u.Affinity)
	}
	// 档位
	cases := map[int]string{-100: "厌恶", -10: "看不顺眼", 0: "蝼蚁而已", 30: "勉强顺眼", 60: "还算识相", 90: "本王认了"}
	for v, want := range cases {
		if got := AffinityTier(v); got != want {
			t.Errorf("AffinityTier(%d)=%q want %q", v, got, want)
		}
	}
}

// 好感度要出现在 prompt 注入与总结里（否则"系统"只是自嗨）。
func TestAffinityVisible(t *testing.T) {
	cfg := Default()
	cfg.MemoryFile = ""
	b := New(cfg)
	b.mem.Observe(1001, "怪我咯", "你好")
	b.mem.AddAffinity(1001, 60)

	if d := b.mem.Describe(1001); !strings.Contains(d, "好感度 60/100") || !strings.Contains(d, "还算识相") {
		t.Errorf("描述里应带好感度与档位: %s", d)
	}
	ev := &Event{MessageType: "private", UserID: 1001, SelfID: 3467208422}
	if s := b.summaryOf(ev, "", nil); !strings.Contains(s, "好感度 60/100") {
		t.Errorf("总结里应带好感度: %s", s)
	}
}

// @某人 时，总结应针对被 @ 的人；且不把点评写进说话人记忆。
func TestSummaryTargetFromAt(t *testing.T) {
	cfg := Default()
	cfg.MemoryFile = ""
	b := New(cfg)
	b.mem.Observe(2001, "被艾特的人", "我来了")
	b.mem.Observe(1001, "说话的人", "我是说话的人")

	sender := &Event{MessageType: "group", GroupID: 1, UserID: 1001, SelfID: 3467208422}
	// 本条 @ 了 2001（机器人自己不在 atTargets 里时也算他人）
	got := b.summaryOf(sender, "", []int64{3467208422, 2001})
	if !strings.Contains(got, "被艾特的人") || strings.Contains(got, "说话的人") {
		t.Errorf("总结目标应是被 @ 的人，得到: %s", got)
	}
	// otherAtTargets 应过滤掉自己
	if others := otherAtTargets([]int64{3467208422, 2001}, 3467208422); len(others) != 1 || others[0] != 2001 {
		t.Errorf("otherAtTargets 过滤错误: %v", others)
	}
}
