package qqbot

import (
	"strings"
	"sync"
	"time"
)

// botguard.go —— bot 对话风控。
//
// 为什么单独一层：QQ 群里不止一个 bot，bot 之间很容易"你一句我一句"地互相回，
// 既烧额度又会刷屏成环。做法是识别对方像 bot（昵称/群名片命中关键词，或显式名单），
// 对同一个 bot 在同一会话里**最多接 3 轮**，超过就"高冷退场"：冷却期内完全不理它。
//
// 为什么用"轮数+冷却"而不是永久拉黑：有些 bot 偶尔会给出有用信息，永久拉黑会漏；
// 3 轮足够完成一次交换，冷却到期后还能再聊一次——像人一样"烦了就不理了"。

type botCounter struct {
	rounds int
	until  time.Time // 高冷退场截止时刻
}

// BotGuard bot 对话风控。
type BotGuard struct {
	mu       sync.Mutex
	counters map[string]*botCounter // key = chatKey + "|" + uid
}

// NewBotGuard 构造。
func NewBotGuard() *BotGuard { return &BotGuard{counters: map[string]*botCounter{}} }

// LooksLikeBot 判断发言者是否像 bot（昵称或群名片命中关键词）。
//
// 启发式而不是权威判定：QQ 协议不告诉客户端"这是 bot"，只能靠昵称特征。
// 显式名单（ignored_users）优先于本判定，走的是硬忽略。
func LooksLikeBot(patterns []string, nick, card string) bool {
	if len(patterns) == 0 {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(nick + " " + card))
	if name == "" {
		return false
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" && strings.Contains(name, p) {
			return true
		}
	}
	return false
}

// Verdict bot 风控判定结果。
type Verdict struct {
	Allow  bool
	Reason string // ok / bot_capped / bot_cold
	Rounds int
	Until  time.Time
}

// Check 对一条来自 bot 的消息做判定；非 bot 消息请勿调用（调用方先用 LooksLikeBot 判断）。
func (g *BotGuard) Check(cfg Config, key string, uid int64) Verdict {
	now := time.Now()
	gk := key + "|" + itoa(uid)

	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.counters[gk]
	if c == nil {
		c = &botCounter{}
		g.counters[gk] = c
	}
	// 冷却期内：高冷退场，一句都不回
	if now.Before(c.until) {
		return Verdict{Allow: false, Reason: "bot_cold", Rounds: c.rounds, Until: c.until}
	}
	// 冷却到期：计数归零，重新给 3 轮
	if !c.until.IsZero() && !now.Before(c.until) {
		c.rounds = 0
		c.until = time.Time{}
	}
	c.rounds++
	if c.rounds > cfg.BotRoundLimit {
		c.until = now.Add(time.Duration(cfg.BotCooldownSeconds) * time.Second)
		return Verdict{Allow: false, Reason: "bot_capped", Rounds: c.rounds - 1, Until: c.until}
	}
	return Verdict{Allow: true, Reason: "ok", Rounds: c.rounds}
}

// Snapshot 后台展示用。
type BotGuardView struct {
	Key    string `json:"key"`
	Rounds int    `json:"rounds"`
	Until  string `json:"until,omitempty"`
}

// Snapshot 返回所有 bot 计数状态。
func (g *BotGuard) Snapshot() []BotGuardView {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]BotGuardView, 0, len(g.counters))
	for k, c := range g.counters {
		v := BotGuardView{Key: k, Rounds: c.rounds}
		if !c.until.IsZero() {
			v.Until = c.until.Format("15:04:05")
		}
		out = append(out, v)
	}
	return out
}

// itoa 小工具（避免为一个转换引 strconv 到本文件）。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
