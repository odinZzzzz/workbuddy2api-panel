package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

// anthropic_test.go —— Anthropic Messages 兼容层的单元测试。
//
// 覆盖：请求翻译（system/messages/tools/tool_choice/thinking/stop_sequences）、
// 工具调用双向回填（tool_use ↔ tool_calls、tool_result ↔ tool 消息）、
// 流式事件序列（文本 / tool_use 两种 block）、非流式 JSON、x-api-key 鉴权。

func TestAnthropicToChatBasic(t *testing.T) {
	src := map[string]any{
		"model":      "claude-sonnet-5",
		"max_tokens": float64(512),
		"system":     "你是助手",
		"messages": []any{
			map[string]any{"role": "user", "content": "你好"},
		},
		"thinking":       map[string]any{"type": "enabled", "budget_tokens": float64(10000)},
		"stop_sequences": []any{"END"},
		"tools": []any{map[string]any{
			"name":        "shell",
			"description": "run cmd",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}},
			},
		}},
		"tool_choice": map[string]any{"type": "any"},
	}
	body, model, stream, err := anthropicToChat(src, nil)
	if err != nil {
		t.Fatalf("anthropicToChat: %v", err)
	}
	if model != "claude-sonnet-5" || stream {
		t.Fatalf("model=%q stream=%v (stream 默认应为 false)", model, stream)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["stream"] != true {
		t.Errorf("上游必须强制 stream=true，得到 %v", got["stream"])
	}
	if got["max_tokens"] != float64(512) {
		t.Errorf("max_tokens=%v", got["max_tokens"])
	}
	if got["reasoning_effort"] != "max" {
		t.Errorf("thinking.budget_tokens=10000 应映射成 effort=max，得到 %v", got["reasoning_effort"])
	}
	stops, _ := got["stop"].([]any)
	if len(stops) != 1 || stops[0] != "END" {
		t.Errorf("stop=%v", got["stop"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages=%d, want 2", len(msgs))
	}
	if m0, _ := msgs[0].(map[string]any); m0["role"] != "system" || m0["content"] != "你是助手" {
		t.Errorf("system 消息=%v", m0)
	}
	tools, _ := got["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "shell" {
		t.Errorf("tool name=%v", fn["name"])
	}
	// tool_choice any → required
	if got["tool_choice"] != "required" {
		t.Errorf("tool_choice=%v, want required", got["tool_choice"])
	}
}

func TestAnthropicToolRoundTrip(t *testing.T) {
	src := map[string]any{
		"model":      "claude-sonnet-5",
		"max_tokens": float64(100),
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "列目录"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "我来执行"},
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "shell", "input": map[string]any{"cmd": "ls"}},
				map[string]any{"type": "tool_use", "id": "toolu_2", "name": "shell", "input": map[string]any{"cmd": "pwd"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "a.txt"},
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": []any{
					map[string]any{"type": "text", "text": "/tmp"},
				}},
			}},
		},
	}
	body, _, _, err := anthropicToChat(src, nil)
	if err != nil {
		t.Fatalf("anthropicToChat: %v", err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// user(文本) → assistant(文本 + 2 个 tool_calls) → tool → tool
	if len(got.Messages) != 4 {
		t.Fatalf("messages=%d, want 4: %#v", len(got.Messages), got.Messages)
	}
	asst := got.Messages[1]
	if asst["role"] != "assistant" || asst["content"] != "我来执行" {
		t.Errorf("assistant=%v", asst)
	}
	calls, _ := asst["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("tool_calls=%d, want 2（相邻 tool_use 应合并进同一条 assistant）", len(calls))
	}
	c0, _ := calls[0].(map[string]any)
	fn0, _ := c0["function"].(map[string]any)
	if c0["id"] != "toolu_1" || fn0["name"] != "shell" || fn0["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("tool_call[0]=%v", c0)
	}
	if got.Messages[2]["role"] != "tool" || got.Messages[2]["tool_call_id"] != "toolu_1" || got.Messages[2]["content"] != "a.txt" {
		t.Errorf("tool msg=%v", got.Messages[2])
	}
	if got.Messages[3]["content"] != "/tmp" {
		t.Errorf("tool_result 的 text block 应拍平成文本，得到 %v", got.Messages[3]["content"])
	}
}

func anthropicEvents(t *testing.T, raw string) ([]string, []map[string]any) {
	t.Helper()
	var names []string
	var payloads []map[string]any
	for _, block := range strings.Split(raw, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var name, data string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name == "" {
			continue
		}
		names = append(names, name)
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil {
			payloads = append(payloads, m)
		}
	}
	return names, payloads
}

func TestAnthropicWriterTextStream(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicWriter(rec, "claude-sonnet-5", true)
	for _, f := range []string{
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
	} {
		if _, err := w.Write([]byte("data: " + f + "\n\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	w.finish()

	names, payloads := anthropicEvents(t, rec.Body.String())
	want := []string{"message_start", "ping", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("event 序列\n got: %v\nwant: %v", names, want)
	}
	var text string
	for i, n := range names {
		if n == "content_block_delta" {
			d, _ := payloads[i]["delta"].(map[string]any)
			if d["type"] == "text_delta" {
				text += asString(d["text"])
			}
		}
	}
	if text != "你好" {
		t.Errorf("text=%q, want 你好", text)
	}
	// message_delta 的 stop_reason 与 output_tokens
	md := payloads[len(payloads)-2]
	delta, _ := md["delta"].(map[string]any)
	if delta["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason=%v, want end_turn", delta["stop_reason"])
	}
	usage, _ := md["usage"].(map[string]any)
	if usage["output_tokens"] != float64(2) {
		t.Errorf("output_tokens=%v, want 2", usage["output_tokens"])
	}
}

func TestAnthropicWriterToolUse(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicWriter(rec, "claude-sonnet-5", true)
	for _, f := range []string{
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_9","function":{"name":"shell","arguments":"{\"cmd\""}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"ls\"}"}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	} {
		if _, err := w.Write([]byte("data: " + f + "\n\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	w.finish()

	names, payloads := anthropicEvents(t, rec.Body.String())
	var startOK, deltaOK, stopOK bool
	var partial string
	for i, n := range names {
		switch n {
		case "content_block_start":
			cb, _ := payloads[i]["content_block"].(map[string]any)
			if cb["type"] == "tool_use" && cb["id"] == "toolu_9" && cb["name"] == "shell" {
				startOK = true
			}
		case "content_block_delta":
			d, _ := payloads[i]["delta"].(map[string]any)
			if d["type"] == "input_json_delta" {
				deltaOK = true
				partial += asString(d["partial_json"])
			}
		case "message_delta":
			d, _ := payloads[i]["delta"].(map[string]any)
			if d["stop_reason"] == "tool_use" {
				stopOK = true
			}
		}
	}
	if !startOK || !deltaOK || !stopOK {
		t.Fatalf("tool_use 事件缺失 start=%v delta=%v stop=%v (%v)", startOK, deltaOK, stopOK, names)
	}
	if partial != `{"cmd":"ls"}` {
		t.Errorf("partial_json 拼接=%q", partial)
	}
}

func TestAnthropicWriterNonStream(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newAnthropicWriter(rec, "claude-sonnet-5", false)
	if _, err := w.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"好的\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.finish()

	body := rec.Body.String()
	if strings.Contains(body, "event:") {
		t.Fatalf("非流式不能吐 SSE：%s", body)
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("非 JSON：%v (%s)", err, body)
	}
	if msg["type"] != "message" || msg["role"] != "assistant" || msg["stop_reason"] != "end_turn" {
		t.Errorf("message=%v", msg)
	}
	content, _ := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content blocks=%d, want 1", len(content))
	}
	c0, _ := content[0].(map[string]any)
	if c0["type"] != "text" || c0["text"] != "好的" {
		t.Errorf("block=%v", c0)
	}
}

// x-api-key 与 Bearer 必须等价放行；错误 key 必须拒绝。
func TestVerifyAPIKeyBothHeaders(t *testing.T) {
	const key = "sk-test"
	cases := []struct {
		name string
		set  func(*http.Request)
		want bool
	}{
		{"bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }, true},
		{"x-api-key", func(r *http.Request) { r.Header.Set("x-api-key", key) }, true},
		{"both", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+key)
			r.Header.Set("x-api-key", key)
		}, true},
		{"wrong x-api-key", func(r *http.Request) { r.Header.Set("x-api-key", "nope") }, false},
		{"none", func(r *http.Request) {}, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.set(r)
		if got := httpauth.VerifyAPIKey(r, key); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// 未配置 api_key 时恒放行
	if !httpauth.VerifyAPIKey(httptest.NewRequest(http.MethodPost, "/v1/messages", nil), "") {
		t.Error("空 api_key 应放行")
	}
}

// 模型别名：精确命中优先；claude-* 回落到 default_model；未配置则原样透传。
func TestResolveAnthropicModel(t *testing.T) {
	h := &Handler{cfg: Config{
		AnthropicDefaultModel: "cn:deepseek-v4.1-flash",
		AnthropicAliases:      map[string]string{"claude-opus-5": "cn:deepseek-v4-pro"},
	}}
	cases := map[string]string{
		"claude-opus-5":             "cn:deepseek-v4-pro",     // 精确别名优先
		"claude-sonnet-5":           "cn:deepseek-v4.1-flash", // claude-* 回落
		"claude-haiku-4-5-20251001": "cn:deepseek-v4.1-flash",
		"cn:glm-5.3":                "cn:glm-5.3", // 真实 id 不动
		"gpt-5.5":                   "gpt-5.5",    // 非 claude 前缀不动
	}
	for in, want := range cases {
		if got := h.resolveAnthropicModel(in); got != want {
			t.Errorf("resolve(%q)=%q, want %q", in, got, want)
		}
	}
	// 未配置映射时保持透明
	empty := &Handler{cfg: Config{}}
	if got := empty.resolveAnthropicModel("claude-sonnet-5"); got != "claude-sonnet-5" {
		t.Errorf("未配置时应原样透传，得到 %q", got)
	}
}
