package qqbot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestParseTextArray 数组格式：文本段拼接、@ 机器人识别、非文本段丢弃。
func TestParseTextArray(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"at","data":{"qq":"190839970"}},
		{"type":"text","data":{"text":" 你好"}},
		{"type":"face","data":{"id":"1"}},
		{"type":"text","data":{"text":"，讲个笑话"}}
	]`)
	text, at := ParseText(raw, "", 190839970)
	if !at {
		t.Error("@机器人 应被识别")
	}
	if text != "你好，讲个笑话" {
		t.Errorf("text=%q", text)
	}
}

// TestParseTextString 字符串格式：剥 CQ 码、识别 @。
func TestParseTextString(t *testing.T) {
	text, at := ParseText(json.RawMessage(`"[CQ:at,qq=190839970] 在吗"`), "[CQ:at,qq=190839970] 在吗", 190839970)
	if !at {
		t.Error("CQ 码形态的 @ 应被识别")
	}
	if text != "在吗" {
		t.Errorf("text=%q", text)
	}
}

// TestParseTextOtherAt 别人被 @ 不算（否则群里任何 @ 都会触发）。
func TestParseTextOtherAt(t *testing.T) {
	raw := json.RawMessage(`[{"type":"at","data":{"qq":"123456"}},{"type":"text","data":{"text":"hi"}}]`)
	_, at := ParseText(raw, "", 190839970)
	if at {
		t.Error("别人的 @ 不应触发")
	}
}

func TestCommand(t *testing.T) {
	cases := map[string]bool{
		"/help":       true,
		"/CLEAR":      true,
		"/model":      true,
		"/ping":       true,
		"/new":        true,
		"你好":          false,
		"/unknown":    false,
		"":            false,
		" /clear ":    true,
		"/clear 多余参数": true,
	}
	for in, want := range cases {
		_, _, ok := Command(in)
		if ok != want {
			t.Errorf("Command(%q)=%v want %v", in, ok, want)
		}
	}
}

// TestWhitelistDenyByDefault 空白名单 = 谁都不回（安全默认）。
func TestWhitelistDenyByDefault(t *testing.T) {
	c := Default()
	if c.AllowsGroup(123) || c.AllowsUser(456) {
		t.Fatal("默认配置不应放行任何群/人")
	}
	c.AllowedGroups = []int64{123}
	c.AllowedUsers = []int64{456}
	if !c.AllowsGroup(123) || c.AllowsGroup(999) {
		t.Error("群白名单判定错误")
	}
	if !c.AllowsUser(456) || c.AllowsUser(999) {
		t.Error("私聊白名单判定错误")
	}
}

// TestHistoryTrim 历史按 MaxHistoryPairs 轮裁剪（保留最近 N 轮）。
func TestHistoryTrim(t *testing.T) {
	cfg := Default()
	cfg.MaxHistoryPairs = 2
	b := New(cfg)
	for i := 0; i < 5; i++ {
		b.appendHistory("group:1", "q", "a")
	}
	b.mu.Lock()
	got := len(b.sessions["group:1"].msgs)
	b.mu.Unlock()
	if got != 4 { // 2 轮 = 4 条
		t.Fatalf("历史条数=%d want 4", got)
	}
}

// TestHistoryTTL 会话闲置过期后按新会话处理。
func TestHistoryTTL(t *testing.T) {
	cfg := Default()
	cfg.MaxHistoryPairs = 3
	cfg.SessionTTLMinutes = 1
	b := New(cfg)
	b.appendHistory("group:1", "q", "a")
	b.mu.Lock()
	b.sessions["group:1"].last = time.Now().Add(-2 * time.Minute)
	b.mu.Unlock()

	msgs := b.buildMessages("group:1", 1001, "新问题")
	// 忽略 system（人设+排版规则+记忆总会有），只看对话消息：应只剩本轮 user
	var dialog []chatMsg
	for _, m := range msgs {
		if m.Role != "system" {
			dialog = append(dialog, m)
		}
	}
	if len(dialog) != 1 || dialog[0].Content != "新问题" {
		t.Fatalf("过期会话应清空历史，得到 %+v", msgs)
	}
}

// TestRateLimit 限速：每用户每分钟 N 次，用完后拒绝。
func TestRateLimit(t *testing.T) {
	cfg := Default()
	cfg.RatePerMinute = 2
	b := New(cfg)
	if !b.allow(1) || !b.allow(1) {
		t.Fatal("前两次应放行")
	}
	if b.allow(1) {
		t.Fatal("第三次应被限速")
	}
	if !b.allow(2) {
		t.Fatal("不同用户互不影响")
	}
}

// TestHandleEventEndToEnd 端到端：模拟 OneBot 事件 → 调网关 → 调 OneBot API 发消息。
//
// 两个 httptest 分别扮演网关和 OneBot API，验证：
//  1. 白名单外不触发任何外呼；
//  2. 白名单内 + @ 机器人 → 网关收到正确的 model/messages，OneBot 收到 send_group_msg；
//  3. 群里没 @ 机器人 → 不回。
func TestHandleEventEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var gwCalls int
	var gotModel string
	var sent []string

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gwCalls++
		mu.Unlock()
		var req struct {
			Model    string    `json:"model"`
			Messages []chatMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		gotModel = req.Model
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"这是模型的回答"}}]}`)
	}))
	defer gw.Close()

	ob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, r.URL.Path+" "+string(body))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0}`)
	}))
	defer ob.Close()

	cfg := Default()
	cfg.Enabled = true
	cfg.GatewayBase = gw.URL
	cfg.OneBotAPI = ob.URL
	cfg.Model = "cn:test-model"
	cfg.ProfileEnabled = false
	cfg.AllowedGroups = []int64{992511553}
	cfg.RequireAtInGroup = true
	b := New(cfg)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	post := func(body string) {
		resp, err := http.Post(srv.URL+"/onebot", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		resp.Body.Close()
	}

	// 1) 白名单外的群：不发任何外呼
	post(`{"post_type":"message","message_type":"group","group_id":111,"user_id":1,"self_id":190839970,
		"message":[{"type":"at","data":{"qq":"190839970"}},{"type":"text","data":{"text":"你好"}}]}`)
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	if gwCalls != 0 || len(sent) != 0 {
		mu.Unlock()
		t.Fatalf("白名单外不应有任何外呼：gw=%d sent=%d", gwCalls, len(sent))
	}
	mu.Unlock()

	// 2) 白名单内但没 @：不回
	post(`{"post_type":"message","message_type":"group","group_id":992511553,"user_id":1,"self_id":190839970,
		"message":[{"type":"text","data":{"text":"路过"}}]}`)
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	if gwCalls != 0 {
		mu.Unlock()
		t.Fatalf("没 @ 机器人不应触发网关调用")
	}
	mu.Unlock()

	// 3) 白名单内 + @：走完整链路
	post(`{"post_type":"message","message_type":"group","group_id":992511553,"user_id":1,"self_id":190839970,
		"sender":{"card":"测试员"},"message":[{"type":"at","data":{"qq":"190839970"}},{"type":"text","data":{"text":"讲个笑话"}}]}`)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(sent) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if gwCalls != 1 {
		t.Fatalf("网关应被调用 1 次，实际 %d", gwCalls)
	}
	if gotModel != "cn:test-model" {
		t.Errorf("model=%q want cn:test-model", gotModel)
	}
	if len(sent) != 1 {
		t.Fatalf("OneBot 应收到 1 次发送，实际 %d", len(sent))
	}
	if !strings.Contains(sent[0], "/send_group_msg") {
		t.Errorf("应调 send_group_msg，实际 %s", sent[0])
	}
	if !strings.Contains(sent[0], "这是模型的回答") {
		t.Errorf("回复内容应透传，实际 %s", sent[0])
	}
}

// TestDisabledReturns503 关闭时 /onebot 返回 503（灰度开关生效）。
func TestDisabledReturns503(t *testing.T) {
	cfg := Default() // Enabled=false
	b := New(cfg)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/onebot", "application/json", strings.NewReader(`{"post_type":"message"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

// TestTruncateRunes 截断按字符，不切坏 UTF-8。
func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("中文中文中文", 3); got != "中文中…" {
		t.Errorf("got %q", got)
	}
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Errorf("got %q", got)
	}
}

