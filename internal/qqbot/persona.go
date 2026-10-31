package qqbot

import (
	"strings"
	"sync"
	"time"
)

// persona.go —— 双人格 + 怒气值。
//
// 设定：
//   kind   （默认）知识渊博、谦逊、乐于助人的「新时代好青年」
//   dragon （备用）桀骜不驯的龙王——**怒气攒满**才切，消气后自动回落
//
// 怒气模型（比"命中关键词就翻脸"自然得多）：
//   - 直接辱骂（attack_keywords）  → 一次拉满（+threshold），立刻切龙王
//   - 阴阳怪气/轻度冒犯（mild_keywords）→ +mild_gain（默认 25）
//   - 正常/友善对话               → -decay（默认 12），也就是"聊几句就消气"
//   - 怒气 >= threshold（默认 100）→ 切龙王，维持 persona_alt_seconds
//   - 龙王期内怒气掉到一半以下      → 提前回到善良人格（"气消了"）
//
// 为什么按房间：群里有人骂了龙王，不该让私聊里问技术的人也被喷。

const (
	personaKind   = "kind"
	personaDragon = "dragon"
)

type personaState struct {
	mode     string
	until    time.Time
	anger    int
	switches int
}

// PersonaSwitch 人格选择结果。
type PersonaSwitch struct {
	Mode   string
	Prompt string
	Until  time.Time
	Anger  int
}

// Persona 双人格状态机。
type Persona struct {
	mu     sync.Mutex
	states map[string]*personaState
}

// NewPersona 构造。
func NewPersona() *Persona { return &Persona{states: map[string]*personaState{}} }

// hitAny 文本是否命中任一关键词（不区分大小写）。
func hitAny(text string, kws []string) bool {
	if strings.TrimSpace(text) == "" || len(kws) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, k := range kws {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" && strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// Decide 更新怒气并判定本条消息该用哪个人格。
func (p *Persona) Decide(cfg Config, key, text string) PersonaSwitch {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	st := p.states[key]
	if st == nil {
		st = &personaState{mode: personaKind}
		p.states[key] = st
	}
	threshold := cfg.AngerThreshold
	if threshold <= 0 {
		threshold = 100
	}

	// 1) 怒气结算
	switch {
	case hitAny(text, cfg.AttackKeywords):
		st.anger = threshold // 直接辱骂：一次拉满
	case hitAny(text, cfg.MildKeywords):
		st.anger += cfg.AngerMildGain
	default:
		st.anger -= cfg.AngerDecay
	}
	if st.anger < 0 {
		st.anger = 0
	}
	if st.anger > threshold {
		st.anger = threshold
	}

	// 2) 攒满 → 切龙王（续期）
	if st.anger >= threshold {
		if st.mode != personaDragon {
			st.switches++
			st.mode = personaDragon
		}
		st.until = now.Add(time.Duration(cfg.PersonaAltSeconds) * time.Second)
		return PersonaSwitch{Mode: personaDragon, Prompt: cfg.PersonaAltPrompt, Until: st.until, Anger: st.anger}
	}

	// 3) 龙王期内：气消到一半以下，或时间到了 → 回落
	if st.mode == personaDragon {
		calmed := st.anger <= threshold/2
		expired := !st.until.IsZero() && !now.Before(st.until)
		if calmed || expired {
			st.mode = personaKind
			st.until = time.Time{}
			if expired && st.anger > threshold/2 {
				st.anger = threshold / 2 // 时间到了但还气：留一半，别刚回去又炸
			}
		} else {
			return PersonaSwitch{Mode: personaDragon, Prompt: cfg.PersonaAltPrompt, Until: st.until, Anger: st.anger}
		}
	}

	return PersonaSwitch{Mode: personaKind, Prompt: cfg.PersonaDefaultPrompt, Anger: st.anger}
}

// PersonaView 后台展示。
type PersonaView struct {
	Key      string `json:"key"`
	Mode     string `json:"mode"`
	Anger    int    `json:"anger"`
	Until    string `json:"until,omitempty"`
	Switches int    `json:"switches"`
}

// Snapshot 当前各会话人格状态。
func (p *Persona) Snapshot() []PersonaView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PersonaView, 0, len(p.states))
	for k, st := range p.states {
		v := PersonaView{Key: k, Mode: st.mode, Anger: st.anger, Switches: st.switches}
		if st.mode == personaDragon && !st.until.IsZero() {
			v.Until = st.until.Format("15:04:05")
		}
		out = append(out, v)
	}
	return out
}

// Reset 手动消气并恢复善良人格。
func (p *Persona) Reset(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st := p.states[key]; st != nil {
		st.mode = personaKind
		st.until = time.Time{}
		st.anger = 0
	}
}
