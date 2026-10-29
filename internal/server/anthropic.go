package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// anthropic.go —— Anthropic Messages API 兼容层（POST /v1/messages、POST /v1/messages/count_tokens）
//
// 为什么需要它：Claude Code / cc-switch 的 claude 通道走 Anthropic 协议
// （ANTHROPIC_BASE_URL + /v1/messages），而本网关原本只有 OpenAI 形状的端点，
// 客户端会 404。
//
// 设计取向与 responses.go 完全一致：**边界翻译，不复制治理逻辑**。请求体翻译成
// chat/completions 请求后直接调用既有 chatCompletions，再用 ResponseWriter 适配器
// 把 chat SSE 翻译回 Anthropic SSE。选号、轮转、熔断、冷却、粘性、指纹脱敏、
// 提示词改写、DSML 工具标记还原、用量统计与请求日志全部原样生效。
//
// 与 Responses 层不同的是鉴权头：Anthropic 客户端用 x-api-key，故这两条路由挂
// withAuthAPIKey（见 handler.go），两种头都认。

const maxAnthropicBody = 64 << 20

// Anthropic 的 stop_reason 词汇与 chat 的 finish_reason 不同，需要映射。
func anthropicStopReason(finish string, hasTool bool) string {
	if hasTool {
		return "tool_use"
	}
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

/* ───────────────────────── 请求翻译：Anthropic → Chat ───────────────────────── */

func (h *Handler) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAnthropicBody))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "read body: "+err.Error())
		return
	}
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid json: "+err.Error())
		return
	}
	chatBody, model, wantStream, convErr := anthropicToChat(src, h.resolveAnthropicModel)
	if convErr != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", convErr.Error())
		return
	}

	sub := r.Clone(r.Context())
	sub.Method = http.MethodPost
	sub.URL.Path = "/v1/chat/completions"
	sub.Body = io.NopCloser(bytes.NewReader(chatBody))
	sub.ContentLength = int64(len(chatBody))
	sub.Header = r.Header.Clone()
	sub.Header.Set("Content-Type", "application/json")
	sub.Header.Del("Content-Length")

	aw := newAnthropicWriter(w, model, wantStream)
	defer aw.finish()
	h.chatCompletions(aw, sub)
}

func anthropicToChat(src map[string]any, resolve func(string) string) ([]byte, string, bool, error) {
	// requested 是客户端发来的模型名（响应里原样回显，客户端据此判断"我请求的模型"）；
	// upstream 才是真正打给上游的 id——claude-* 这类 Anthropic 原生命名会被别名表改写。
	requested := asString(src["model"])
	if strings.TrimSpace(requested) == "" {
		return nil, "", false, fmt.Errorf("missing required field: model")
	}
	upstream := requested
	if resolve != nil {
		if mapped := resolve(requested); strings.TrimSpace(mapped) != "" {
			upstream = mapped
		}
	}
	wantStream := false
	if v, ok := src["stream"].(bool); ok {
		wantStream = v
	}

	msgs := make([]map[string]any, 0, 8)
	// system 可以是字符串，也可以是 text block 数组
	switch s := src["system"].(type) {
	case string:
		if strings.TrimSpace(s) != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": s})
		}
	case []any:
		if txt := anthropicBlocksToText(s); txt != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": txt})
		}
	}
	msgs = append(msgs, anthropicMessagesToChat(src["messages"])...)
	if len(msgs) == 0 {
		return nil, "", false, fmt.Errorf("missing required field: messages")
	}

	out := map[string]any{
		"model":    upstream,
		"messages": msgs,
		"stream":   true, // 上游恒流式，非流式由本层聚合
	}
	if v, ok := src["max_tokens"].(float64); ok && v > 0 {
		out["max_tokens"] = int(v)
	}
	if v, ok := src["temperature"].(float64); ok {
		out["temperature"] = v
	}
	if v, ok := src["top_p"].(float64); ok {
		out["top_p"] = v
	}
	if seqs, ok := src["stop_sequences"].([]any); ok && len(seqs) > 0 {
		stops := make([]string, 0, len(seqs))
		for _, s := range seqs {
			if str := asString(s); str != "" {
				stops = append(stops, str)
			}
		}
		if len(stops) > 0 {
			out["stop"] = stops
		}
	}
	// thinking.enabled → reasoning_effort：网关再按模型档位自动降级。
	// Anthropic 的 budget_tokens 没有 chat 侧等价物，只用来决定档位高低。
	if th, ok := src["thinking"].(map[string]any); ok && asString(th["type"]) == "enabled" {
		budget := asFloat(th["budget_tokens"])
		effort := "high"
		if budget >= 8000 {
			effort = "max"
		}
		out["reasoning_effort"] = effort
	}
	if tools := anthropicToolsToChat(src["tools"]); len(tools) > 0 {
		out["tools"] = tools
		if tc, ok := anthropicToolChoiceToChat(src["tool_choice"]); ok {
			out["tool_choice"] = tc
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, "", false, err
	}
	return b, requested, wantStream, nil
}

