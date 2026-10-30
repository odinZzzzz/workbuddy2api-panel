package upstream

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 渠道校验三件套的钉子测试（issue: 400 code=11128 "conversation id missing"）。
//
// 上游要求 X-Conversation-ID / X-Conversation-Request-ID / X-Conversation-Message-ID
// 三者齐全，缺任一头即按「渠道校验不通过」拦截（displayTips「这次调用没通过渠道校验，
// 可能不是官方客户端发出的」）。本文件钉两件事：
//   1. chat 出站**恒**发全三件套——包括零值 ChatMeta{}（blackcat/autotask 的实际形态）；
//   2. 爆炸半径——refresh / billing 等非 chat 路径**永不**携带，避免头族外溢。
//
// 形态：三层 ID 逐位对齐官方客户端（实测 app.asar:/main/conversations.js 与
// @genie/agent-harness shared.cjs）——
//   Conversation-ID        带横线 UUID v4（客户端 session.id）
//   Conversation-Request-ID 32 hex UUID v7（客户端 mintConversationRequestId）
//   Message-ID = X-Request-ID 32 hex UUID v4（客户端 generateUUUID 去横线）
// 为什么必须钉：客户端的 isCanonicalConversationRequestId 用
// ^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$ 校验服务端回显的 conv-req，
// 不合规即丢弃重造（resolveConversationRequestId）——形状错会真的改行为。

const (
	hdrConvID    = "X-Conversation-ID"
	hdrConvReqID = "X-Conversation-Request-ID"
	hdrConvMsgID = "X-Conversation-Message-ID"
)

// id32Hex 钉 ID 形态：32 位小写 hex（session.NewMessageID 的契约）。
var id32Hex = regexp.MustCompile("^[0-9a-f]{32}$")

