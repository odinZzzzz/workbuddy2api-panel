package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Bridge 消息桥：OneBot 事件 → 网关 → OneBot 发消息。
type Bridge struct {
	// cfgPtr 用原子指针承载配置：后台热改人设/白名单时不需要停服，读路径也不加锁。
	// 之所以不直接用 cfg Config + 互斥锁：b.cfg 在热路径上有 20+ 处读取，逐个加锁
	// 既啰嗦又容易和既有锁（sessions/limits）缠成死锁。
	cfgPtr atomic.Pointer[Config]
	client *http.Client

	mu       sync.Mutex
	sessions map[string]*session
	limits   map[int64]*bucket
	mem      *Memory // 按 QQ 号持久化的记忆（人设连续性）
	state    *adminState
	gate     *Gate
	botGuard *BotGuard
	cfgPath  string
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type session struct {
	msgs []chatMsg
	last time.Time
}

// bucket 简易令牌桶（按分钟补充）。
type bucket struct {
	tokens float64
	last   time.Time
}

// New 构造桥。记忆打开失败不致命：没有记忆也能聊天，只是少了人设连续性。
func New(cfg Config) *Bridge {
	cfg.normalize()
	mem, err := OpenMemory(cfg.MemoryFile, cfg.MemoryNotesPerUser)
	if err != nil {
		log.Printf("[qqbot] memory disabled: %v", err)
		mem, _ = OpenMemory("", cfg.MemoryNotesPerUser)
	}
	cp := cfg
	br := &Bridge{
		client:   &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second},
		sessions: map[string]*session{},
		limits:   map[int64]*bucket{},
		mem:      mem,
		state:    &adminState{startedAt: time.Now()},
		gate:     NewGate(),
		botGuard: NewBotGuard(),
	}
	br.cfgPtr.Store(&cp)
	return br
}

// currentConfig 返回当前配置的副本（后台读取）。
func (b *Bridge) cfg() Config { return *b.cfgPtr.Load() }

func (b *Bridge) currentConfig() Config { return b.cfg() }

// applyConfig 热应用新配置：能热改的立刻生效；会话/限流状态保留。
func (b *Bridge) applyConfig(cfg Config) {
	cp := cfg
	b.cfgPtr.Store(&cp)
	b.client.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
}

// saveConfig 把当前配置写回文件（0600，tmp+rename）。
func (b *Bridge) saveConfig() error {
	b.mu.Lock()
	path := b.cfgPath
	b.mu.Unlock()
	cfg := b.cfg()
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o600)
	return os.Rename(tmp, path)
}

// SetConfigPath 记录配置文件路径（供后台保存回写）。
func (b *Bridge) SetConfigPath(p string) {
	b.mu.Lock()
	b.cfgPath = p
	b.mu.Unlock()
}

// Close 落盘记忆（优雅退出时调用）。
func (b *Bridge) Close() error {
	if b.mem == nil {
		return nil
	}
	return b.mem.Flush()
}

// Handler 返回 HTTP 路由：POST /onebot 收事件，GET /healthz 健康检查。
func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /onebot", b.handleEvent)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fmt.Sprintf(`{"ok":true,"enabled":%v,"model":%q}`, b.cfg().Enabled, b.cfg().Model))
	})
	return mux
}