// anthropicMessagesToChat 把 Anthropic messages 摊平成 chat messages。
//
// 关键差异（与 Responses 层不同）：
//   - Anthropic 的一条 assistant 消息里，text 与 tool_use 是**并列的 content block**，
//     而 chat 要求它们同属**一条** assistant 消息（content + tool_calls）。
//     因此 assistant 消息按「文本合并 + 调用合并」组装成单条，绝不拆成两条。
//   - Anthropic 的一条 user 消息里可以既有 tool_result 又有新文本；chat 侧
//     tool_result 必须是独立的 tool 消息，所以按 block 出现次序展开：
//     遇到 tool_result 先 flush 已累积的文本消息，再追加 tool 消息。
func anthropicMessagesToChat(input any) []map[string]any {
	list, ok := input.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))

	for _, raw := range list {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := asString(msg["role"])

		// content 直接是字符串的简单形态
		if s, isStr := msg["content"].(string); isStr {
			if s != "" {
				out = append(out, map[string]any{"role": role, "content": s})
			}
			continue
		}
		blocks, _ := msg["content"].([]any)
		if blocks == nil {
			continue
		}

		if role == "assistant" {
			// 文本与 tool_use 合并进同一条 assistant 消息
			var texts []string
			var calls []any
			for _, braw := range blocks {
				block, ok := braw.(map[string]any)
				if !ok {
					continue
				}
				switch asString(block["type"]) {
				case "text":
					if t := asString(block["text"]); t != "" {
						texts = append(texts, t)
					}
				case "tool_use":
					args, _ := json.Marshal(block["input"])
					argStr := string(args)
					if argStr == "" || argStr == "null" {
						argStr = "{}"
					}
					calls = append(calls, map[string]any{
						"id":   firstNonEmpty(asString(block["id"]), "call_"+randHex(6)),
						"type": "function",
						"function": map[string]any{
							"name":      asString(block["name"]),
							"arguments": argStr,
						},
					})
				}
			}
			if len(texts) > 0 || len(calls) > 0 {
				m := map[string]any{"role": "assistant", "content": strings.Join(texts, "\n")}
				if len(calls) > 0 {
					m["tool_calls"] = calls
				}
				out = append(out, m)
			}
			continue
		}

		// user（或其它角色）：文本/图片聚合成一条，tool_result 单独成 tool 消息
		var texts []string
		var parts []map[string]any
		hasImage := false
		flushUser := func() {
			if len(texts) == 0 && !hasImage {
				return
			}
			var content any
			if hasImage {
				content = parts
			} else {
				content = strings.Join(texts, "\n")
			}
			out = append(out, map[string]any{"role": role, "content": content})
			texts = nil
			parts = nil
			hasImage = false
		}
		for _, braw := range blocks {
			block, ok := braw.(map[string]any)
			if !ok {
				continue
			}
			switch asString(block["type"]) {
			case "text":
				if t := asString(block["text"]); t != "" {
					texts = append(texts, t)
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			case "image":
				if url := anthropicImageURL(block); url != "" {
					hasImage = true
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
				}
			case "tool_result":
				flushUser()
				out = append(out, map[string]any{
					"role":         "tool",
					"tool_call_id": asString(block["tool_use_id"]),
					"content":      anthropicToolResultText(block["content"]),
				})
			case "thinking", "redacted_thinking":
				continue
			}
		}
		flushUser()
	}
	return out
}
func anthropicBlocksToText(blocks []any) string {
	var texts []string
	for _, braw := range blocks {
		if block, ok := braw.(map[string]any); ok && asString(block["type"]) == "text" {
			if t := asString(block["text"]); t != "" {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func anthropicImageURL(block map[string]any) string {
	src, _ := block["source"].(map[string]any)
	if src == nil {
		return ""
	}
	switch asString(src["type"]) {
	case "base64":
		media := firstNonEmpty(asString(src["media_type"]), "image/png")
		data := asString(src["data"])
		if data == "" {
			return ""
		}
		return "data:" + media + ";base64," + data
	case "url":
		return asString(src["url"])
	}
	return ""
}

func anthropicToolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var texts []string
		for _, raw := range v {
			if m, ok := raw.(map[string]any); ok {
				if asString(m["type"]) == "text" {
					if t := asString(m["text"]); t != "" {
						texts = append(texts, t)
					}
				}
				continue
			}
			if s, ok := raw.(string); ok {
				texts = append(texts, s)
			}
		}
		return strings.Join(texts, "\n")
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func anthropicToolsToChat(tools any) []map[string]any {
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
		name := asString(t["name"])
		if name == "" {
			continue
		}
		fn := map[string]any{
			"name":       name,
			"parameters": defaultObject(t["input_schema"]),
		}
		if d := asString(t["description"]); d != "" {
			fn["description"] = d
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func anthropicToolChoiceToChat(tc any) (any, bool) {
	m, ok := tc.(map[string]any)
	if !ok {
		return nil, false
	}
	switch asString(m["type"]) {
	case "auto":
		return "auto", true
	case "any":
		return "required", true
	case "none":
		return "none", true
	case "tool":
		if name := asString(m["name"]); name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}, true
		}
	}
	return nil, false
}

/* ───────────────────────── count_tokens ───────────────────────── */

// anthropicCountTokens 返回输入 token 的**估算值**。
//
// 为什么是估算：网关没有上游分词器，也无法为任意模型精确复现 tokenizer。
// Claude Code 用它做上下文预算，量级正确即可（宁可高估，不要低估）。
// 口径：文本按「字符数/3.2」估（中英混排的经验值，偏保守），图片按固定 1600，
// 每条消息 +4 的帧开销。
func (h *Handler) anthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAnthropicBody))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "read body: "+err.Error())
		return
	}
	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid json: "+err.Error())
		return
	}
	var text strings.Builder
	images := 0
	// 递归收集文本（content 可能是字符串 / block 数组 / 嵌套结构），
	// 需要先声明再赋值才能自引用。
	var collect func(v any)
	collect = func(v any) {
		switch t := v.(type) {
		case string:
			text.WriteString(t)
		case []any:
			for _, raw := range t {
				if m, ok := raw.(map[string]any); ok {
					switch asString(m["type"]) {
					case "image":
						images++
					case "text":
						text.WriteString(asString(m["text"]))
					default:
						collect(m["content"])
						collect(m["input"])
					}
				}
			}
		}
	}
	collect(src["system"])
	collect(src["messages"])
	collect(src["tools"])

	runes := 0
	for range text.String() {
		runes++
	}
	estimate := int(float64(runes)/3.2) + images*1600 + 16
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": estimate})
}

