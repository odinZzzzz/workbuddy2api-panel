package qqbot

import "testing"

// 模拟线上：锐评话题时 rapOf 应返回空（交给模型），且不 panic
func TestRapTopicReturnsEmpty(t *testing.T) {
	cfg := Default()
	cfg.MemoryFile = ""
	b := New(cfg)
	ev := &Event{MessageType: "private", UserID: 941628696, SelfID: 3467208422}
	got := b.rapOf(ev, "三角洲行动这游戏", nil)
	if got != "" {
		t.Errorf("话题型应返回空，实际 %q", got)
	}
	t.Log("rapOf 话题型返回空 → 会走 doChat 分支")
}
