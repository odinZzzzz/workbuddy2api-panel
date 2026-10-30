package session

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// imagePartSig 计算 part 原文摘要（sha256 前 8 hex），供期望值精确构造——
// 签名算法变更时测试期望值随此 helper 单点同步。
func imagePartSig(t *testing.T, part string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(part))
	return hex.EncodeToString(sum[:4])
}

// TestResolveConversationID 覆盖 conversationId 提取的 snake/camel/缺失三态：
//   - metadata.conversation_id / metadata.conversationId → 取值
//   - 顶层 conversation_id / conversationId → 取值（snake 优先同 ExtractKey）
//   - 缺失 / 只有 user_id → ""（会话头族语义只认对话 ID，绝不回落 user_id）
func TestResolveConversationID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"metadata snake_case", `{"metadata":{"conversation_id":"conv-1"}}`, "conv-1"},
		{"metadata camelCase", `{"metadata":{"conversationId":"conv-2"}}`, "conv-2"},
		{"top-level snake_case", `{"conversation_id":"conv-3"}`, "conv-3"},
		{"top-level camelCase", `{"conversationId":"conv-4"}`, "conv-4"},
		{"snake wins over camel", `{"conversation_id":"conv-s","conversationId":"conv-c"}`, "conv-s"},
		{"missing", `{"model":"glm-5.2"}`, ""},
		{"empty body", ``, ""},
		{"broken json", `{broken`, ""},
		{"metadata user_id only", `{"metadata":{"user_id":"u1"}}`, ""},
		{"top-level user_id only", `{"user_id":"u1"}`, ""},
		{"empty string value", `{"conversationId":""}`, ""},
		{"non-string value", `{"conversationId":123}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveConversationID([]byte(c.body)); got != c.want {
				t.Errorf("ResolveConversationID(%q) = %q want %q", c.body, got, c.want)
			}
		})
	}
}

// TestNewMessageIDFormat 32 位 hex 且不为空、两次调用大概率不同（随机性冒烟）。
func TestNewMessageIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := NewMessageID()
		if len(id) != 32 {
			t.Fatalf("NewMessageID() = %q len=%d want 32", id, len(id))
		}
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				t.Fatalf("NewMessageID() = %q has non-hex char %q", id, ch)
			}
		}
	}
}

// TestRequestIDForKeyStability 同 key 恒稳定、异 key 各不同、空 key 每次新值。
func TestRequestIDForKeyStability(t *testing.T) {
	// 预热清空包级缓存，避免其他测试污染 key（测试隔离）。
	k1a := RequestIDForKey("conv-a")
	k1b := RequestIDForKey("conv-a")
	if k1a != k1b {
		t.Errorf("same key should be stable: %q vs %q", k1a, k1b)
	}
	k2 := RequestIDForKey("conv-b")
	if k1a == k2 {
		t.Errorf("different keys should differ: %q", k1a)
	}
	// 空 key：每次调用生成新值（无会话则无"会话内稳定"语义）。
	e1 := RequestIDForKey("")
	e2 := RequestIDForKey("")
	if e1 == e2 {
		t.Errorf("empty key should yield fresh values each call: %q", e1)
	}
	// 稳定值自身也须是 32 hex（可作 B3 TraceId 直接使用）。
	for _, id := range []string{k1a, k2, e1} {
		if len(id) != 32 {
			t.Errorf("RequestIDForKey value %q len=%d want 32", id, len(id))
		}
	}
}

// TestTurnKeyExtraction 轮级兜底键的提取：取**最后一条** user 消息的「序号+文本」，
// 轮内追加 assistant/tool 消息不改变键；无 user / 无文本 / 坏 JSON 一律空串。
func TestTurnKeyExtraction(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"single user", `{"messages":[{"role":"user","content":"你好"}]}`, "u0:你好"},
		{"last user wins", `{"messages":[{"role":"user","content":"第一问"},{"role":"assistant","content":"答"},{"role":"user","content":"第二问"}]}`, "u2:第二问"},
		// agent 多步：轮内追加 assistant/tool 消息，末条 user 位置与内容不变 → 同键。
		{"agent step keeps same key", `{"messages":[{"role":"user","content":"任务"},{"role":"assistant","tool_calls":[{"id":"c1"}]},{"role":"tool","content":"结果"}]}`, "u0:任务"},
		{"multimodal parts text joined", `{"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, "u0:看图\n[image_url:" + imagePartSig(t, `{"type":"image_url","image_url":{"url":"data:x"}}`) + "]"},
		{"no user message", `{"messages":[{"role":"system","content":"sys"}]}`, ""},
		{"empty messages", `{"messages":[]}`, ""},
		{"messages key absent", `{"model":"glm-5.2"}`, ""},
		{"broken json", `{broken`, ""},
		{"empty body", ``, ""},
		{"empty content", `{"messages":[{"role":"user","content":""}]}`, ""},
		{"null content", `{"messages":[{"role":"user","content":null}]}`, ""},
		// 纯图片 content 按内容签名派生非空轮级键（G1 修复，原为 ""）；键含 image part 摘要。
		{"image only content", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, "u0:[image_url:" + imagePartSig(t, `{"type":"image_url","image_url":{"url":"x"}}`) + "]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TurnKey([]byte(c.body)); got != c.want {
				t.Errorf("TurnKey(%q) = %q want %q", c.body, got, c.want)
			}
		})
	}
}