// 「监听所有消息」的群 + 忽略名单（别的 bot / 旧 bot，避免 bot 互回成环）。
func TestRequiresAtAndIgnores(t *testing.T) {
	c := Default()
	c.AllowedGroups = []int64{992511553, 414452720}
	c.NoAtGroups = []int64{992511553}
	c.IgnoredUsers = []int64{190839970}

	if c.RequiresAt(992511553) {
		t.Error("no_at_groups 里的群不应要求 @")
	}
	if !c.RequiresAt(414452720) {
		t.Error("其他群仍应要求 @")
	}
	if !c.Ignores(190839970) {
		t.Error("忽略名单未生效")
	}
	if c.Ignores(941628696) {
		t.Error("普通用户不应被忽略")
	}
	// 全局关掉 require_at 时，所有群都不要求
	c.RequireAtInGroup = false
	if c.RequiresAt(414452720) {
		t.Error("全局关闭后不应要求 @")
	}
}

// 忽略名单里的账号发言不触发任何外呼（防 bot 互回）。
func TestIgnoredUserNoReply(t *testing.T) {
	var calls int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"x"}}]}`)
	}))
	defer gw.Close()
	cfg := Default()
	cfg.Enabled = true
	cfg.GatewayBase = gw.URL
	cfg.OneBotAPI = gw.URL
	cfg.AllowedGroups = []int64{992511553}
	cfg.NoAtGroups = []int64{992511553}
	cfg.IgnoredUsers = []int64{190839970}
	b := New(cfg)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	body := `{"post_type":"message","message_type":"group","group_id":992511553,"user_id":190839970,
		"self_id":3467208422,"message":[{"type":"text","data":{"text":"我是旧 bot"}}]}`
	resp, err := http.Post(srv.URL+"/onebot", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(200 * time.Millisecond)
	if calls != 0 {
		t.Fatalf("忽略名单内的账号不应触发网关调用，实际 %d", calls)
	}
}

// 记忆：观察→描述→落盘→重开仍在（人设连续性靠它）。
func TestMemoryObserveDescribePersist(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/users.json"
	m, err := OpenMemory(path, 3)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	m.Observe(941628696, "怪我咯", "龙王你昨天骂我")
	m.Observe(941628696, "怪我咯", "今天还骂吗")
	m.Observe(1002, "别人", "我是路人")

	d := m.Describe(941628696)
	for _, want := range []string{"怪我咯", "941628696", "2 次", "昨天骂我"} {
		if !strings.Contains(d, want) {
			t.Errorf("记忆片段缺 %q: %s", want, d)
		}
	}
	if got := m.Describe(9999); got != "" {
		t.Errorf("陌生人不该有记忆，得到 %q", got)
	}
	if err := m.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	m2, err := OpenMemory(path, 3)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if m2.Count() != 2 {
		t.Fatalf("重开后用户数=%d want 2", m2.Count())
	}
	if d2 := m2.Describe(941628696); !strings.Contains(d2, "昨天骂我") {
		t.Errorf("重开后记忆丢失: %s", d2)
	}
}

// 记忆条数按 notesPerUser 裁剪（保留最近 N 句）。
func TestMemoryNotesTrim(t *testing.T) {
	m, _ := OpenMemory("", 2)
	for i := 0; i < 5; i++ {
		m.Observe(1, "u", "第"+string(rune('0'+i))+"句")
	}
	d := m.Describe(1)
	if strings.Contains(d, "第0句") || strings.Contains(d, "第2句") {
		t.Errorf("旧记忆应被裁掉: %s", d)
	}
	if !strings.Contains(d, "第4句") {
		t.Errorf("最新记忆应保留: %s", d)
	}
}

// 按人隔离：同一个群里两个人各自一段上下文（不串味）。
func TestChatKeyPerUser(t *testing.T) {
	a := &Event{MessageType: "group", GroupID: 992511553, UserID: 1}
	b := &Event{MessageType: "group", GroupID: 992511553, UserID: 2}
	if chatKey(a, true) == chatKey(b, true) {
		t.Error("per_user 模式下同群不同人应有不同会话键")
	}
	if chatKey(a, false) != chatKey(b, false) {
		t.Error("per_chat 模式下同群应共享会话键")
	}
	if chatKey(a, true) != "group:992511553:u:1" {
		t.Errorf("会话键格式不符: %s", chatKey(a, true))
	}
}

// 激活/静默是**房间级**：A 激活后，B 在同一群里说话也应被受理。
// 回归背景：早期闸门键含 uid，A 喊了激活词、B 再说话仍是 idle，看起来像"激活了没反应"。
func TestGateIsRoomScoped(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"嗤。"}}]}`)
	}))
	defer gw.Close()
	ob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0}`)
	}))
	defer ob.Close()

	cfg := Default()
	cfg.Enabled = true
	cfg.GatewayBase = gw.URL
	cfg.OneBotAPI = ob.URL
	cfg.MemoryFile = ""
	cfg.ProfileEnabled = false
	cfg.ToolsEnabled = false
	cfg.AllowedGroups = []int64{992511553}
	cfg.NoAtGroups = []int64{992511553}
	cfg.ActivationKeywords = []string{"小八嘎"}
	b := New(cfg)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	post := func(uid int64, nick, text string) {
		body := fmt.Sprintf(`{"post_type":"message","message_type":"group","group_id":992511553,
			"user_id":%d,"self_id":3467208422,"sender":{"nickname":%q},
			"message":[{"type":"text","data":{"text":%q}}]}`, uid, nick, text)
		resp, err := http.Post(srv.URL+"/onebot", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		resp.Body.Close()
	}

	post(1001, "甲", "小八嘎") // A 激活房间
	time.Sleep(300 * time.Millisecond)
	post(1002, "乙", "今天吃啥") // B 说话：房间已激活 → 应受理
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("房间激活后，另一个用户的消息也应触发回复，实际只调用 %d 次", calls)
	}
}
