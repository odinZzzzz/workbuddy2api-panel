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
	"path/filepath"
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
	persona  *Persona
	cfgPath  string
}

type chatMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
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
		persona:  NewPersona(),
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

	// 戳一戳：notice 事件里 notice_type=poke 且目标是自己。
	if ev.PostType == "notice" && ev.NoticeType == "poke" && ev.TargetID != 0 && ev.TargetID == ev.SelfID {
		b.handlePoke(&ev)
		return
	}
	// 只处理消息事件；心跳/通知/请求一律忽略。
	if ev.PostType != "message" {
		return
	}
	text, atBot := ParseText(ev.Message, ev.RawMessage, ev.SelfID)
	atTargets := ParseAtTargets(ev.Message, ev.RawMessage)
	images := ParseImages(ev.Message)
	if len(images) > 0 {
		// 模型看不到图，但必须知道"有人发了图"，否则答非所问。
		marker := "[图片×" + itoa(int64(len(images))) + "]"
		if !strings.Contains(text, marker) {
			if text == "" {
				text = marker
			} else {
				text = text + " " + marker
			}
		}
	}
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

	// 激活闸门只作用于群聊：私聊是"直接找上门"，再要求激活词等于把门焊死。
	// 私聊仍受静默/冷静约束（用户自己叫停的场合），所以走 Decide 但忽略 idle 结果。
	//
	// 闸门作用域是**房间**（group:<gid>），不是「房间+人」：群里 A 喊了"小八嘎"，
	// 龙王应当跟整个房间聊一会儿，而不是只有 A 说话才算数——按人隔离会让人觉得
	// "激活了但没反应"（记忆/上下文才按人隔离，见 chatKey）。
	if b.gate != nil {
		gateKey := chatKey(&ev, false)
		d := b.gate.Decide(b.cfg(), gateKey, text, atBot)
		// 私聊里 idle 视为放行（不需要激活词）；silent/calm/interrupted 仍然生效
		if ev.MessageType == "private" && d.Reason == "idle" {
			d.Allow = true
		}
		// 好感度按闸门结果增减（确定性规则，不调模型）
		if b.mem != nil {
			switch d.Reason {
			case "interrupted":
				b.mem.AddAffinity(ev.UserID, AffinityPerInterrupt)
			case "calm":
				b.mem.AddAffinity(ev.UserID, AffinityPerCalm)
			case "activated":
				if atBot {
					b.mem.AddAffinity(ev.UserID, AffinityPerAt)
				}
			}
		}
		if !d.Allow {
			b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
				User: ev.UserID, Nick: name, At: atBot, Runes: len([]rune(text)),
				Action: "gate_" + d.Reason})
			log.Printf("[qqbot] gate: %s 不放行（%s）user=%d", gateKey, d.Reason, ev.UserID)
			return
		}
	}

	// 把"本条 @ 了谁"告诉模型：否则它只会看到被剥掉 @ 的纯文本，
	// 遇到"@某人 总结一下"时根本不知道要总结谁，只能拿说话人凑数（实测踩过）。
	if others := otherAtTargets(atTargets, ev.SelfID); len(others) > 0 {
		var names []string
		for _, uid := range others {
			names = append(names, itoa(uid))
		}
		text = text + "（本条 @ 了 QQ " + strings.Join(names, "、") + "，如果要对" +
			"这些人说话/总结，就用他们的 QQ 号）"
	}

	// 表情包：命中关键词就发一张（不消耗模型额度）
	if b.matchesSticker(text) {
		b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
			User: ev.UserID, Nick: name, At: atBot, Runes: len([]rune(text)), Action: "sticker"})
		go b.sendSticker(&ev)
		return
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
	// 顺手刷新画像（昵称/等级/签名/号位数）：异步、一天一次，失败静默。
	b.refreshProfileAsync(ev.UserID)
	// 双人格：默认善良好青年，被攻击才切龙王（按房间，到期自动回落）
	prompt := b.cfg().PersonaDefaultPrompt
	if b.persona != nil {
		ps := b.persona.Decide(b.cfg(), chatKey(&ev, false), text)
		if strings.TrimSpace(ps.Prompt) != "" {
			prompt = ps.Prompt
		}
		log.Printf("[qqbot] persona=%s anger=%d key=%s", ps.Mode, ps.Anger, chatKey(&ev, false))
	}
	b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
		User: ev.UserID, Nick: name, At: atBot, Runes: len([]rune(text)), Action: "replied"})
	go b.reply(&ev, key, name, text, atTargets, prompt)
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
func (b *Bridge) reply(ev *Event, key, name, text string, atTargets []int64, prompt string) {
	answer, err := b.chat(key, ev, text, atTargets, prompt)
	if err != nil {
		log.Printf("[qqbot] chat failed key=%s user=%s: %v", key, name, err)
		answer = "（模型暂时不可用，稍后再试）"
	}
	answer = truncateRunes(answer, b.cfg().MaxReplyRunes)
	if strings.TrimSpace(answer) == "" {
		return
	}
	log.Printf("[qqbot] reply → %s: %s", key, truncateRunes(strings.ReplaceAll(answer, "\n", " / "), 120))
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
	b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
		User: ev.UserID, Nick: name, Action: "cmd_" + strings.TrimPrefix(cmd, "/")})
	if err := b.send(ev, out); err != nil {
		log.Printf("[qqbot] command send failed: %v", err)
	}
}

