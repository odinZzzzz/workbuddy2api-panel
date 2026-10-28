package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// responses.go —— OpenAI Responses API 兼容层（POST /v1/responses）
//
// 为什么需要它：Codex CLI 0.165 起把 wire_api = "chat" 移除了，只剩 "responses"，
// 于是它只发 POST /v1/responses（Responses API）。网关原本只有 chat/completions，
// 客户端直接撞 404。
//
// 设计取舍：**不复制治理逻辑**。这里把 Responses 请求翻译成一份 chat/completions
// 请求体，然后直接调用既有的 h.chatCompletions，用一个 ResponseWriter 适配器把
// 它写出的 chat SSE 实时翻译回 Responses SSE。这样选号、轮转、熔断、粘性会话、
// 指纹脱敏、提示词改写、原生工具调用标记还原（dsml）、用量统计、请求日志——
// 全部原样生效，两条协议的行为不会漂移。新增协议只在边界上做格式转换。
//
// 上游调用链：Codex → /v1/responses → [本文件] → /v1/chat/completions 内部调用
// → CodeBuddy 上游 SSE → 回程同样由本文件逐帧翻译。

// maxResponsesBody 与 chat 侧保持同样的宽松策略：网关不做请求体预拦截，
// 超限问题交给上游返回真实错误（多图/长上下文会话体积极大）。
const maxResponsesBody = 64 << 20

func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxResponsesBody))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "invalid json: "+err.Error())
		return
	}
	chatBody, model, wantStream, convErr := responsesToChat(src)
	if convErr != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", convErr.Error())
		return
	}

	// 复用 chat 处理器：同一个 request（含 Authorization 已由 withAuth 校验），
	// 只把 body 换成翻译后的 chat 请求体。
	sub := r.Clone(r.Context())
	sub.Method = http.MethodPost
	sub.URL.Path = "/v1/chat/completions"
	sub.Body = io.NopCloser(bytes.NewReader(chatBody))
	sub.ContentLength = int64(len(chatBody))
	sub.Header = r.Header.Clone()
	sub.Header.Set("Content-Type", "application/json")
	sub.Header.Del("Content-Length")

	rw := newResponsesWriter(w, model, wantStream)
	defer rw.finish()
	h.chatCompletions(rw, sub)
}

/* ───────────────────────── 请求翻译：Responses → Chat ───────────────────────── */

// responsesToChat 把 Responses 请求体翻译成 chat/completions 请求体。
// 返回 chat JSON、模型名（用于回显）、客户端是否要求流式。
func responsesToChat(src map[string]any) ([]byte, string, bool, error) {
	model, _ := src["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, "", false, fmt.Errorf("missing required field: model")
	}
	wantStream := true
	if v, ok := src["stream"].(bool); ok {
		wantStream = v
	}

	msgs := make([]map[string]any, 0, 8)
	if ins, ok := src["instructions"].(string); ok && strings.TrimSpace(ins) != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": ins})
	}

	msgs = append(msgs, responsesInputToMessages(src["input"])...)
	if len(msgs) == 0 {
		return nil, "", false, fmt.Errorf("missing required field: input")
	}

	out := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   true, // 上游恒流式：非流式由 chat 侧聚合（与既有语义一致）
	}
	if v, ok := src["max_output_tokens"].(float64); ok && v > 0 {
		out["max_tokens"] = int(v)
	}
	if v, ok := src["temperature"].(float64); ok {
		out["temperature"] = v
	}
	if v, ok := src["top_p"].(float64); ok {
		out["top_p"] = v
	}
	// reasoning.effort → reasoning_effort：网关按模型档位自动降级
	if r, ok := src["reasoning"].(map[string]any); ok {
		if e, ok := r["effort"].(string); ok && e != "" {
			out["reasoning_effort"] = e
		}
	}
	// text.format（结构化输出）
	if t, ok := src["text"].(map[string]any); ok {
		if f, ok := t["format"].(map[string]any); ok {
			if f["type"] == "json_schema" {
				if schema, ok := f["schema"]; ok {
					out["response_format"] = map[string]any{
						"type": "json_schema",
						"json_schema": map[string]any{
							"name":   firstNonEmpty(asString(f["name"]), "output"),
							"schema": schema,
							"strict": f["strict"],
						},
					}
				}
			}
		}
	}

	if tools := responsesToolsToChat(src["tools"]); len(tools) > 0 {
		out["tools"] = tools
		if tc, ok := responsesToolChoiceToChat(src["tool_choice"]); ok {
			out["tool_choice"] = tc
		}
		if p, ok := src["parallel_tool_calls"].(bool); ok {
			out["parallel_tool_calls"] = p
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, "", false, err
	}
	return b, model, wantStream, nil
}

