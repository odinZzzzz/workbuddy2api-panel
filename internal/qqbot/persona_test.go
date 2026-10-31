package qqbot

import (
	"testing"
	"time"
)

func personaCfg() Config {
	c := Default()
	c.PersonaDefaultPrompt = "KIND"
	c.PersonaAltPrompt = "DRAGON"
	c.PersonaAltSeconds = 2
	c.AttackKeywords = []string{"傻逼", "废物", "滚蛋"}
	c.MildKeywords = []string{"蠢", "垃圾", "笨蛋"}
	c.AngerThreshold = 100
	c.AngerMildGain = 25
	c.AngerDecay = 12
	return c
}

// 默认就是善良人格。
func TestPersonaDefaultKind(t *testing.T) {
	p := NewPersona()
	c := personaCfg()
	got := p.Decide(c, "group:1", "帮我看看这段代码")
	if got.Mode != personaKind || got.Prompt != "KIND" {
		t.Fatalf("默认应是善良人格: %+v", got)
	}
}

// 被骂 → 切龙王；同一房间后续消息仍是龙王。
func TestPersonaSwitchOnAttack(t *testing.T) {
	p := NewPersona()
	c := personaCfg()
	got := p.Decide(c, "group:1", "你个废物")
	if got.Mode != personaDragon || got.Prompt != "DRAGON" {
		t.Fatalf("被骂应切龙王: %+v", got)
	}
	if next := p.Decide(c, "group:1", "今天天气如何"); next.Mode != personaDragon {
		t.Fatalf("龙王期内应保持: %+v", next)
	}
	// 别的房间不受影响
	if other := p.Decide(c, "group:2", "帮我看看代码"); other.Mode != personaKind {
		t.Fatalf("其他房间不该被牵连: %+v", other)
	}
}

// 龙王期到期自动回到善良人格。
func TestPersonaRevertsAfterTTL(t *testing.T) {
	p := NewPersona()
	c := personaCfg()
	p.Decide(c, "group:1", "傻逼")
	time.Sleep(2200 * time.Millisecond)
	if got := p.Decide(c, "group:1", "帮我看看代码"); got.Mode != personaKind {
		t.Fatalf("到期应回落善良人格: %+v", got)
	}
}

// 手动 Reset 恢复善良人格。
func TestPersonaReset(t *testing.T) {
	p := NewPersona()
	c := personaCfg()
	p.Decide(c, "group:1", "滚蛋")
	p.Reset("group:1")
	if got := p.Decide(c, "group:1", "在吗"); got.Mode != personaKind {
		t.Fatalf("Reset 后应是善良人格: %+v", got)
	}
}

// 人格 prompt 要真的进 system（否则切了也没用）。
func TestPersonaPromptReachesSystem(t *testing.T) {
	c := Default()
	c.MemoryFile = ""
	b := New(c)
	msgs := b.buildMessagesWithPrompt("group:1", 1001, "你好", "KIND-PROMPT")
	if len(msgs) == 0 || msgs[0].Role != "system" || msgs[0].Content[:11] != "KIND-PROMPT" {
		t.Fatalf("人格 prompt 应作为 system 首条: %+v", msgs)
	}
}

// 攻击判定要"宽松"：技术用语与轻微吐槽不该切龙王；明确辱骂才切。
func TestAttackKeywordsLenient(t *testing.T) {
	c := Default()
	p := NewPersona()

	notAttack := []string{
		"这个垃圾回收器怎么调优",
		"这段代码写得真蠢",
		"sb 是 sandbox 的缩写吗",
		"你这个笨蛋哈哈",    // 打趣
		"蠢萌蠢萌的龙王",    // 调侃
		"废物利用一下这个函数", // 正常词
	}
	for i, msg := range notAttack {
		// 每条用独立房间：怒气会累积，同一房间连说三次轻度冒犯攒满切龙王是**正确**行为
		key := "group:lenient-" + itoa(int64(i))
		if got := p.Decide(c, key, msg); got.Mode != personaKind {
			t.Errorf("%q 不该触发龙王，实际 %s（anger=%d）", msg, got.Mode, got.Anger)
		}
	}

	attack := []string{"你个傻逼", "智障吧你", "人工智障", "滚蛋", "去死吧", "蠢货"}
	for _, msg := range attack {
		p.Reset("group:y")
		if got := p.Decide(c, "group:y", msg); got.Mode != personaDragon {
			t.Errorf("%q 应触发龙王，实际 %s", msg, got.Mode)
		}
	}
}

// 怒气攒满才切龙王：轻度冒犯三次不够，第四次满。
func TestAngerAccumulates(t *testing.T) {
	p := NewPersona()
	c := personaCfg()
	c.AngerMildGain = 30
	c.AngerDecay = 5

	for i := 1; i <= 3; i++ {
		got := p.Decide(c, "group:1", "这设计真蠢")
		if got.Mode != personaKind {
			t.Fatalf("第 %d 次轻度冒犯不该翻脸（anger=%d）", i, got.Anger)
		}
	}
	got := p.Decide(c, "group:1", "还是蠢")
	if got.Mode != personaDragon || got.Anger < 100 {
		t.Fatalf("第 4 次应攒满切龙王: %+v", got)
	}
}

// 友善对话消气：龙王期内聊几句就回落善良人格。
func TestAngerDecaysToCalm(t *testing.T) {
	p := NewPersona()
	c := personaCfg()
	c.AngerDecay = 30
	c.AngerThreshold = 100
	c.PersonaAltSeconds = 600

	if got := p.Decide(c, "group:1", "你个傻逼"); got.Mode != personaDragon {
		t.Fatalf("直接辱骂应拉满: %+v", got)
	}
	// 连续友善对话 → 怒气下降 → 掉到一半以下回落
	modes := []string{}
	for i := 0; i < 3; i++ {
		modes = append(modes, p.Decide(c, "group:1", "谢谢你").Mode)
	}
	if modes[len(modes)-1] != personaKind {
		t.Fatalf("聊几句后应消气回落，实际 %v", modes)
	}
}

// 直接辱骂一次拉满（不管之前多友善）。
func TestDirectInsultFillsAnger(t *testing.T) {
	p := NewPersona()
	c := personaCfg()
	c.AttackKeywords = []string{"智障", "傻逼"}
	for i := 0; i < 3; i++ {
		p.Decide(c, "group:1", "你好")
	}
	got := p.Decide(c, "group:1", "智障")
	if got.Mode != personaDragon {
		t.Fatalf("直接辱骂应一次拉满: %+v", got)
	}
}
