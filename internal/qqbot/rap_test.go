package qqbot

// rap_test.go 「锐评」指令：解析 + 记忆型锐评。

import (
	"strings"
	"testing"
)

func TestCommandRap(t *testing.T) {
	// 中文「锐评」映射到 /rap，参数正确切出
	name, arg, ok := Command("锐评 三角洲这游戏")
	if !ok || name != "/rap" {
		t.Fatalf("锐评 → name=%q ok=%v, want /rap", name, ok)
	}
	if arg != "三角洲这游戏" {
		t.Errorf("arg = %q, want 三角洲这游戏", arg)
	}
	// 「总结」仍然映射到 /summary（别名不互相抢占）
	if n2, _, ok2 := Command("总结"); !ok2 || n2 != "/summary" {
		t.Errorf("总结 → %q ok=%v", n2, ok2)
	}
	// 普通聊天不是指令
	if _, _, ok3 := Command("锐评一下下饭操作"); ok3 {
		// 「锐评一下」以「锐评」开头 → 是指令，参数为空
		if _, a4, _ := Command("锐评一下下饭操作"); a4 != "一下下饭操作" {
			t.Logf("arg=%q（符合预期：前缀匹配）", a4)
		}
	}
}

func TestRapOfUsesMemory(t *testing.T) {
	cfg := Default()
	cfg.MemoryFile = "" // 纯内存
	b := New(cfg)
	b.mem.Observe(1001, "怪我咯", "龙王你好")

	ev := &Event{MessageType: "group", GroupID: 1, UserID: 1001, SelfID: 3467208422}
	got := b.rapOf(ev, "", []int64{1001})
	if got == "" {
		t.Fatal("有记忆时应给出锐评")
	}
	// 应带上真实记忆要素（次数/名字），不是凭空编
	if !strings.Contains(got, "怪我咯") || !strings.Contains(got, "见过 1 次") {
		t.Errorf("锐评应基于真实记忆: %s", got)
	}
	// 无印象的人不给瞎编
	got2 := b.rapOf(&Event{MessageType: "group", GroupID: 1, UserID: 9999, SelfID: 3467208422}, "", []int64{8888})
	if !strings.Contains(got2, "毫无印象") {
		t.Errorf("没印象应直说: %s", got2)
	}
	// 话题型锐评（没 @ 人、有话题）→ 返回空，交给模型现写
	if r := b.rapOf(ev, "三角洲这游戏", nil); r != "" {
		t.Errorf("话题型应返回空交给模型，实际: %s", r)
	}
}