// responsesInputToMessages 把 Responses 的 input（字符串或 item 数组）摊平成 chat messages。
//
// item 类型：message / function_call / function_call_output / reasoning。
// chat 协议要求同一轮的多个 function_call 合并进**同一条** assistant 消息，
// 因此这里用 pendingAssistant 暂存、遇到非调用项时 flush。
func responsesInputToMessages(input any) []map[string]any {
	switch v := input.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []map[string]any{{"role": "user", "content": v}}
	case []any:
		return itemsToMessages(v)
	default:
		return nil
	}
}

func itemsToMessages(items []any) []map[string]any {
	msgs := make([]map[string]any, 0, len(items))
	var pending *map[string]any
	flush := func() {
		if pending != nil {
			msgs = append(msgs, *pending)
			pending = nil
		}
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := asString(item["type"])
		if typ == "" && item["role"] != nil {
			typ = "message"
		}
		switch typ {
		case "message":
			role := asString(item["role"])
			switch role {
			case "developer":
				role = "system"
			case "":
				role = "user"
			}
			content := responsesContentToChat(item["content"])
			flush()
			msgs = append(msgs, map[string]any{"role": role, "content": content})
		case "function_call":
			if pending == nil {
				m := map[string]any{"role": "assistant", "content": ""}
				pending = &m
			}
			call := map[string]any{
				"id":   firstNonEmpty(asString(item["call_id"]), asString(item["id"]), "call_"+randHex(6)),
				"type": "function",
				"function": map[string]any{
					"name":      asString(item["name"]),
					"arguments": firstNonEmpty(asString(item["arguments"]), "{}"),
				},
			}
			calls, _ := (*pending)["tool_calls"].([]any)
			(*pending)["tool_calls"] = append(calls, call)
		case "function_call_output":
			flush()
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": firstNonEmpty(asString(item["call_id"]), asString(item["id"])),
				"content":      outputToText(item["output"]),
			})
		case "reasoning":
			// 思维链不回传上游（与 chat 侧 reasoning_content 回填策略一致：不伪造）
			continue
		default:
			if role := asString(item["role"]); role != "" {
				flush()
				msgs = append(msgs, map[string]any{"role": role, "content": responsesContentToChat(item["content"])})
			}
		}
	}
	flush()
	return msgs
}

// responsesContentToChat 把 Responses 的 content parts 转成 chat content。
// 纯文本 → 单个字符串（上游对数组形态的纯文本会报 11101，见实测）；
// 含图片 → chat 的 parts 数组。
func responsesContentToChat(content any) any {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var texts []string
		var parts []map[string]any
		hasImage := false
		for _, raw := range v {
			p, ok := raw.(map[string]any)
			if !ok {
				if s, ok := raw.(string); ok {
					texts = append(texts, s)
				}
				continue
			}
			switch asString(p["type"]) {
			case "input_text", "output_text", "text":
				t := asString(p["text"])
				texts = append(texts, t)
				parts = append(parts, map[string]any{"type": "text", "text": t})
			case "input_image":
				url := asString(p["image_url"])
				if url == "" {
					if data := asString(p["data"]); data != "" {
						mime := firstNonEmpty(asString(p["mime_type"]), "image/png")
						url = "data:" + mime + ";base64," + data
					}
				}
				if url != "" {
					hasImage = true
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
				}
			}
		}
		if !hasImage {
			return strings.Join(texts, "\n")
		}
		return parts
	default:
		return ""
	}
}

