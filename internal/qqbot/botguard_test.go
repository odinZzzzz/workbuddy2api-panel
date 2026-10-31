package qqbot

import (
	"testing"
	"time"
)

func botCfg() Config {
	c := Default()
	c.BotPatterns = []string{"bot", "机器人", "助手"}
	c.BotRoundLimit = 3
	c.BotCooldownSeconds = 2
	return c
}

// 昵称/群名片命中关键词即判为 bot。
func TestLooksLikeBot(t *testing.T) {
	p := []string{"bot", "机器人", "助手"}
	cases := []struct {
		nick, card string
		want       bool
	}{
		{"黑猫Bot", "", true},
		{"", "签到助手", true},
		{"小苔藓", "机器人", true},
		{"怪我咯", "", false},
		{"", "", false},
		{"BOT", "", true}, // 不区分大小写
	}
	for _, c := range cases {
		if got := LooksLikeBot(p, c.nick, c.card); got != c.want {
			t.Errorf("LooksLikeBot(%q,%q)=%v want %v", c.nick, c.card, got, c.want)
		}
	}
}

// 同一个 bot 最多 3 轮，第 4 轮高冷退场。
func TestBotGuardRoundLimit(t *testing.T) {
	g := NewBotGuard()
	c := botCfg()
	for i := 1; i <= 3; i++ {
		v := g.Check(c, "group:1", 2854199922)
		if !v.Allow || v.Rounds != i {
			t.Fatalf("第 %d 轮应放行，得到 %+v", i, v)
		}
	}
	v := g.Check(c, "group:1", 2854199922)
	if v.Allow || v.Reason != "bot_capped" {
		t.Fatalf("第 4 轮应高冷退场，得到 %+v", v)
	}
	// 冷却期内继续不理
	if v := g.Check(c, "group:1", 2854199922); v.Allow || v.Reason != "bot_cold" {
		t.Fatalf("冷却期内应 bot_cold，得到 %+v", v)
	}
}

// 冷却到期后重新给 3 轮。
func TestBotGuardCooldownResets(t *testing.T) {
	g := NewBotGuard()
	c := botCfg()
	for i := 0; i < 4; i++ {
		g.Check(c, "group:1", 1)
	}
	time.Sleep(2200 * time.Millisecond)
	if v := g.Check(c, "group:1", 1); !v.Allow || v.Rounds != 1 {
		t.Fatalf("冷却到期应重新计数，得到 %+v", v)
	}
}

// 计数按「会话 + bot」隔离：不同群、不同 bot 互不影响。
func TestBotGuardIsolation(t *testing.T) {
	g := NewBotGuard()
	c := botCfg()
	for i := 0; i < 3; i++ {
		g.Check(c, "group:1", 1)
	}
	if v := g.Check(c, "group:2", 1); !v.Allow {
		t.Fatalf("另一个群的计数应独立: %+v", v)
	}
	if v := g.Check(c, "group:1", 2); !v.Allow {
		t.Fatalf("另一个 bot 的计数应独立: %+v", v)
	}
}