// 三层 ID 的精确形态（与官方客户端逐位一致，见文件头注释）。
var (
	reConvReqV7Hex32 = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
	reConvMsgV4Hex32 = regexp.MustCompile(`^[0-9a-f]{12}4[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
	reConvIDUUIDv4   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// trioOf 取出三件套的值，便于逐项断言。
func trioOf(t *testing.T, h http.Header) map[string]string {
	t.Helper()
	return map[string]string{
		hdrConvID:    h.Get(hdrConvID),
		hdrConvReqID: h.Get(hdrConvReqID),
		hdrConvMsgID: h.Get(hdrConvMsgID),
	}
}

// assertTrioPresent 断言三件套**齐全且非空**——这是渠道校验的硬要求，
// 也是本次修复的核心不变量（缺失即 400 code=11128）。
//
// 形态断言只施加于「本网关自己生成的」头（Message-ID 恒为现造 32 hex）；
// Conversation-ID / Request-ID 允许透传客户端原值，其形态由客户端决定，
// 断言 32 hex 会误伤合法透传（实测：客户端给 "conv-abc" 是常态）。
func assertTrioPresent(t *testing.T, h http.Header, context string) {
	t.Helper()
	for _, name := range []string{hdrConvID, hdrConvReqID, hdrConvMsgID} {
		if v := h.Get(name); v == "" {
			t.Fatalf("%s: %s 缺失（渠道校验会以 11128 conversation id missing 拦截）", context, name)
		}
	}
	if v := h.Get(hdrConvMsgID); !id32Hex.MatchString(v) {
		t.Errorf("%s: %s = %q，期望现造 32 位 hex", context, hdrConvMsgID, v)
	}
}

// assertTrioMinted 断言三件套齐全**且形态各自对齐官方客户端**——
// 仅在调用方未提供任何会话身份（零值 meta）时成立：
//
//	Conversation-ID         带横线 v4
//	Conversation-Request-ID 32 hex v7
//	Message-ID              32 hex v4
func assertTrioMinted(t *testing.T, h http.Header, context string) {
	t.Helper()
	want := []struct {
		name string
		re   *regexp.Regexp
	}{
		{hdrConvID, reConvIDUUIDv4},
		{hdrConvReqID, reConvReqV7Hex32},
		{hdrConvMsgID, reConvMsgV4Hex32},
	}
	seen := map[string]string{}
	for _, w := range want {
		v := h.Get(w.name)
		if v == "" {
			t.Fatalf("%s: %s 缺失（渠道校验会以 11-128 conversation id missing 拦截）", context, w.name)
		}
		if !w.re.MatchString(v) {
			t.Errorf("%s: %s = %q，形态与官方客户端不符", context, w.name, v)
		}
		if prev, dup := seen[v]; dup {
			t.Errorf("%s: %s 与 %s 同值（%q）——三层 ID 必须互异", context, w.name, prev, v)
		}
		seen[v] = w.name
	}
}

// TestChatHeadersAlwaysSendsConversationTrio 是本次修复的核心钉子。
//
// 零值 ChatMeta{} 是 blackcat.RunNightChats 与 panel.runModelChat 的实际调用形态；
// 修复前 X-Conversation-ID 走「空则不发」分支，这两条路径必然缺头被拦。
func TestChatHeadersAlwaysSendsConversationTrio(t *testing.T) {
	cases := []struct {
		name string
		meta ChatMeta
	}{
		{"零值 meta（blackcat/autotask 形态）", ChatMeta{}},
		{"仅 ConversationID", ChatMeta{ConversationID: "conv-abc"}},
		{"仅 SessionKey", ChatMeta{SessionKey: "sess-xyz"}},
		{"仅 ConversationRequestID", ChatMeta{ConversationRequestID: "0123456789abcdef0123456789abcdef"}},
		{"全量", ChatMeta{ConversationID: "conv-abc", SessionKey: "sess-xyz", ConversationRequestID: "0123456789abcdef0123456789abcdef", TraceID: "trace-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{}
			req := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", nil)
			c.ChatHeaders(req, &auth.Auth{AccessToken: "at", UID: "u1"}, "", tc.meta)
			// 零值 meta 时三层 ID 全部现造；其余形态至少保证齐全（透传值形态不限）。
			if tc.meta.ConversationID == "" && tc.meta.SessionKey == "" && tc.meta.ConversationRequestID == "" {
				assertTrioMinted(t, req.Header, tc.name)
				return
			}
			assertTrioPresent(t, req.Header, tc.name)
		})
	}
}

// TestChatHeadersConversationIDPrefersPassthrough 钉兜底优先级：透传值优先，
// 不伪造覆盖客户端原值（否则会破坏客户端自己的多轮会话聚合）。
func TestChatHeadersConversationIDPrefersPassthrough(t *testing.T) {
	c := &Client{}

	// ConversationID 优先于 SessionKey。
	req := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", nil)
	c.ChatHeaders(req, &auth.Auth{AccessToken: "at", UID: "u1"}, "", ChatMeta{ConversationID: "client-conv", SessionKey: "sess-key"})
	if got := req.Header.Get(hdrConvID); got != "client-conv" {
		t.Errorf("ConversationID 应优先透传，got %q", got)
	}

	// 无 ConversationID 时回落 SessionKey：**稳定派生**成带横线 v4，不再直发 sessKey
	// 原串——那是网关自编键（形状不受控），而官方客户端该头恒为带横线 v4 的 session id。
	req2 := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", nil)
	c.ChatHeaders(req2, &auth.Auth{AccessToken: "at", UID: "u1"}, "", ChatMeta{SessionKey: "sess-key"})
	got2 := req2.Header.Get(hdrConvID)
	if !reConvIDUUIDv4.MatchString(got2) {
		t.Errorf("无 ConversationID 时应派生带横线 v4，got %q", got2)
	}
	if got2 == "sess-key" {
		t.Error("不应再把 SessionKey 原串直接当 Conversation-ID")
	}
	// 同键再出站一次必须同值（会话级稳定的契约不变）。
	req3 := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", nil)
	c.ChatHeaders(req3, &auth.Auth{AccessToken: "at", UID: "u1"}, "", ChatMeta{SessionKey: "sess-key"})
	if got3 := req3.Header.Get(hdrConvID); got3 != got2 {
		t.Errorf("同 SessionKey 派生值应稳定：%q != %q", got3, got2)
	}
}

// TestChatHeadersConversationIDStableAcrossCalls 钉「会话级稳定」：同一 SessionKey
// 的多次出站（换号/重试/多轮）必须复用同一 Conversation-ID——这正是与
// ConversationRequestID（轮级）/ Message-ID（消息级）的语义差异所在。
func TestChatHeadersConversationIDStableAcrossCalls(t *testing.T) {
	c := &Client{}
	var first string
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", nil)
		c.ChatHeaders(req, &auth.Auth{AccessToken: "at", UID: "u1"}, "", ChatMeta{SessionKey: "sess-stable"})
		got := req.Header.Get(hdrConvID)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("第 %d 次出站 Conversation-ID 漂移：%q != %q", i+1, got, first)
		}
	}
}

// TestConversationTrioStaysOffNonChatEndpoints 是爆炸半径钉子：refresh / billing
// 等非 chat 路径不得携带三件套。头族外溢会让上游把这些请求也按对话渠道记账，
// 并可能触发按对话维度的风控聚合。
func TestConversationTrioStaysOffNonChatEndpoints(t *testing.T) {
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", UID: "u1"}
	c := &Client{}

	paths := map[string]func(*http.Request){
		"refresh": func(r *http.Request) { c.RefreshHeaders(r, a) },
		"billing": func(r *http.Request) { c.BillingHeaders(r, a) },
		"common":  func(r *http.Request) { c.CommonHeaders(r, a) },
	}
	for name, inject := range paths {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			inject(req)
			for _, h := range []string{hdrConvID, hdrConvReqID, hdrConvMsgID} {
				if v := req.Header.Get(h); v != "" {
					t.Errorf("%s 路径不应携带 %s，got %q", name, h, v)
				}
			}
		})
	}
}

// TestDesktopChatWithExpertSendsTrio 钉 desktop.go 的手搓头路径。
// 该路径不走 ChatHeaders，此前只设 X-Conversation-ID，缺 Request-ID / Message-ID。
func TestDesktopChatWithExpertSendsTrio(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"srv-req-1\"}\n\n"))
	}))
	defer srv.Close()

	c := &Client{ChatBaseCN: srv.URL, HTTP: srv.Client()}
	_, _, _ = c.DesktopChatWithExpert(&auth.Auth{AccessToken: "at", UID: "u1"}, "")

	if got == nil {
		t.Fatal("服务端未收到请求")
	}
	// 本路径三层 ID 全部现造，且形态对齐官方客户端（不再有 wb2api-conv-<纳秒>）。
	assertTrioMinted(t, got, "DesktopChatWithExpert")
	// Message-ID 与 X-Request-ID 必须同值（客户端一次生成、两个头复用）。
	if msg, reqID := got.Get(hdrConvMsgID), got.Get("X-Request-ID"); msg != reqID {
		t.Errorf("X-Conversation-Message-ID 应与 X-Request-ID 同值：%q vs %q", msg, reqID)
	}
	// Conversation-ID 与 Request-ID 必须是两个不同的值（客户端几乎不会让它们相等）。
	if got.Get(hdrConvID) == got.Get(hdrConvReqID) {
		t.Error("Conversation-ID 与 Conversation-Request-ID 不应同值")
	}
}