func responsesToolsToChat(tools any) []map[string]any {
	list, ok := tools.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, raw := range list {
		t, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// Responses 的 function 工具是扁平结构；chat 需要嵌一层 function。
		// 其它内置工具（web_search / local_shell 等）网关不支持，忽略。
		if asString(t["type"]) != "function" {
			continue
		}
		src := t
		if fn, ok := t["function"].(map[string]any); ok {
			src = fn
		}
		name := asString(src["name"])
		if name == "" {
			continue
		}
		fn := map[string]any{
			"name":       name,
			"parameters": defaultObject(src["parameters"]),
		}
		if d := asString(src["description"]); d != "" {
			fn["description"] = d
		}
		if s, ok := src["strict"]; ok && s != nil {
			fn["strict"] = s
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func responsesToolChoiceToChat(tc any) (any, bool) {
	switch v := tc.(type) {
	case string:
		if v == "auto" || v == "none" || v == "required" {
			return v, true
		}
		return nil, false
	case map[string]any:
		if asString(v["type"]) == "function" {
			name := firstNonEmpty(asString(v["name"]), "")
			if fn, ok := v["function"].(map[string]any); ok && name == "" {
				name = asString(fn["name"])
			}
			if name != "" {
				return map[string]any{"type": "function", "function": map[string]any{"name": name}}, true
			}
		}
	}
	return nil, false
}

/* ───────────────────────── 响应翻译：Chat SSE → Responses SSE ───────────────────────── */

type responsesFCItem struct {
	itemID string
	callID string
	name   string
	args   strings.Builder
	outIdx int
}

// responsesWriter 是挂在 chatCompletions 与真实 ResponseWriter 之间的适配器：
// 把 chat SSE 帧实时翻译成 Responses SSE 事件；非流式则聚合后一次性返回 JSON。
// 上游报错（非 200）时退化为原样透传，保证错误语义与 chat 端点一致。
type responsesWriter struct {
	dst        http.ResponseWriter
	flusher    http.Flusher
	model      string
	wantStream bool

	header http.Header
	status int

	parseBuf    strings.Builder
	passthrough bool

	started   bool
	respID    string
	createdAt int64
	seq       int

	textItemID  string
	textOutIdx  int
	textStarted bool
	accText     strings.Builder

	tools  map[int]*responsesFCItem
	outIdx int
	usage  map[string]any
	// finishReason 记录上游 finish_reason（stop / length / tool_calls）。
	// 不能叫 finish：与本类型的方法 finish() 同名会冲突。
	finishReason string
	failed       bool
	errText      string
}

func newResponsesWriter(dst http.ResponseWriter, model string, wantStream bool) *responsesWriter {
	w := &responsesWriter{
		dst:        dst,
		model:      model,
		wantStream: wantStream,
		header:     http.Header{},
		respID:     "resp_" + randHex(10),
		createdAt:  time.Now().Unix(),
		tools:      map[int]*responsesFCItem{},
		textOutIdx: -1,
	}
	if f, ok := dst.(http.Flusher); ok {
		w.flusher = f
	}
	return w
}

func (w *responsesWriter) Header() http.Header { return w.header }

func (w *responsesWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	if code != http.StatusOK {
		// 上游/网关错误：原样透传状态码与 body（chat 侧已组装好 OpenAI 错误 JSON）
		w.passthrough = true
		w.dst.WriteHeader(code)
	}
}

func (w *responsesWriter) Write(p []byte) (int, error) {
	if w.passthrough {
		return w.dst.Write(p)
	}
	w.parseBuf.Write(p)
	for {
		s := w.parseBuf.String()
		idx := strings.Index(s, "\n\n")
		if idx < 0 {
			break
		}
		block := s[:idx]
		w.parseBuf.Reset()
		w.parseBuf.WriteString(s[idx+2:])
		w.handleBlock(block)
	}
	return len(p), nil
}

func (w *responsesWriter) Flush() {
	if w.flusher != nil && (w.started || w.passthrough) {
		w.flusher.Flush()
	}
}

func (w *responsesWriter) handleBlock(block string) {
	block = strings.TrimSpace(block)
	if block == "" {
		return
	}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		w.consumeChunk(chunk)
	}
}

func (w *responsesWriter) consumeChunk(chunk map[string]any) {
	// 上游 error 帧（chat 侧会透传）→ 记为失败，不再产出成功事件
	if e, ok := chunk["error"]; ok && e != nil {
		w.failed = true
		w.errText = asString(mapOf(e)["message"])
		if w.errText == "" {
			b, _ := json.Marshal(e)
			w.errText = string(b)
		}
		return
	}
	if u, ok := chunk["usage"].(map[string]any); ok {
		w.usage = u
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	if fr := asString(choice["finish_reason"]); fr != "" {
		w.finishReason = fr
	}
	delta, _ := choice["delta"].(map[string]any)
	if delta == nil {
		return
	}
	if c := asString(delta["content"]); c != "" {
		w.ensureStarted()
		w.startText()
		w.accText.WriteString(c)
		w.emit("response.output_text.delta", map[string]any{
			"item_id": w.textItemID, "output_index": w.textOutIdx, "content_index": 0, "delta": c,
		})
	}
	if calls, ok := delta["tool_calls"].([]any); ok {
		w.ensureStarted()
		for _, raw := range calls {
			tc, _ := raw.(map[string]any)
			if tc == nil {
				continue
			}
			idx := int(asFloat(tc["index"]))
			rec := w.tools[idx]
			if rec == nil {
				rec = w.startTool(idx, tc)
			}
			fn, _ := tc["function"].(map[string]any)
			frag := asString(fn["arguments"])
			if frag != "" {
				rec.args.WriteString(frag)
				w.emit("response.function_call_arguments.delta", map[string]any{
					"item_id": rec.itemID, "output_index": rec.outIdx, "delta": frag,
				})
			}
		}
	}
}

func (w *responsesWriter) ensureStarted() {
	if w.started {
		return
	}
	w.started = true
	// 非流式：不写 SSE 头、不发 created/in_progress——body 由 finish() 以 JSON 写出。
	// 这里只标记「已开始」，避免 writeJSONResponse 重复 WriteHeader。
	if !w.wantStream {
		return
	}
	h := w.dst.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.dst.WriteHeader(http.StatusOK)
	w.emit("response.created", map[string]any{"response": w.responseObject("in_progress", nil)})
	w.emit("response.in_progress", map[string]any{"response": w.responseObject("in_progress", nil)})
}

func (w *responsesWriter) startText() {
	if w.textStarted {
		return
	}
	w.textStarted = true
	w.textItemID = "msg_" + randHex(10)
	w.textOutIdx = w.nextOutIdx()
	w.emit("response.output_item.added", map[string]any{
		"output_index": w.textOutIdx,
		"item": map[string]any{
			"id": w.textItemID, "type": "message", "status": "in_progress",
			"role": "assistant", "content": []any{},
		},
	})
	w.emit("response.content_part.added", map[string]any{
		"item_id": w.textItemID, "output_index": w.textOutIdx, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

func (w *responsesWriter) startTool(idx int, tc map[string]any) *responsesFCItem {
	fn, _ := tc["function"].(map[string]any)
	rec := &responsesFCItem{
		itemID: "fc_" + randHex(10),
		callID: firstNonEmpty(asString(tc["id"]), "call_"+randHex(8)),
		name:   asString(fn["name"]),
		outIdx: w.nextOutIdx(),
	}
	w.tools[idx] = rec
	w.emit("response.output_item.added", map[string]any{
		"output_index": rec.outIdx,
		"item": map[string]any{
			"id": rec.itemID, "type": "function_call", "status": "in_progress",
			"call_id": rec.callID, "name": rec.name, "arguments": "",
		},
	})
	return rec
}

func (w *responsesWriter) nextOutIdx() int {
	i := w.outIdx
	w.outIdx++
	return i
}

func (w *responsesWriter) emit(event string, fields map[string]any) {
	// 非流式请求（wantStream=false）只累积状态，不发任何事件：
	// 结果在 finish() 里一次性以 JSON 返回。上游照样是流式，这里只是不转发。
	if w.passthrough || !w.wantStream {
		return
	}
	w.seq++
	obj := map[string]any{"type": event, "sequence_number": w.seq}
	for k, v := range fields {
		obj[k] = v
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return
	}
	if _, err := io.WriteString(w.dst, "event: "+event+"\ndata: "+string(b)+"\n\n"); err != nil {
		return
	}
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

// finish 收尾：非流式一次性返回 JSON；流式补齐输出项并发出 response.completed。
func (w *responsesWriter) finish() {
	if w.passthrough {
		return
	}
	if !w.wantStream {
		w.writeJSONResponse()
		return
	}
	w.ensureStarted()
	if w.failed {
		w.emit("response.failed", map[string]any{
			"response": map[string]any{
				"id": w.respID, "object": "response", "created_at": w.createdAt,
				"model": w.model, "status": "failed",
				"error": map[string]any{"code": "upstream_error", "message": w.errText},
			},
		})
		return
	}
	items := make([]map[string]any, 0, 1+len(w.tools))
	if w.textStarted {
		text := w.accText.String()
		w.emit("response.output_text.done", map[string]any{
			"item_id": w.textItemID, "output_index": w.textOutIdx, "content_index": 0, "text": text,
		})
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
		w.emit("response.content_part.done", map[string]any{
			"item_id": w.textItemID, "output_index": w.textOutIdx, "content_index": 0, "part": part,
		})
		item := map[string]any{
			"id": w.textItemID, "type": "message", "status": "completed",
			"role": "assistant", "content": []any{part},
		}
		w.emit("response.output_item.done", map[string]any{"output_index": w.textOutIdx, "item": item})
		items = append(items, item)
	}
	idxs := make([]int, 0, len(w.tools))
	for i := range w.tools {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		rec := w.tools[i]
		args := rec.args.String()
		if args == "" {
			args = "{}"
		}
		w.emit("response.function_call_arguments.done", map[string]any{
			"item_id": rec.itemID, "output_index": rec.outIdx, "arguments": args,
		})
		item := map[string]any{
			"id": rec.itemID, "type": "function_call", "status": "completed",
			"call_id": rec.callID, "name": rec.name, "arguments": args,
		}
		w.emit("response.output_item.done", map[string]any{"output_index": rec.outIdx, "item": item})
		items = append(items, item)
	}
	w.emit("response.completed", map[string]any{"response": w.responseObject("completed", items)})
}

func (w *responsesWriter) writeJSONResponse() {
	if !w.started {
		// 非流式：body 从未写过，直接返回 JSON
		h := w.dst.Header()
		h.Set("Content-Type", "application/json")
		w.dst.WriteHeader(http.StatusOK)
	}
	items := make([]map[string]any, 0, 1+len(w.tools))
	if text := w.accText.String(); text != "" {
		items = append(items, map[string]any{
			"id": "msg_" + randHex(10), "type": "message", "status": "completed",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		})
	}
	idxs := make([]int, 0, len(w.tools))
	for i := range w.tools {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		rec := w.tools[i]
		args := rec.args.String()
		if args == "" {
			args = "{}"
		}
		items = append(items, map[string]any{
			"id": rec.itemID, "type": "function_call", "status": "completed",
			"call_id": rec.callID, "name": rec.name, "arguments": args,
		})
	}
	b, _ := json.Marshal(w.responseObject("completed", items))
	_, _ = w.dst.Write(b)
}

func (w *responsesWriter) responseObject(status string, items []map[string]any) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	obj := map[string]any{
		"id": w.respID, "object": "response", "created_at": w.createdAt,
		"model": w.model, "status": status, "output": items,
	}
	if u := w.usage; u != nil {
		obj["usage"] = map[string]any{
			"input_tokens":  asFloat(u["prompt_tokens"]),
			"output_tokens": asFloat(u["completion_tokens"]),
			"total_tokens":  asFloat(u["total_tokens"]),
			"input_tokens_details": map[string]any{
				"cached_tokens": cachedTokens(u),
			},
			"output_tokens_details": map[string]any{
				"reasoning_tokens": reasoningTokens(u),
			},
		}
	}
	return obj
}

/* ───────────────────────── 小工具 ───────────────────────── */

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// firstNonEmpty 返回第一个非空字符串（缺省值链）。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func defaultObject(v any) any {
	if v == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return v
}

func outputToText(v any) string {
	switch out := v.(type) {
	case string:
		return out
	case []any:
		var parts []string
		for _, raw := range out {
			if s, ok := raw.(string); ok {
				parts = append(parts, s)
				continue
			}
			if m, ok := raw.(map[string]any); ok {
				if t := asString(m["text"]); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		b, err := json.Marshal(out)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func cachedTokens(u map[string]any) float64 {
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		return asFloat(d["cached_tokens"])
	}
	if v, ok := u["cached_tokens"]; ok {
		return asFloat(v)
	}
	return 0
}

func reasoningTokens(u map[string]any) float64 {
	if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		return asFloat(d["reasoning_tokens"])
	}
	return 0
}
