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