// handleEvent 处理一条 OneBot 上报事件。
//
// 关键：**永远快速返回 200**。OneBot 上报是 fire-and-forget，处理慢会拖住上游
// 的事件循环；耗时的网关调用放到 goroutine 里，失败只记日志。
func (b *Bridge) handleEvent(w http.ResponseWriter, r *http.Request) {
	if !b.cfg().Enabled {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"ok":false,"reason":"disabled"}`)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"ok":true}`)

	// 只处理消息事件；心跳/通知/请求一律忽略。
	if ev.PostType != "message" {
		return
	}
	text, atBot := ParseText(ev.Message, ev.RawMessage, ev.SelfID)
	atTargets := ParseAtTargets(ev.Message, ev.RawMessage)
	// 到达即记一行：排查"为什么没回复"时，必须先能区分"事件没到"与"规则挡掉"。
	log.Printf("[qqbot] event type=%s group=%d user=%d at=%v len=%d",
		ev.MessageType, ev.GroupID, ev.UserID, atBot, len([]rune(text)))

	// 白名单：默认谁都不回（空名单 = 全拒），这是安全默认。
	// 忽略名单（别的 bot / 旧 bot）：必须在白名单判断之前——否则两个 bot 会互回成环。
	if b.cfg().Ignores(ev.UserID) {
		return
	}
	if ev.MessageType == "group" {
		if !b.cfg().AllowsGroup(ev.GroupID) {
			log.Printf("[qqbot] skip: group %d 不在白名单", ev.GroupID)
			return
		}
		if b.cfg().RequiresAt(ev.GroupID) && !atBot {
			return // 该群要求 @：没 @ 就静默
		}
	} else if ev.MessageType == "private" {
		if !b.cfg().AllowsUser(ev.UserID) {
			log.Printf("[qqbot] skip: user %d 不在白名单", ev.UserID)
			return
		}
	} else {
		return
	}

	key := chatKey(&ev, b.cfg().ScopePerUser())
	name := displayName(&ev)

	// bot 对话风控：对方像 bot 时，同一会话最多接 N 轮，超过高冷退场（冷却期内不理）。
	// 放在激活闸门之前：bot 的刷屏不该影响"人"的激活/静默状态。
	if b.botGuard != nil && LooksLikeBot(b.cfg().BotPatterns, ev.Sender.Nickname, ev.Sender.Card) {
		v := b.botGuard.Check(b.cfg(), key, ev.UserID)
		if !v.Allow {
			b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
				User: ev.UserID, Nick: name, At: atBot, Runes: len([]rune(text)), Action: v.Reason})
			if v.Reason == "bot_capped" {
				log.Printf("[qqbot] bot 风控: %s(%d) 已达 %d 轮，高冷退场至 %s", name, ev.UserID,
					b.cfg().BotRoundLimit, v.Until.Format("15:04:05"))
			}
			return
		}
	}

	// 激活闸门：idle 只认激活词/@；active 期内正常回话并续期；silent 期内一律不理。
	if b.gate != nil {
		d := b.gate.Decide(b.cfg(), key, text, atBot)
		if !d.Allow {
			b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
				User: ev.UserID, Nick: name, At: atBot, Runes: len([]rune(text)),
				Action: "gate_" + d.Reason})
			log.Printf("[qqbot] gate: %s 不放行（%s）user=%d", key, d.Reason, ev.UserID)
			return
		}
	}

	// 内置指令：不消耗网关额度。
	if cmd, arg, ok := Command(text); ok {
		go b.runCommand(&ev, key, name, cmd, arg, atTargets)
		return
	}
	if strings.TrimSpace(text) == "" {
		// 只 @ 不说话（群里很常见）：给一句引导，而不是静默——静默会被当成"机器人挂了"。
		// 这条不走网关，不消耗额度。
		if atBot {
			go func() {
				if err := b.send(&ev, "在的，说点什么？也可以发 /help 看指令。"); err != nil {
					log.Printf("[qqbot] hint send failed: %v", err)
				}
			}()
		}
		return
	}
	if !b.allow(ev.UserID) {
		log.Printf("[qqbot] rate limited user=%d(%s)", ev.UserID, name)
		return
	}
	// 先记下这次发言（人设记忆），再生成回复。
	if b.mem != nil {
		b.mem.Observe(ev.UserID, name, text)
	}
	b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
		User: ev.UserID, Nick: name, At: atBot, Runes: len([]rune(text)), Action: "replied"})
	go b.reply(&ev, key, name, text)
}

// allow 令牌桶限速：每用户每分钟 RatePerMinute 次（0 = 不限）。
func (b *Bridge) allow(userID int64) bool {
	if b.cfg().RatePerMinute <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	bk := b.limits[userID]
	if bk == nil {
		bk = &bucket{tokens: float64(b.cfg().RatePerMinute), last: now}
		b.limits[userID] = bk
	}
	// 按经过时间补充令牌
	elapsed := now.Sub(bk.last).Minutes()
	if elapsed > 0 {
		bk.tokens += elapsed * float64(b.cfg().RatePerMinute)
		if bk.tokens > float64(b.cfg().RatePerMinute) {
			bk.tokens = float64(b.cfg().RatePerMinute)
		}
		bk.last = now
	}
	if bk.tokens < 1 {
		return false
	}
	bk.tokens--
	return true
}