// chat 走网关 /chat/completions（非流式），带会话历史。
func (b *Bridge) chat(key string, ev *Event, userText string, atTargets []int64, prompt string) (string, error) {
	uid := ev.UserID
	msgs := b.buildMessagesWithPrompt(key, uid, userText, prompt)

	var tools *toolRegistry
	if b.cfg().ToolsEnabled {
		tools = b.buildTools()
	}
	rounds := b.cfg().MaxToolRounds
	if rounds <= 0 {
		rounds = 3
	}

	answer := ""
	for i := 0; i <= rounds; i++ {
		msg, err := b.doChat(msgs, tools)
		if err != nil {
			return "", err
		}
		if len(msg.ToolCalls) == 0 || tools == nil {
			answer = strings.TrimSpace(msg.Content)
			break
		}
		// 把模型这次的工具请求写回对话，再逐条执行并回填结果
		msgs = append(msgs, chatMsg{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls})
		for _, call := range msg.ToolCalls {
			res, e := tools.exec(toolContext{ev: ev, b: b}, call)
			if e != nil {
				res = "执行失败：" + e.Error()
				log.Printf("[qqbot] tool %s 失败: %v", call.Function.Name, e)
			}
			msgs = append(msgs, chatMsg{Role: "tool", Content: res, ToolCallID: call.ID})
		}
		if i == rounds {
			// 轮数用尽：再要一次纯文本收尾，别把"我要调工具"当回复发出去
			if msg2, e := b.doChat(msgs, nil); e == nil {
				answer = strings.TrimSpace(msg2.Content)
			}
		}
	}
	if strings.TrimSpace(answer) == "" {
		answer = "……"
	}

	// 聊成一轮 → 好感度 +1（封顶 100）
	if b.mem != nil {
		b.mem.AddAffinity(uid, AffinityPerChat)
	}
	// 抽「〔点评：…〕」存进记忆，并从**可见回复**里剥掉（点评是内心活动，不必每轮外发）
	if v := extractVerdict(answer); v != "" {
		// 本条若 @ 了别人，点评很可能是针对被 @ 的那个人的，别写进说话人记忆里
		if b.mem != nil && len(otherAtTargets(atTargets, ev.SelfID)) == 0 {
			b.mem.SetVerdict(uid, v)
		}
		if stripped := stripVerdict(answer); strings.TrimSpace(stripped) != "" {
			answer = strings.TrimSpace(stripped)
		}
	}
	b.appendHistory(key, userText, answer)
	return answer, nil
}

// chatRespMsg 网关返回的 assistant 消息（含工具请求）。
type chatRespMsg struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

