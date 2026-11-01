package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
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

	// 失败计数（原子）：模型调用失败 / 发送失败。
	// 之前只在日志里，出了问题只能翻文件；现在 /admin/api/state 直接能看。
	chatFailures atomic.Int64
	sendFailures atomic.Int64
	mem          *Memory // 按 QQ 号持久化的记忆（人设连续性）
	state        *adminState
	gate         *Gate
	botGuard     *BotGuard
	cfgPath      string
}

type chatMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// ImageURLs 本轮用户消息附带的图片（多模态）：非空时 content 会发成
	// [{type:text},{type:image_url}...] 数组，而不是纯字符串。
	ImageURLs []string `json:"-"`
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
	// 多模态：把图片真的喂给模型（最多 2 张，仅 http(s)/base64）。
	var visionURLs []string
	for _, u := range images {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") ||
			strings.HasPrefix(u, "base64://") || strings.HasPrefix(u, "data:") {
			visionURLs = append(visionURLs, u)
		}
		if len(visionURLs) >= 2 {
			break
		}
	}
	if len(images) > 0 {
		// 顺手收藏群聊表情（异步，失败静默；只存普通图片，mface 无图源会跳过）
		safeGo("collectStickers", func() { b.collectStickers(images) })
	}
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
			names = append(names, b.nickOf(uid))
		}
		text = text + "（本条还 @ 了：" + strings.Join(names, "、") +
			"，要提到他们就直呼其名，别念 QQ 号）"
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
			safeGo("hint", func() {
				if err := b.send(&ev, "在的，说点什么？也可以发 /help 看指令。"); err != nil {
					log.Printf("[qqbot] hint send failed: %v", err)
				}
			})
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
	// 单人格：恒用配置里的默认人格（病娇龙王；怒气/双人格切换已下线）
	prompt := b.cfg().PersonaDefaultPrompt
	b.state.note(RecentEvent{Time: time.Now(), Type: ev.MessageType, Group: ev.GroupID,
		User: ev.UserID, Nick: name, At: atBot, Runes: len([]rune(text)), Action: "replied"})
	safeGo("reply", func() { b.reply(&ev, key, name, text, atTargets, prompt, visionURLs) })
}