// reply 调网关拿回复并发出。
func (b *Bridge) reply(ev *Event, key, name, text string) {
	answer, err := b.chat(key, ev.UserID, text)
	if err != nil {
		log.Printf("[qqbot] chat failed key=%s user=%s: %v", key, name, err)
		answer = "（模型暂时不可用，稍后再试）"
	}
	answer = truncateRunes(answer, b.cfg().MaxReplyRunes)
	if strings.TrimSpace(answer) == "" {
		return
	}
	if err := b.send(ev, answer); err != nil {
		log.Printf("[qqbot] send failed key=%s: %v", key, err)
	}
}

// runCommand 处理内置指令。
func (b *Bridge) runCommand(ev *Event, key, name, cmd, arg string, atTargets []int64) {
	var out string
	switch cmd {
	case "/help":
		out = "指令：/help｜/clear 清空上下文｜/model 看模型｜/ping｜总结（或 总结 <QQ号>/@某人）看本王记得他什么"
	case "/summary", "/whois":
		out = b.summaryOf(ev, arg, atTargets)
	case "/clear", "/new":
		b.mu.Lock()
		delete(b.sessions, key)
		b.mu.Unlock()
		out = "上下文已清空"
	case "/model":
		out = "当前模型：" + b.cfg().Model
	case "/ping":
		out = "pong"
	}
	if out == "" {
		return
	}
	if err := b.send(ev, out); err != nil {
		log.Printf("[qqbot] command send failed: %v", err)
	}
}

// chat 走网关 /chat/completions（非流式），带会话历史。
func (b *Bridge) chat(key string, uid int64, userText string) (string, error) {
	msgs := b.buildMessages(key, uid, userText)
	payload := map[string]any{
		"model":    b.cfg().Model,
		"messages": msgs,
		"stream":   false,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	url := b.cfg().GatewayBase + "/chat/completions"
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(b.cfg().TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg().GatewayKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg().GatewayKey)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway %d: %s", resp.StatusCode, truncateRunes(string(body), 200))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("解析网关响应: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("网关返回空 choices")
	}
	answer := strings.TrimSpace(out.Choices[0].Message.Content)

	// 抽「〔点评：…〕」并入库（人设要求每轮收尾点评对方），正文保留点评行。
	if b.mem != nil {
		if v := extractVerdict(answer); v != "" {
			b.mem.SetVerdict(uid, v)
		}
	}
	// 记入历史（只记成功的一轮）
	b.appendHistory(key, userText, answer)
	return answer, nil
}

// buildMessages 组装请求消息：system + 历史 + 本轮。
func (b *Bridge) buildMessages(key string, uid int64, userText string) []chatMsg {
	out := make([]chatMsg, 0, 2*b.cfg().MaxHistoryPairs+2)
	system := strings.TrimSpace(b.cfg().SystemPrompt)
	// 把"你记得这个凡人"拼进 system：人设连续性靠它（同一人多轮之间记得住）。
	if b.mem != nil {
		if memo := b.mem.Describe(uid); memo != "" {
			if system != "" {
				system += "\n\n" + memo
			} else {
				system = memo
			}
		}
	}
	if system != "" {
		out = append(out, chatMsg{Role: "system", Content: system})
	}
	b.mu.Lock()
	s := b.sessions[key]
	var hist []chatMsg
	if s != nil {
		if b.cfg().SessionTTLMinutes > 0 && time.Since(s.last) > time.Duration(b.cfg().SessionTTLMinutes)*time.Minute {
			delete(b.sessions, key) // 闲置过期：按新会话处理
		} else {
			hist = append(hist, s.msgs...)
		}
	}
	b.mu.Unlock()
	out = append(out, hist...)
	out = append(out, chatMsg{Role: "user", Content: userText})
	return out
}

// appendHistory 追加一轮并裁剪到 MaxHistoryPairs 轮。
func (b *Bridge) appendHistory(key, userText, answer string) {
	if b.cfg().MaxHistoryPairs <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[key]
	if s == nil {
		s = &session{}
		b.sessions[key] = s
	}
	s.msgs = append(s.msgs,
		chatMsg{Role: "user", Content: userText},
		chatMsg{Role: "assistant", Content: answer})
	max := b.cfg().MaxHistoryPairs * 2
	if len(s.msgs) > max {
		s.msgs = s.msgs[len(s.msgs)-max:]
	}
	s.last = time.Now()
}