// doChat 单次 chat/completions 调用；tools 非空时带上工具定义。
func (b *Bridge) doChat(msgs []chatMsg, tools *toolRegistry) (chatRespMsg, error) {
	var zero chatRespMsg
	payload := map[string]any{
		"model":    b.cfg().Model,
		"messages": msgs,
		"stream":   false,
	}
	if tools != nil && len(tools.defs) > 0 {
		payload["tools"] = tools.defs
		payload["tool_choice"] = "auto"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return zero, err
	}
	url := b.cfg().GatewayBase + "/chat/completions"
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(b.cfg().TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.cfg().GatewayKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg().GatewayKey)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("gateway %d: %s", resp.StatusCode, truncateRunes(string(body), 200))
	}
	var out struct {
		Choices []struct {
			Message chatRespMsg `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, fmt.Errorf("解析网关响应: %w", err)
	}
	if len(out.Choices) == 0 {
		return zero, fmt.Errorf("网关返回空 choices")
	}
	return out.Choices[0].Message, nil
}

// buildMessages 组装请求消息：system + 历史 + 本轮。
func (b *Bridge) buildMessages(key string, uid int64, userText string) []chatMsg {
	return b.buildMessagesWithPrompt(key, uid, userText, "")
}

// buildMessagesWithPrompt 同 buildMessages，但可指定本次使用的人格 prompt
// （双人格切换时由调用方传入；空 = 用配置里的 SystemPrompt 兜底）。
func (b *Bridge) buildMessagesWithPrompt(key string, uid int64, userText, personaPrompt string) []chatMsg {
	out := make([]chatMsg, 0, 2*b.cfg().MaxHistoryPairs+2)
	system := strings.TrimSpace(personaPrompt)
	if system == "" {
		system = strings.TrimSpace(b.cfg().SystemPrompt)
	}
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
	fmt.Fprintf(&sb, "好感度 %d/100（%s）。", u.Affinity, AffinityTier(u.Affinity))
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

// stripVerdict 从可见回复里删掉「〔点评：…〕」整段（含包裹符号与多余空行）。
//
// 保留 extractVerdict 不动：点评仍然要生成、要入库，只是不再外发。
func stripVerdict(s string) string {
	openers := []string{"〔点评：", "〔点评:", "（点评：", "(点评:", "【点评】"}
	for _, op := range openers {
		for {
			i := strings.Index(s, op)
			if i < 0 {
				break
			}
			// 找对应的收尾符：括号类找配对的，【点评】找行尾
			var end int
			switch op {
			case "〔点评：", "〔点评:":
				end = strings.Index(s[i:], "〕")
			case "（点评：":
				end = strings.Index(s[i:], "）")
			case "(点评:":
				end = strings.Index(s[i:], ")")
			default:
				end = strings.IndexByte(s[i:], '\n')
			}
			if end < 0 {
				// 没有收尾符：从该位置截到行尾
				if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
					end = nl
				} else {
					end = len(s) - i
				}
			} else {
				end += len("〕") // 吃掉收尾符（长度 1~3 字节不等，下面按 rune 处理）
			}
			// 安全：按字节截断时回退到收尾符末尾
			cut := i + end
			if cut > len(s) {
				cut = len(s)
			}
			s = s[:i] + s[cut:]
		}
	}
	// 清理多余空行
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

// handlePoke 被戳一戳：按配置回一句 + 回戳（不消耗模型额度）。
func (b *Bridge) handlePoke(ev *Event) {
	cfg := b.cfg()
	b.state.note(RecentEvent{Time: time.Now(), Type: "poke", Group: ev.GroupID, User: ev.UserID, Action: "poked"})
	log.Printf("[qqbot] 被戳: group=%d user=%d（回戳=%v）", ev.GroupID, ev.UserID, cfg.PokeBack)
	if cfg.PokeBack {
		if err := b.sendPoke(ev); err != nil {
			log.Printf("[qqbot] poke back failed: %v", err)
		}
	}
	if strings.TrimSpace(cfg.PokeReply) != "" {
		if err := b.send(ev, cfg.PokeReply); err != nil {
			log.Printf("[qqbot] poke reply failed: %v", err)
		}
	}
}

// sendPoke 调 OneBot 的戳一戳动作。
func (b *Bridge) sendPoke(ev *Event) error {
	action := "friend_poke"
	payload := map[string]any{"user_id": ev.UserID}
	if ev.MessageType == "group" || ev.GroupID != 0 {
		action = "group_poke"
		payload["group_id"] = ev.GroupID
	}
	return b.callAction(action, payload)
}

// sendImage 发一张图（本地文件路径或 URL，NapCat 都接受）。
func (b *Bridge) sendImage(ev *Event, ref string) error {
	action := "send_private_msg"
	payload := map[string]any{"message": []map[string]any{
		{"type": "image", "data": map[string]string{"file": ref}},
	}}
	if ev.MessageType == "group" {
		action = "send_group_msg"
		payload["group_id"] = ev.GroupID
	} else {
		payload["user_id"] = ev.UserID
	}
	return b.callAction(action, payload)
}

// callAction 通用 OneBot 动作调用（send/poke 共用）。
func (b *Bridge) callAction(action string, payload map[string]any) error {
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
	if tok := b.cfg().OneBotToken; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var r struct {
		RetCode int    `json:"retcode"`
		Msg     string `json:"message"`
	}
	if json.Unmarshal(body, &r) == nil && r.RetCode != 0 {
		return fmt.Errorf("onebot %s retcode=%d msg=%s", action, r.RetCode, r.Msg)
	}
	return nil
}

// pickSticker 从表情包目录随机挑一张（目录为空返回空串）。
func (b *Bridge) pickSticker() string {
	dir := strings.TrimSpace(b.cfg().StickerDir)
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp":
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		return ""
	}
	return files[int(time.Now().UnixNano())%len(files)]
}

// matchesSticker 判断是不是"要库存表情"的**短指令**。
//
// 关键取舍：以前用子串匹配，结果"画一张鲸鱼娘表情包"也会命中"表情"→ 直接发库存图，
// 模型永远收不到"想让你画"的请求。现在改成**精确匹配**（允许首尾标点/空格），
// 并且凡是带"画"字的请求一律让给模型（它可以用 draw_sticker 工具现画）。
func (b *Bridge) matchesSticker(text string) bool {
	t := stripDecorations(text)
	if t == "" {
		return false
	}
	if strings.Contains(t, "画") {
		return false // 想画图 → 交给模型/工具，别用库存图糊弄
	}
	for _, kw := range b.cfg().StickerCommand {
		if t == stripDecorations(kw) {
			return true
		}
	}
	return false
}

// stripDecorations 去掉首尾空白与常见标点/表情符号，用于短指令的精确比较。
func stripDecorations(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Trim(s, " \t\n\r，。！？!?~～、,.…：:；;\"'“”‘’()（）[]【】")
}

// sendSticker 发一张随机表情包（目录空则用文字兜底）。
func (b *Bridge) sendSticker(ev *Event) {
	ref := b.pickSticker()
	if ref == "" {
		if err := b.send(ev, "本王没有表情包可发。"); err != nil {
			log.Printf("[qqbot] sticker fallback failed: %v", err)
		}
		return
	}
	if err := b.sendImage(ev, ref); err != nil {
		log.Printf("[qqbot] sticker send failed: %v", err)
	}
}

// otherAtTargets 过滤掉"@ 机器人自己"，只留被 @ 的其他人。
func otherAtTargets(at []int64, self int64) []int64 {
	out := make([]int64, 0, len(at))
	for _, uid := range at {
		if uid != 0 && uid != self {
			out = append(out, uid)
		}
	}
	return out
}

// personaSnapshot 供后台展示双人格状态。
func (b *Bridge) personaSnapshot() []PersonaView {
	if b.persona == nil {
		return nil
	}
	return b.persona.Snapshot()
}