/* ───────────────────────── 响应翻译：Chat SSE → Anthropic SSE ───────────────────────── */

type anthropicToolBlock struct {
	id     string
	name   string
	index  int
	args   strings.Builder
	closed bool
}

type anthropicWriter struct {
	dst        http.ResponseWriter
	flusher    http.Flusher
	model      string
	wantStream bool

	header      http.Header
	status      int
	parseBuf    strings.Builder
	passthrough bool
	// errStatus / errBuf 非 200 时暂存上游/网关的错误响应，finish() 里改写成
	// Anthropic 错误信封再发出。为什么不能原样透传：chat 侧错误是 OpenAI 形状
	// （{"error":{"code","message"}}），而 Claude 系客户端只认
	// {"type":"error","error":{"type","message"}}——原样透传会让它显示成
	// "unknown error"，把真正的原因（配额/白名单/鉴权）藏掉。
	errStatus int
	errBuf    strings.Builder

	started   bool
	msgID     string
	seq       int
	textIndex int // -1 表示尚未开文本块
	textOpen  bool
	accText   strings.Builder

	tools        map[int]*anthropicToolBlock
	nextIdx      int
	usage        map[string]any
	finishReason string
	failed       bool
	errMsg       string
}

func newAnthropicWriter(dst http.ResponseWriter, model string, wantStream bool) *anthropicWriter {
	w := &anthropicWriter{
		dst:        dst,
		model:      model,
		wantStream: wantStream,
		header:     http.Header{},
		msgID:      "msg_" + randHex(12),
		textIndex:  -1,
		tools:      map[int]*anthropicToolBlock{},
	}
	if f, ok := dst.(http.Flusher); ok {
		w.flusher = f
	}
	return w
}

func (w *anthropicWriter) Header() http.Header { return w.header }

func (w *anthropicWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	if code != http.StatusOK {
		w.passthrough = true
		w.errStatus = code // 先不写头：等 finish() 改写成 Anthropic 信封
	}
}