// send 按会话类型调 OneBot API 发消息。
func (b *Bridge) send(ev *Event, text string) error {
	var action string
	payload := map[string]any{"message": []map[string]any{
		{"type": "text", "data": map[string]string{"text": text}},
	}}
	if ev.MessageType == "group" {
		action = "send_group_msg"
		payload["group_id"] = ev.GroupID
	} else {
		action = "send_private_msg"
		payload["user_id"] = ev.UserID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := b.cfg().OneBotAPI + "/" + action
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg().OneBotToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg().OneBotToken)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("onebot %d: %s", resp.StatusCode, truncateRunes(string(body), 160))
	}
	// OneBot 约定：HTTP 200 但 retcode!=0 也算失败
	var r struct {
		RetCode int    `json:"retcode"`
		Status  string `json:"status"`
		Msg     string `json:"message"`
	}
	if json.Unmarshal(body, &r) == nil && r.RetCode != 0 {
		return fmt.Errorf("onebot retcode=%d msg=%s", r.RetCode, r.Msg)
	}
	return nil
}

// truncateRunes 按字符截断（避免切坏 UTF-8）。
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	rs := []rune(s)
	return string(rs[:max]) + "…"
}

// Stats 供健康检查/调试用的内部计数（会话数与限流桶数）。
func (b *Bridge) Stats() (sessions, limiters int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions), len(b.limits)
}

var _ = strconv.Itoa

// extractVerdict 从回复里抽出「〔点评：xxx〕」/「（点评：xxx）」/「【点评】xxx」。
//
// 为什么用标签抽取而不是再调一次模型：多一次调用就是多一份钱和延迟；点评本来
// 就是同一轮生成的一部分，抽出来存进记忆即可。
func extractVerdict(s string) string {
	for _, pair := range [][2]string{{"〔点评：", "〕"}, {"〔点评:", "〕"}, {"（点评：", "）"}, {"(点评:", ")"}} {
		if i := strings.Index(s, pair[0]); i >= 0 {
			rest := s[i+len(pair[0]):]
			if j := strings.Index(rest, pair[1]); j >= 0 {
				return strings.TrimSpace(rest[:j])
			}
			return strings.TrimSpace(rest)
		}
	}
	if i := strings.Index(s, "【点评】"); i >= 0 {
		rest := strings.TrimSpace(s[i+len("【点评】"):])
		if k := strings.IndexByte(rest, '\n'); k >= 0 {
			rest = rest[:k]
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

// summaryOf 生成「本王记得这个人什么」的总结（不走模型，零额度消耗）。
//
// 目标解析优先级：@ 的第一个人 > 参数里的数字 QQ > 说话人自己。
// 为什么不用模型生成：这类查询是纯事实汇总（见过几次/说过什么/上次点评），
// 用模板拼既准确又省钱，还能保证不编造。
func (b *Bridge) summaryOf(ev *Event, arg string, atTargets []int64) string {
	target := int64(0)
	for _, uid := range atTargets {
		if uid != ev.SelfID {
			target = uid
			break
		}
	}
	if target == 0 {
		if n, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64); err == nil && n > 0 {
			target = n
		}
	}
	if target == 0 {
		target = ev.UserID
	}
	if b.mem == nil {
		return "本王记忆未开，无话可说。"
	}
	u, ok := b.mem.Lookup(target)
	if !ok || (u.Count == 0 && len(u.Notes) == 0) {
		return "嗤，一个本王毫无印象的蝼蚁（" + strconv.FormatInt(target, 10) + "）。"
	}
	name := u.Nick
	if name == "" {
		name = "无名凡人"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "嗤，%s（%d）：见过 %d 次", name, target, u.Count)
	if !u.FirstSeen.IsZero() {
		fmt.Fprintf(&sb, "，首见 %s", u.FirstSeen.Format("2006-01-02"))
	}
	sb.WriteString("。")
	if len(u.Notes) > 0 {
		sb.WriteString("说过：")
		sb.WriteString(strings.Join(u.Notes, "；"))
		sb.WriteString("。")
	}
	if u.Verdict != "" {
		sb.WriteString("本王上次点评：")
		sb.WriteString(u.Verdict)
		sb.WriteString("。")
	}
	sb.WriteString("就这点货色。")
	return sb.String()
}

// gateSnapshot / botGuardSnapshot 供后台展示。
func (b *Bridge) gateSnapshot() []GateView {
	if b.gate == nil {
		return nil
	}
	return b.gate.Snapshot()
}

func (b *Bridge) botGuardSnapshot() []BotGuardView {
	if b.botGuard == nil {
		return nil
	}
	return b.botGuard.Snapshot()
}