// TestTurnRequestIDDerivation 轮级 ID 是纯派生：同键恒同值、异键异值、空键每次新值、
// 形态恒 32 hex。
func TestTurnRequestIDDerivation(t *testing.T) {
	a1 := TurnRequestID("u0:同一个问题")
	a2 := TurnRequestID("u0:同一个问题")
	if a1 != a2 {
		t.Errorf("same turn key should derive same id: %q vs %q", a1, a2)
	}
	if b := TurnRequestID("u0:另一个问题"); b == a1 {
		t.Errorf("different turn keys should derive different ids: %q", b)
	}
	// 同文本但序号不同（不同轮里内容相同的提问）也要分开。
	if c := TurnRequestID("u2:同一个问题"); c == a1 {
		t.Errorf("same text at different position should differ: %q", c)
	}
	// 空键：无轮可聚合 → 每次新值（保持原有请求级独立行为）。
	if e1, e2 := TurnRequestID(""), TurnRequestID(""); e1 == e2 {
		t.Errorf("empty turn key should yield fresh values each call: %q", e1)
	}
	for _, id := range []string{a1, TurnRequestID(""), TurnRequestID("x")} {
		if len(id) != 32 {
			t.Errorf("TurnRequestID value %q len=%d want 32", id, len(id))
		}
	}
}

// ---------------------------------------------------------------- ID 形态 ----

// 官方客户端的三条形态正则（实测自 app.asar:/main/conversations.js）：
//   - conv-req canonical：32 hex 小写 UUID v7
//   - message / request id：32 hex 小写 UUID v4
//   - conv-id（session.id）：带横线 UUID v4
//
// 这三个正则不是审美问题：客户端 isCanonicalConversationRequestId 用它校验服务端
// 回显的 conv-req，不合规即丢弃重造；readUuidV7TimestampMs 还会把前 12 hex 当毫秒解析。
var (
	reV7Hex32 = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
	reV4Hex32 = regexp.MustCompile(`^[0-9a-f]{12}4[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
	reUUIDv4  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// TestIDShapesMatchOfficialClient 钉三层 ID 的形态：与官方客户端逐位一致。
func TestIDShapesMatchOfficialClient(t *testing.T) {
	for i := 0; i < 100; i++ {
		if id := NewMessageID(); !reV4Hex32.MatchString(id) {
			t.Fatalf("NewMessageID() = %q，期望 32 hex v4", id)
		}
		if id := NewConversationID(); !reUUIDv4.MatchString(id) {
			t.Fatalf("NewConversationID() = %q，期望带横线 v4", id)
		}
		if id := NewConversationRequestID(); !reV7Hex32.MatchString(id) {
			t.Fatalf("NewConversationRequestID() = %q，期望 32 hex v7", id)
		}
		if id := StableConversationID("sess-x"); !reUUIDv4.MatchString(id) {
			t.Fatalf("StableConversationID() = %q，期望带横线 v4", id)
		}
		if id := RequestIDForKey("conv-a"); !reV7Hex32.MatchString(id) {
			t.Fatalf("RequestIDForKey() = %q，期望 32 hex v7", id)
		}
		if id := TurnRequestID("u0:hi"); !reV7Hex32.MatchString(id) {
			t.Fatalf("TurnRequestID() = %q，期望 32 hex v7", id)
		}
	}
}

// TestOfficialClientSamplesMatchV7 用客户端真实样本做正向用例（取自上游用量明细的
// requestId，v7 + 2026-10-30 时间戳）：形态正则必须认得它们。
func TestOfficialClientSamplesMatchV7(t *testing.T) {
	samples := []string{
		"01a18a78df53710680a45969c8917a80",
		"01a18a78b3cc7de0b751a035655bfa09",
	}
	for _, s := range samples {
		if !reV7Hex32.MatchString(s) {
			t.Errorf("官方样本 %q 未被 v7 正则接受", s)
		}
	}
}

// TestNewConversationRequestIDTimestamp 钉时间戳语义：前 12 hex 解出的毫秒必须落在
// 本次调用前后（客户端 readUuidV7TimestampMs 会解析它做 2 分钟复用判定）。
func TestNewConversationRequestIDTimestamp(t *testing.T) {
	before := time.Now().UnixMilli()
	id := NewConversationRequestID()
	after := time.Now().UnixMilli()
	ms, err := strconv.ParseInt(id[:12], 16, 64)
	if err != nil {
		t.Fatalf("解析时间前缀失败: %v", err)
	}
	if ms < before || ms > after {
		t.Errorf("时间前缀 %d 不在 [%d, %d] 内", ms, before, after)
	}
}

// TestDerivedIDTimestampIsProcessFixed 钉派生 ID 的时间前缀恒定：同键多次派生必须
// 逐位相同（否则上游按轮聚合会被拆碎），且前缀落在进程启动附近。
func TestDerivedIDTimestampIsProcessFixed(t *testing.T) {
	a, b := RequestIDForKey("conv-a"), RequestIDForKey("conv-a")
	if a != b {
		t.Fatalf("同键派生应恒同值: %q vs %q", a, b)
	}
	if a[:12] != TurnRequestID("x")[:12] {
		t.Errorf("派生 ID 应共享同一进程时间前缀: %q vs %q", a[:12], TurnRequestID("x")[:12])
	}
	ms, err := strconv.ParseInt(a[:12], 16, 64)
	if err != nil {
		t.Fatalf("解析时间前缀失败: %v", err)
	}
	if d := time.Now().UnixMilli() - ms; d < 0 || d > 60_000 {
		t.Errorf("进程时间前缀偏差过大: %d ms", d)
	}
}
