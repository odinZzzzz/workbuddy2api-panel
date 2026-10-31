package qqbot

import (
	"strings"
	"sync"
	"time"
)

// persona.go —— 双人格状态机。
//
// 设定：
//   kind   （默认）知识渊博、谦逊、乐于助人的「新时代好青年」——平时都用这个
//   dragon （备用）桀骜不驯的龙王——**被攻击时**才切过去，过一段时间自动回到 kind
//
// 为什么按会话而不是全局：群里有人骂了龙王，不该让私聊里问技术问题的人也被喷。
// 状态挂在房间键上（与激活闸门同作用域），到期自动回落。

const (
	personaKind   = "kind"
	personaDragon = "dragon"
)

type personaState struct {
	mode  string
	until time.Time
	// switches 记录切换次数（后台观测）
	switches int
}

// PersonaSwitch 人格选择结果。
type PersonaSwitch struct {
	Mode   string
	Prompt string
	Until  time.Time
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

// Decide 判定本条消息该用哪个人格。
//
// 规则：
//  1. 命中攻击词 → 立刻切龙王（并续期），返回龙王 prompt
//  2. 已在龙王期且未过期 → 继续龙王
//  3. 龙王期已过期 → 回落善良人格
//  4. 其余 → 善良人格（默认）
func (p *Persona) Decide(cfg Config, key, text string) PersonaSwitch {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	st := p.states[key]
	if st == nil {
		st = &personaState{mode: personaKind}
		p.states[key] = st
	}

	// 攻击词优先：即使当前是龙王期，被再骂一次就续期
	if hitAny(text, cfg.AttackKeywords) {
		if st.mode != personaDragon {
			st.switches++
		}
		st.mode = personaDragon
		st.until = now.Add(time.Duration(cfg.PersonaAltSeconds) * time.Second)
		return PersonaSwitch{Mode: personaDragon, Prompt: cfg.PersonaAltPrompt, Until: st.until}
	}

	if st.mode == personaDragon {
		if now.Before(st.until) {
			return PersonaSwitch{Mode: personaDragon, Prompt: cfg.PersonaAltPrompt, Until: st.until}
		}
		// 到期：回到善良人格
		st.mode = personaKind
		st.until = time.Time{}
	}

	return PersonaSwitch{Mode: personaKind, Prompt: cfg.PersonaDefaultPrompt}
}

// PersonaView 后台展示。
type PersonaView struct {
	Key      string `json:"key"`
	Mode     string `json:"mode"`
	Until    string `json:"until,omitempty"`
	Switches int    `json:"switches"`
}

// Snapshot 当前各会话人格状态。
func (p *Persona) Snapshot() []PersonaView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PersonaView, 0, len(p.states))
	for k, st := range p.states {
		v := PersonaView{Key: k, Mode: st.mode, Switches: st.switches}
		if st.mode == personaDragon && !st.until.IsZero() {
			v.Until = st.until.Format("15:04:05")
		}
		out = append(out, v)
	}
	return out
}

// Reset 手动恢复善良人格（后台/指令用）。
func (p *Persona) Reset(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st := p.states[key]; st != nil {
		st.mode = personaKind
		st.until = time.Time{}
	}
}
