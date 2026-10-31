package qqbot

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// gate.go —— 激活/静默状态机。
//
// 为什么需要：群里"监听所有消息"会一直回话，吵且烧额度。这里加一层闸门：
//
//	idle   —— 只有出现**激活词**（或被 @）才应声，应声后进入 active
//	active —— 激活期（默认 5 分钟）内正常回话，每说一句自动续期
//	silent —— 被**中断词**打断后静默（默认 60 秒），期间**任何消息都不处理**
//	          （连激活词、指令也不认——这是刻意的：用户说"闭嘴"就是真闭嘴）
//
// 作用域按会话（群 or 私聊）而不是按人：群里一个人激活，大家都能接着聊；
// 一个人喊闭嘴，整个群安静 60 秒——这符合"群聊是一个房间"的直觉。

type gateMode int

const (
	modeIdle gateMode = iota
	modeActive
	modeSilent
)

func (m gateMode) String() string {
	switch m {
	case modeActive:
		return "active"
	case modeSilent:
		return "silent"
	default:
		return "idle"
	}
}

type gateState struct {
	mode  gateMode
	until time.Time
	// hits 统计该会话被激活次数（后台观测用）
	hits int
}

// Gate 会话闸门。
type Gate struct {
	mu     sync.Mutex
	states map[string]*gateState
}

// NewGate 构造闸门。
func NewGate() *Gate { return &Gate{states: map[string]*gateState{}} }

// Decision 闸门判定结果。
type Decision struct {
	Allow  bool
	Mode   string // idle / active / silent
	Reason string // activated / in_window / interrupted / silent / idle / expired
	Until  time.Time
}

// Decide 判定这条消息是否该被处理，并推进状态机。
//
// key 是会话键（群/私聊），text 是抽好的纯文本，atBot 表示是否 @ 了机器人。
func (g *Gate) Decide(cfg Config, key, text string, atBot bool) Decision {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.states[key]
	if st == nil {
		st = &gateState{}
		g.states[key] = st
	}

	hasKeyword := func(list []string) bool {
		if len(list) == 0 || strings.TrimSpace(text) == "" {
			return false
		}
		lower := strings.ToLower(text)
		for _, k := range list {
			k = strings.ToLower(strings.TrimSpace(k))
			if k != "" && strings.Contains(lower, k) {
				return true
			}
		}
		return false
	}

	// 0) 冷静指令最高优先：@机器人 + 冷静词 → 直接进冷静期（默认 5 分钟）。
	//    放在最前面是因为它要能把"激活期"和"已静默期"都升级加长——用户明确要求安静时，
	//    剩下的 60 秒静默不该覆盖"冷静 5 分钟"。
	if atBot && hasKeyword(cfg.CalmKeywords) {
		until := now.Add(time.Duration(cfg.CalmSeconds) * time.Second)
		if until.After(st.until) {
			st.until = until
		}
		st.mode = modeSilent
		return Decision{Allow: false, Mode: modeSilent.String(), Reason: "calm", Until: st.until}
	}

	// 1) 静默期：什么都不处理，连激活词也不认（到期自动回 idle）
	if st.mode == modeSilent {
		if now.Before(st.until) {
			return Decision{Allow: false, Mode: modeSilent.String(), Reason: "silent", Until: st.until}
		}
		st.mode = modeIdle
	}

	// 2) 激活期：中断词优先，其次续期后放行
	if st.mode == modeActive {
		if now.Before(st.until) {
			if hasKeyword(cfg.InterruptKeywords) {
				st.mode = modeSilent
				st.until = now.Add(time.Duration(cfg.SilenceSeconds) * time.Second)
				return Decision{Allow: false, Mode: modeSilent.String(), Reason: "interrupted", Until: st.until}
			}
			// 说话即续期（滑动窗口）：聊得越热越不容易掉线
			st.until = now.Add(time.Duration(cfg.ActivationWindowSeconds) * time.Second)
			return Decision{Allow: true, Mode: modeActive.String(), Reason: "in_window", Until: st.until}
		}
		st.mode = modeIdle
	}

	// 3) 空闲：中断词也要能生效（用户可能想先叫停再说）
	if hasKeyword(cfg.InterruptKeywords) {
		st.mode = modeSilent
		st.until = now.Add(time.Duration(cfg.SilenceSeconds) * time.Second)
		return Decision{Allow: false, Mode: modeSilent.String(), Reason: "interrupted", Until: st.until}
	}

	// 4) 空闲：被 @ 或命中激活词 → 激活
	if atBot || hasKeyword(cfg.ActivationKeywords) {
		st.mode = modeActive
		st.hits++
		st.until = now.Add(time.Duration(cfg.ActivationWindowSeconds) * time.Second)
		return Decision{Allow: true, Mode: modeActive.String(), Reason: "activated", Until: st.until}
	}

	return Decision{Allow: false, Mode: modeIdle.String(), Reason: "idle"}
}

// GateView 后台展示用的会话状态。
type GateView struct {
	Key   string `json:"key"`
	Mode  string `json:"mode"`
	Until string `json:"until,omitempty"`
	Hits  int    `json:"hits"`
}

// Snapshot 返回所有会话的闸门状态（按模式+到期时间排序）。
func (g *Gate) Snapshot() []GateView {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]GateView, 0, len(g.states))
	for k, st := range g.states {
		v := GateView{Key: k, Mode: st.mode.String(), Hits: st.hits}
		if st.mode != modeIdle && !st.until.IsZero() {
			v.Until = st.until.Format("15:04:05")
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mode != out[j].Mode {
			return out[i].Mode < out[j].Mode
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Reset 清空某会话状态（后台手动解除静默用）。
func (g *Gate) Reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.states, key)
}