func (w *anthropicWriter) Write(p []byte) (int, error) {
	if w.passthrough {
		w.errBuf.Write(p)
		return len(p), nil
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

func (w *anthropicWriter) Flush() {
	if w.flusher != nil && (w.started || w.passthrough) {
		w.flusher.Flush()
	}
}

func (w *anthropicWriter) handleBlock(block string) {
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
		w.consume(chunk)
	}
}

func (w *anthropicWriter) consume(chunk map[string]any) {
	if e, ok := chunk["error"]; ok && e != nil {
		w.failed = true
		w.errMsg = asString(mapOf(e)["message"])
		if w.errMsg == "" {
			b, _ := json.Marshal(e)
			w.errMsg = string(b)
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
		w.openText()
		w.accText.WriteString(c)
		w.emit("content_block_delta", map[string]any{
			"index": w.textIndex,
			"delta": map[string]any{"type": "text_delta", "text": c},
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
			blk := w.tools[idx]
			if blk == nil {
				blk = w.openTool(idx, tc)
			}
			fn, _ := tc["function"].(map[string]any)
			if frag := asString(fn["arguments"]); frag != "" {
				blk.args.WriteString(frag)
				w.emit("content_block_delta", map[string]any{
					"index": blk.index,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": frag},
				})
			}
		}
	}
}

func (w *anthropicWriter) ensureStarted() {
	if w.started {
		return
	}
	w.started = true
	if !w.wantStream {
		return
	}
	h := w.dst.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.dst.WriteHeader(http.StatusOK)
	w.emit("message_start", map[string]any{
		"message": map[string]any{
			"id": w.msgID, "type": "message", "role": "assistant", "model": w.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": w.inputTokens(), "output_tokens": 0},
		},
	})
	w.emit("ping", map[string]any{})
}

func (w *anthropicWriter) openText() {
	if w.textOpen {
		return
	}
	w.textOpen = true
	w.textIndex = w.nextIdx
	w.nextIdx++
	w.emit("content_block_start", map[string]any{
		"index":         w.textIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (w *anthropicWriter) openTool(idx int, tc map[string]any) *anthropicToolBlock {
	fn, _ := tc["function"].(map[string]any)
	blk := &anthropicToolBlock{
		id:    firstNonEmpty(asString(tc["id"]), "toolu_"+randHex(10)),
		name:  asString(fn["name"]),
		index: w.nextIdx,
	}
	w.nextIdx++
	w.tools[idx] = blk
	w.emit("content_block_start", map[string]any{
		"index": blk.index,
		"content_block": map[string]any{
			"type": "tool_use", "id": blk.id, "name": blk.name, "input": map[string]any{},
		},
	})
	return blk
}

func (w *anthropicWriter) emit(event string, fields map[string]any) {
	if w.passthrough || !w.wantStream {
		return
	}
	obj := map[string]any{"type": event}
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

func (w *anthropicWriter) inputTokens() float64 {
	if w.usage != nil {
		return asFloat(w.usage["prompt_tokens"])
	}
	return 0
}

func (w *anthropicWriter) outputTokens() float64 {
	if w.usage != nil {
		if v := asFloat(w.usage["completion_tokens"]); v > 0 {
			return v
		}
	}
	// usage 缺失时按文本长度粗估，避免 usage.output_tokens 恒为 0
	runes := 0
	for range w.accText.String() {
		runes++
	}
	return float64(int(float64(runes) / 3.2))
}

func (w *anthropicWriter) finish() {
	if w.passthrough {
		w.flushError()
		return
	}
	if !w.wantStream {
		w.writeJSONMessage()
		return
	}
	w.ensureStarted()
	if w.failed {
		w.emit("error", map[string]any{
			"error": map[string]any{"type": "api_error", "message": w.errMsg},
		})
		return
	}
	if w.textOpen {
		w.emit("content_block_stop", map[string]any{"index": w.textIndex})
	}
	idxs := make([]int, 0, len(w.tools))
	for i := range w.tools {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		blk := w.tools[i]
		if blk.closed {
			continue
		}
		blk.closed = true
		w.emit("content_block_stop", map[string]any{"index": blk.index})
	}
	w.emit("message_delta", map[string]any{
		"delta": map[string]any{
			"stop_reason":   anthropicStopReason(w.finishReason, len(w.tools) > 0),
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": w.outputTokens()},
	})
	w.emit("message_stop", map[string]any{})
}

func (w *anthropicWriter) writeJSONMessage() {
	if !w.started {
		w.dst.Header().Set("Content-Type", "application/json")
		w.dst.WriteHeader(http.StatusOK)
	}
	content := make([]map[string]any, 0, 1+len(w.tools))
	if w.textOpen {
		content = append(content, map[string]any{"type": "text", "text": w.accText.String()})
	}
	idxs := make([]int, 0, len(w.tools))
	for i := range w.tools {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		blk := w.tools[i]
		args := blk.args.String()
		if args == "" {
			args = "{}"
		}
		var input any
		if err := json.Unmarshal([]byte(args), &input); err != nil {
			input = map[string]any{}
		}
		content = append(content, map[string]any{
			"type": "tool_use", "id": blk.id, "name": blk.name, "input": input,
		})
	}
	msg := map[string]any{
		"id": w.msgID, "type": "message", "role": "assistant", "model": w.model,
		"content":       content,
		"stop_reason":   anthropicStopReason(w.finishReason, len(w.tools) > 0),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  w.inputTokens(),
			"output_tokens": w.outputTokens(),
		},
	}
	b, _ := json.Marshal(msg)
	_, _ = w.dst.Write(b)
}

// writeAnthropicError 按 Anthropic 的错误信封返回，便于 Claude 系客户端解析。
func writeAnthropicError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": code, "message": msg},
	})
	_, _ = w.Write(b)
}

// resolveAnthropicModel 把客户端请求的模型名解析成上游真实 id。
//
// 两级：Aliases 精确命中优先；否则若名字是 claude-* 前缀（Anthropic 原生命名）
// 且配置了 anthropic.default_model，则回落到它。都没命中则原样返回——网关保持
// 透明，让上游自己报「模型不可用」，而不是悄悄换成别的模型。
func (h *Handler) resolveAnthropicModel(name string) string {
	if h == nil {
		return name
	}
	if mapped, ok := h.cfg.AnthropicAliases[name]; ok && strings.TrimSpace(mapped) != "" {
		return mapped
	}
	if d := strings.TrimSpace(h.cfg.AnthropicDefaultModel); d != "" &&
		strings.HasPrefix(strings.ToLower(name), "claude") {
		return d
	}
	return name
}

// flushError 把暂存的 OpenAI 形状错误改写成 Anthropic 信封后发出。
//
// 映射规则与官方 SDK 对齐：401→authentication_error、403→permission_error、
// 429→rate_limit_error、400→invalid_request_error、5xx→api_error。
// 原始 message 与 code 一并保留（Anthropic 允许额外字段，排障时能看到网关侧 code）。
func (w *anthropicWriter) flushError() {
	status := w.errStatus
	if status == 0 {
		status = http.StatusBadGateway
	}
	msg := strings.TrimSpace(w.errBuf.String())
	var code string
	// 尝试从 OpenAI 信封里取 message/code
	var openai struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if msg != "" && json.Unmarshal([]byte(msg), &openai) == nil && openai.Error.Message != "" {
		code = openai.Error.Code
		msg = openai.Error.Message
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	// 非 JSON 错误体（HTML 错误页 / 纯文本）保留原文但截断：原文比状态码文案更有
	// 排障价值，而整页 HTML 塞给客户端既难读又可能撑爆日志。
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	typ := "api_error"
	switch {
	case status == http.StatusUnauthorized:
		typ = "authentication_error"
	case status == http.StatusForbidden:
		typ = "permission_error"
	case status == http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case status >= 400 && status < 500:
		typ = "invalid_request_error"
	}
	body := map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": msg},
	}
	if code != "" {
		body["error"].(map[string]any)["code"] = code
	}
	b, _ := json.Marshal(body)
	w.dst.Header().Set("Content-Type", "application/json")
	w.dst.WriteHeader(status)
	_, _ = w.dst.Write(b)
}

// writeAnthropicErrorCoded 按 Anthropic 规范返回错误：error.type 是**类别**
// （authentication_error / permission_error / rate_limit_error / api_error），
// 网关自己的细分原因放进 error.code。
//
// 为什么不能把 code 直接当 type：Claude 系客户端按 type 分类处理（401 触发重新登录、
// 429 触发退避重试），拿到 type=token_quota_exceeded 会当成未知错误直接抛给用户。
func writeAnthropicErrorCoded(w http.ResponseWriter, status int, code, msg string) {
	typ := "api_error"
	switch {
	case status == http.StatusUnauthorized:
		typ = "authentication_error"
	case status == http.StatusForbidden:
		typ = "permission_error"
	case status == http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case status >= 400 && status < 500:
		typ = "invalid_request_error"
	}
	payload := map[string]any{"type": typ, "message": msg}
	if code != "" {
		payload["code"] = code
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"type": "error", "error": payload})
	_, _ = w.Write(b)
}