// allow 令牌桶限速：每用户每分钟 RatePerMinute 次（0 = 不限）。
func (b *Bridge) allow(userID int64) bool {
	if b.cfg().RatePerMinute <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	// 令牌桶表按 QQ 号增长，且永不复用 → 长期运行会一直累积。
	// 超过阈值时顺手清掉「10 分钟没动过」的桶（它们早已回满，删了等价于新建）。
	if len(b.limits) > 512 {
		for uid, old := range b.limits {
			if now.Sub(old.last) > 10*time.Minute {
				delete(b.limits, uid)
			}
		}
	}
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
func (b *Bridge) reply(ev *Event, key, name, text string, atTargets []int64, prompt string, images []string) {
	answer, err := b.chat(key, ev, text, atTargets, prompt, images)
	if err != nil {
		b.chatFailures.Add(1)
		log.Printf("[qqbot] chat failed key=%s user=%s: %v", key, name, err)
		answer = "（模型暂时不可用，稍后再试）"
	}
	answer = stripStageDirections(answer)
	answer = stripMarkdown(answer)
	// 含分享码的回复**不截断**：码被切掉一位就等于废码（用户反馈过 "6L…"）。
	if codePattern.MatchString(answer) {
		log.Printf("[qqbot] 回复含分享码，跳过截断（原长 %d 字）", len([]rune(answer)))
	} else {
		answer = truncateRunes(answer, b.cfg().MaxReplyRunes)
	}
	if strings.TrimSpace(answer) == "" {
		return
	}
	log.Printf("[qqbot] reply → %s: %s", key, truncateRunes(strings.ReplaceAll(answer, "\n", " / "), 120))
	if err := b.send(ev, answer); err != nil {
		b.sendFailures.Add(1)
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
func (b *Bridge) chat(key string, ev *Event, userText string, atTargets []int64, prompt string, images []string) (string, error) {
	uid := ev.UserID
	msgs := b.buildMessagesWithPrompt(key, uid, userText, prompt)
	if len(images) > 0 && len(msgs) > 0 {
		// 图片只挂在本轮 user 消息上（历史里不带，省 token）
		msgs[len(msgs)-1].ImageURLs = images
	}

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
		"messages": toPayloadMessages(msgs),
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

	persona := strings.TrimSpace(personaPrompt)
	if persona == "" {
		persona = strings.TrimSpace(b.cfg().SystemPrompt)
	}

	// 组装顺序很重要：**人设放最后**。
	// 实测：人设写在最前面时，模型会跟随后面的排版规则/工具说明，用"助手"口吻回答
	// （"我是你的三角洲行动小助手…"）；挪到末尾后依从性明显提高（近因效应）。
	// 顺序按**前缀缓存**排：静态内容（规则 + 人设）必须在前且逐字节稳定，
	// 易变内容（记忆里的"已见过 N 次"、知识库卡片）一律放最后。
	// 之前把记忆放最前，每轮数字一变就把整段前缀打废——实测缓存命中率只有 77.7%。
	var parts []string
	parts = append(parts, layoutGuide)
	if persona != "" {
		parts = append(parts, persona)
	}
	// 身份锚定：紧跟在人设之后，仍属静态前缀（逐字节稳定，不破坏前缀缓存）。
	// 简化提示词时删过这一段，实测依从性下降——模型会滑回"助手"口吻。
	parts = append(parts, "【身份锚定】你就是「龙王」，不是助手、不是机器人。以上所有规则都要用龙王的语气执行："+
		"自称本王、先损一句、黏人又暴躁；数据类问题照常调工具拿真数据，但说话方式必须是龙王。")
	if b.mem != nil {
		if memo := b.mem.Describe(uid); memo != "" {
			parts = append(parts, memo)
		}
	}
	if b.cfg().KBEnabled {
		if kb := b.kbContext(userText); kb != "" {
			parts = append(parts, kb)
		}
	}

	// 人设「提醒」拼进本轮 user 消息（模型对 user 指令依从性最高）。
	// 实测：只写在 system 里时，模型会跟随后面的工具/排版说明，用「助手」口吻回答；
	// 同样的内容放到 user 前缀，立刻恢复龙王腔（含本王/点评）。
	if persona != "" {
		// 完整人设提醒（截断到 160 rune）：模型对 user 前缀的依从性明显高于 system，
		// 只给"短句别演"这类标记时，前几轮还能装住，后面就滑回助手口吻。
		compact := truncateRunes(strings.ReplaceAll(persona, "\n", " "), 160)
		userText = "（人设提醒，务必遵守：" + compact + "）\n\n" + userText
	}

	system := strings.Join(parts, "\n\n")
	if system != "" {
		log.Printf("[qqbot] sys head=%q len=%d", truncateRunes(system, 40), len([]rune(system)))
		out = append(out, chatMsg{Role: "system", Content: system})
	}
	b.mu.Lock()
	// 会话表按「会话+用户」增长，只在被再次访问时才过期删除 → 顺手清一批长期不动的。
	if len(b.sessions) > 512 {
		ttl := time.Duration(b.cfg().SessionTTLMinutes) * time.Minute
		if ttl <= 0 {
			ttl = 30 * time.Minute
		}
		cut := time.Now().Add(-2 * ttl)
		for k, sess := range b.sessions {
			if sess.last.Before(cut) {
				delete(b.sessions, k)
			}
		}
	}
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

// mdBold 之类：把常见的 Markdown 记号剥掉。
//
// 为什么在代码里做：人设里写了"禁止 Markdown"，模型大多数时候遵守，但偶尔还是冒出
// **加粗**、反引号、# 标题 —— QQ 不支持富文本，这些会原样显示成噪音。与其指望模型
// 每次都记得，不如发送前硬剥一层（只剥记号，不动正文内容）。
var (
	mdBold         = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdItalic       = regexp.MustCompile(`(?m)(^|[^*])\*([^*\n]+)\*`)
	mdCode         = regexp.MustCompile("`([^`\n]+)`")
	mdHeading      = regexp.MustCompile(`(?m)^\s*#{1,6}\s*`)
	mdQuote        = regexp.MustCompile(`(?m)^\s*>\s?`)
	mdLink         = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\n]+)\)`)
	mdTableSep     = regexp.MustCompile(`(?m)^\s*\|?[\s:|-]{5,}\|?\s*$`)
	mdTableRow     = regexp.MustCompile(`(?m)^\s*\|.+\|\s*$`)
	mdTableSepOnly = regexp.MustCompile(`^[\s:|-]+$`)
	mdStrike       = regexp.MustCompile(`~~([^~\n]+)~~`)
)

// stripMarkdown 剥掉 Markdown 记号；链接保留"文字 (url)"的形式，信息不丢。
func stripMarkdown(s string) string {
	s = mdLink.ReplaceAllString(s, "$1 ($2)")
	s = mdBold.ReplaceAllString(s, "$1")
	s = mdStrike.ReplaceAllString(s, "$1")
	s = mdCode.ReplaceAllString(s, "$1")
	s = mdItalic.ReplaceAllString(s, "${1}$2")
	s = mdHeading.ReplaceAllString(s, "")
	s = mdQuote.ReplaceAllString(s, "")
	s = mdTableSep.ReplaceAllString(s, "")
	// 表格行：| 地图 | 密码 | → 地图 密码（多行仍是多行，只是去掉管道符）
	s = mdTableRow.ReplaceAllStringFunc(s, func(line string) string {
		cells := []string{}
		for _, c := range strings.Split(strings.TrimSpace(strings.Trim(line, "|")), "|") {
			c = strings.TrimSpace(c)
			if c != "" && !regexp.MustCompile(`^[:\- ]+$`).MatchString(c) {
				cells = append(cells, c)
			}
		}
		if len(cells) == 0 {
			return ""
		}
		return strings.Join(cells, "：")
	})
	return s
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
		return "嗤，一个本王毫无印象的蝼蚁。"
	}
	name := u.Nick
	if name == "" {
		name = "无名凡人"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "嗤，%s：见过 %d 次", name, u.Count)
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

// sendFace 发一个 QQ 原生表情（face 段，不占图片带宽）。
func (b *Bridge) sendFace(ev *Event, id string) error {
	action := "send_private_msg"
	payload := map[string]any{"message": []map[string]any{
		{"type": "face", "data": map[string]string{"id": id}},
	}}
	if ev.MessageType == "group" {
		action = "send_group_msg"
		payload["group_id"] = ev.GroupID
	} else {
		payload["user_id"] = ev.UserID
	}
	return b.callAction(action, payload)
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
// maxInlineImageBytes 走 base64 内联发送的图片上限（再大就退回文件路径）。
const maxInlineImageBytes = 8 << 20

func (b *Bridge) sendImage(ev *Event, ref string) error {
	// 本地文件优先走 base64 内联。
	// 为什么：NapCat 走文件路径发图时，QQ 侧 NodeIKernelMsgService/sendMsg 经常
	// 10s 不回调 → `Timeout: NTEvent ... sendMsg`，表现就是"图发不出去"
	// （线上实测：同一张图，路径发送超时，base64 正常）。
	if data, err := readLocalImage(ref); err == nil && len(data) > 0 && len(data) <= maxInlineImageBytes {
		ref = "base64://" + base64.StdEncoding.EncodeToString(data)
	}
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
	// 根目录图片 + 群聊里收藏的表情（扫描逻辑见 libraryStickers/collectedStickers）
	files := libraryStickers(dir)
	files = append(files, collectedStickers(dir)...)
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

// layoutGuide 排版规则（对所有回答生效）。
//
// 背景：QQ 不渲染 Markdown，但**换行有效**。之前把"六张图的密码"压成一句话，
// 群友得逐字找；分行后一眼扫完。规则只针对"列表类信息"，闲聊仍保持 1~3 句。
const layoutGuide = `【回复长度】
- 闲聊：**一句话，≤20 字**。不铺垫、不解释、不复述问题、不问「还需要什么」。
- 数据类（密码/改枪码/行情/活动）：照工具内容列清单，不加废话。

【排版】
- 纯文本，禁 Markdown（星号、反引号、井号、表格竖线）。
- 列表每行一条：名称：值。

【称呼】用群昵称或 QQ 昵称，别念 QQ 号。

【数据】密码/改枪码/行情必须调工具取真值；取不到就说取不到，别编。
【码必须完整】改枪码/分享码要**一位不差地原样输出**，禁止用「…」「等等」省略或缩写，
码少一位就导入失败。多套码就分行列全。

【表情】偶尔 send_face 或 emoji，别每句都发。`

// kkrbOverview 取 kkrb 概览数据（统一缓存入口，供各市场工具复用）。
func (b *Bridge) kkrbOverview() (*KkrbOverview, error) {
	if !b.cfg().KkrbEnabled {
		return nil, fmt.Errorf("实时数据源已关闭（kkrb_enabled=false）")
	}
	ttl := time.Duration(b.cfg().KkrbCacheMinutes) * time.Minute
	return kkrbSingleton.Overview(ttl)
}

// nickOf 取某个 QQ 的**称呼**（群昵称/QQ 昵称），取不到就给个模糊说法。
// 用户明确要求：不要念别人的 QQ 号。
func (b *Bridge) nickOf(uid int64) string {
	if b.mem != nil {
		if u, ok := b.mem.Lookup(uid); ok && strings.TrimSpace(u.Nick) != "" {
			return u.Nick
		}
	}
	return "那位"
}

// toPayloadMessages 把内部 chatMsg 转成请求体消息：
// 带图的那条 user 消息展开成 OpenAI 多模态数组（text + image_url），其余原样。
func toPayloadMessages(msgs []chatMsg) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		item := map[string]any{"role": m.Role}
		if len(m.ImageURLs) > 0 {
			parts := []map[string]any{}
			if strings.TrimSpace(m.Content) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, u := range m.ImageURLs {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": u},
				})
			}
			item["content"] = parts
		} else {
			item["content"] = m.Content
		}
		if len(m.ToolCalls) > 0 {
			item["tool_calls"] = m.ToolCalls
		}
		if m.ToolCallID != "" {
			item["tool_call_id"] = m.ToolCallID
		}
		out = append(out, item)
	}
	return out
}

// stageLine 整行都是（…）或(…)——那种"（把下巴搁在你肩头，声音闷闷的）"的动作描写。
var stageLine = regexp.MustCompile(`^\s*[（(][^）)]{0,200}[）)]\s*$`)

// stripStageDirections 删掉整行的括号动作描写，只留对话。
//
// 用户明确不要这种"设定/神态"部分：龙王说话就说话，别演。
// 只删**整行**括号，行内的（4100 费）这类说明保留。
func stripStageDirections(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if stageLine.MatchString(ln) {
			continue
		}
		out = append(out, ln)
	}
	res := strings.Join(out, "\n")
	// 删行后可能留下连续空行，压成一个
	for strings.Contains(res, "\n\n\n") {
		res = strings.ReplaceAll(res, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(res)
}

// codePattern 匹配游戏分享码（16+ 位大写字母数字，且含字母）。
var codePattern = regexp.MustCompile(`\b[A-Z0-9]{14,}\b`)

// safeGo 起一个**带 recover** 的 goroutine。
//
// 为什么必须有：回复链路会走模型解析、工具执行（含 exec 图表渲染、KB 读盘）、
// 图片/语音发送——任何一处 panic，裸 `go` 都会让整个桥进程退出。QQ 桥是常驻服务，
// 一次畸形响应就够把机器人打死；这里选择丢这一条消息并留下堆栈。
func safeGo(what string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[qqbot] panic in %s: %v\n%s", what, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// readLocalImage 读本地图片文件；ref 是 URL 或非本地路径时返回错误（调用方保持原样发送）。
func readLocalImage(ref string) ([]byte, error) {
	if strings.Contains(ref, "://") {
		return nil, fmt.Errorf("not a local path")
	}
	st, err := os.Stat(ref)
	if err != nil || st.IsDir() || st.Size() > maxInlineImageBytes {
		return nil, fmt.Errorf("not inlineable")
	}
	return os.ReadFile(ref)
}
