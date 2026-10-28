package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// responses_test.go —— Responses API 兼容层的单元测试。
//
// 覆盖三件事：
//  1. 请求翻译（Responses → chat）：instructions/input/tools/reasoning/max_output_tokens
//  2. 多轮工具调用回填：function_call 合并进同一条 assistant 消息、function_call_output → tool 消息
//  3. 流式翻译（chat SSE → Responses SSE）：文本增量与工具调用两种输出项

func TestResponsesToChatBasic(t *testing.T) {
	src := map[string]any{
		"model":             "cn:deepseek-v4-flash",
		"instructions":      "你是助手",
		"input":             "你好",
		"max_output_tokens": float64(256),
		"reasoning":         map[string]any{"effort": "high"},
		"tools": []any{map[string]any{
			"type":        "function",
			"name":        "shell",
			"description": "run a command",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		"tool_choice": "auto",
	}
	body, model, stream, err := responsesToChat(src)
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	if model != "cn:deepseek-v4-flash" || !stream {
		t.Fatalf("model=%q stream=%v", model, stream)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal chat body: %v", err)
	}
	if got["stream"] != true {
		t.Errorf("chat body must force stream=true, got %v", got["stream"])
	}
	if got["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want 256", got["max_tokens"])
	}
	if got["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", got["reasoning_effort"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2 (system + user)", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "你是助手" {
		t.Errorf("system message = %v", first)
	}
	second, _ := msgs[1].(map[string]any)
	// 纯文本必须落成字符串：上游对 ["文本"] 这种数组形态报 11101
	if second["content"] != "你好" {
		t.Errorf("user content = %#v, want plain string", second["content"])
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "shell" {
		t.Errorf("tool name = %v", fn["name"])
	}
}

func TestResponsesToChatToolRoundTrip(t *testing.T) {
	src := map[string]any{
		"model": "cn:deepseek-v4-flash",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "列目录"}}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": `{"cmd":"ls"}`},
			map[string]any{"type": "function_call", "call_id": "call_2", "name": "shell", "arguments": `{"cmd":"pwd"}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "a.txt"},
			map[string]any{"type": "function_call_output", "call_id": "call_2", "output": "/tmp"},
		},
	}
	body, _, _, err := responsesToChat(src)
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// user → assistant(2 个 tool_calls 合并在一条) → tool → tool
	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %#v", len(got.Messages), got.Messages)
	}
	if got.Messages[0]["role"] != "user" || got.Messages[0]["content"] != "列目录" {
		t.Errorf("user message = %v", got.Messages[0])
	}
	asst := got.Messages[1]
	if asst["role"] != "assistant" {
		t.Fatalf("second message role = %v, want assistant", asst["role"])
	}
	calls, _ := asst["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("assistant tool_calls = %d, want 2 merged into one message", len(calls))
	}
	for i, want := range []string{"call_1", "call_2"} {
		c, _ := calls[i].(map[string]any)
		if c["id"] != want {
			t.Errorf("tool_calls[%d].id = %v, want %s", i, c["id"], want)
		}
	}
	if got.Messages[2]["role"] != "tool" || got.Messages[2]["tool_call_id"] != "call_1" || got.Messages[2]["content"] != "a.txt" {
		t.Errorf("tool message = %v", got.Messages[2])
	}
}

// feedSSE 把 chat 帧喂给 responsesWriter，返回它写到 dst 的原始 SSE 文本。
func feedSSE(t *testing.T, w *responsesWriter, frames ...string) string {
	t.Helper()
	for _, f := range frames {
		if _, err := w.Write([]byte("data: " + f + "\n\n")); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}
	return ""
}

func events(t *testing.T, raw string) ([]string, []map[string]any) {
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
		if err := json.Unmarshal([]byte(data), &m); err == nil {
			payloads = append(payloads, m)
		}
	}
	return names, payloads
}

func TestResponsesWriterTextStream(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newResponsesWriter(rec, "cn:deepseek-v4-flash", true)
	feedSSE(t, w,
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
	)
	w.finish()

	names, payloads := events(t, rec.Body.String())
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence\n got: %v\nwant: %v", names, want)
	}
	// 文本增量拼起来应为「你好」
	var text string
	for i, n := range names {
		if n == "response.output_text.delta" {
			text += asString(payloads[i]["delta"])
		}
	}
	if text != "你好" {
		t.Errorf("delta text = %q, want 你好", text)
	}
	// completed 里应带完整文本与 usage
	last := payloads[len(payloads)-1]
	resp, _ := last["response"].(map[string]any)
	out, _ := resp["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("completed output = %d, want 1", len(out))
	}
	msg, _ := out[0].(map[string]any)
	content, _ := msg["content"].([]any)
	part, _ := content[0].(map[string]any)
	if part["text"] != "你好" {
		t.Errorf("completed text = %v, want 你好", part["text"])
	}
	usage, _ := resp["usage"].(map[string]any)
	if usage["total_tokens"] != float64(9) {
		t.Errorf("usage.total_tokens = %v, want 9", usage["total_tokens"])
	}
}

func TestResponsesWriterToolCalls(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newResponsesWriter(rec, "cn:deepseek-v4-flash", true)
	feedSSE(t, w,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"shell","arguments":"{\"cmd\""}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"ls\"}"}}]}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	w.finish()

	names, payloads := events(t, rec.Body.String())
	var sawAdded, sawArgsDelta, sawCompleted bool
	for i, n := range names {
		switch n {
		case "response.output_item.added":
			item, _ := payloads[i]["item"].(map[string]any)
			if item["type"] == "function_call" && item["name"] == "shell" && item["call_id"] == "call_9" {
				sawAdded = true
			}
		case "response.function_call_arguments.delta":
			sawArgsDelta = true
		case "response.completed":
			sawCompleted = true
			resp, _ := payloads[i]["response"].(map[string]any)
			out, _ := resp["output"].([]any)
			if len(out) != 1 {
				t.Fatalf("completed output = %d, want 1 function_call", len(out))
			}
			fc, _ := out[0].(map[string]any)
			if fc["type"] != "function_call" || fc["arguments"] != `{"cmd":"ls"}` {
				t.Errorf("function_call item = %v", fc)
			}
		}
	}
	if !sawAdded || !sawArgsDelta || !sawCompleted {
		t.Fatalf("missing events: added=%v argsDelta=%v completed=%v (%v)", sawAdded, sawArgsDelta, sawCompleted, names)
	}
}

// 上游非 200（例如全部账号冷却 503）必须原样透传状态码与 body，不能被翻译成 SSE。
func TestResponsesWriterPassthroughError(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newResponsesWriter(rec, "cn:deepseek-v4-flash", true)
	w.WriteHeader(503)
	if _, err := w.Write([]byte(`{"error":{"message":"all accounts busy"}}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.finish()
	if rec.Code != 503 {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "all accounts busy") {
		t.Errorf("body not passed through: %s", rec.Body.String())
	}
}

// 非流式（stream=false）必须返回单个 JSON response 对象，**不能**吐 SSE 事件。
// 上游照样是流式（chat 侧恒流式），所以这条守的是「不转发事件、只在末尾出 JSON」。
func TestResponsesWriterNonStreamJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newResponsesWriter(rec, "cn:deepseek-v4-flash", false)
	feedSSE(t, w,
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"好"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"的"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
	)
	w.finish()

	body := rec.Body.String()
	if strings.Contains(body, "event:") {
		t.Fatalf("non-stream response must not contain SSE events: %s", body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, body)
	}
	if got["object"] != "response" || got["status"] != "completed" {
		t.Errorf("object=%v status=%v", got["object"], got["status"])
	}
	out, _ := got["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("output = %d, want 1", len(out))
	}
	msg, _ := out[0].(map[string]any)
	content, _ := msg["content"].([]any)
	part, _ := content[0].(map[string]any)
	if part["text"] != "好的" {
		t.Errorf("text = %v, want 好的", part["text"])
	}
}
