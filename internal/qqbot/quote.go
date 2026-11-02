package qqbot

// quote.go 引用回复（reply）与合并转发（forward/chat history）的内容回填。
//
// 为什么需要：
// - **引用回复**：OneBot 的 reply 段只给一个 message_id，正文在 `get_msg` 里。
//   不回填的话，模型看到的只是「（回复了一条消息）」，答非所问。
// - **合并转发**：forward 段同样只有 id，正文要 `get_forward_msg` 拉。
//   群里转发的"聊天记录"是最常见的上下文来源，之前完全丢弃。
//
// 两者都走新增的 callActionResult（能拿返回 data），并把内容裁剪后再拼进 userText：
// 转发的聊天记录可以很长，不裁剪会把 token 撑爆。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// maxQuoteRunes 引用/转发内容回填的字符上限（超出截断，避免撑爆 prompt）。
const maxQuoteRunes = 300

// maxForwardItems 合并转发最多回填多少条。
const maxForwardItems = 12

// parseQuoteIDs 从消息段里提取引用回复 id 与合并转发 id。
func parseQuoteIDs(raw json.RawMessage) (replyID int64, forwardID string) {
	segs := parseSegments(raw)
	for _, sg := range segs {
		switch sg.Type {
		case "reply":
			if replyID == 0 {
				replyID = parseIntAny(sg.Data.ID, sg.Data.Text)
			}
		case "forward":
			if forwardID == "" {
				forwardID = sg.Data.ID
			}
		}
	}
	return
}

// parseSegments 把 message 数组解析成段（复用 event.go 的 segment 定义）。
func parseSegments(raw json.RawMessage) []segment {
	var out []segment
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return out
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// quoteContext 回填引用/转发内容，返回要拼进 userText 的前缀（空 = 无需拼接）。
func (b *Bridge) quoteContext(raw json.RawMessage) string {
	replyID, forwardID := parseQuoteIDs(raw)
	var parts []string

	if replyID != 0 {
		if txt := b.fetchQuotedMessage(replyID); txt != "" {
			parts = append(parts, "（引用上文："+txt+"）")
		}
	}
	if forwardID != "" {
		if txt := b.fetchForwardMessages(forwardID); txt != "" {
			parts = append(parts, "（转发的聊天记录："+txt+"）")
		}
	}
	return strings.Join(parts, " ")
}

// fetchQuotedMessage 用 get_msg 取被引用消息的正文。
func (b *Bridge) fetchQuotedMessage(id int64) string {
	res, err := b.callActionResult("get_msg", map[string]any{"message_id": id})
	if err != nil {
		return ""
	}
	// get_msg 返回 {message_id, real_id, sender, time, message}
	sender := ""
	if s, ok := res["sender"].(map[string]any); ok {
		if n, _ := s["nickname"].(string); n != "" {
			sender = n
		} else if c, _ := s["card"].(string); c != "" {
			sender = c
		}
	}
	body := flattenSegments(res["message"])
	if body == "" {
		return ""
	}
	if sender != "" {
		return truncateRunes(sender+"说："+body, maxQuoteRunes)
	}
	return truncateRunes(body, maxQuoteRunes)
}

// fetchForwardMessages 用 get_forward_msg 取合并转发里的前若干条。
func (b *Bridge) fetchForwardMessages(id string) string {
	res, err := b.callActionResult("get_forward_msg", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	items, ok := res["messages"].([]any)
	if !ok {
		return ""
	}
	var lines []string
	for _, it := range items {
		if len(lines) >= maxForwardItems {
			break
		}
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		sender := ""
		if s, ok := m["sender"].(map[string]any); ok {
			if n, _ := s["nickname"].(string); n != "" {
				sender = n
			} else if c, _ := s["card"].(string); c != "" {
				sender = c
			}
		}
		body := flattenSegments(m["message"])
		if body == "" {
			continue
		}
		if sender != "" {
			lines = append(lines, sender+"："+body)
		} else {
			lines = append(lines, body)
		}
	}
	return truncateRunes(strings.Join(lines, " | "), maxQuoteRunes*2)
}

// flattenSegments 把 message 字段（数组段或纯字符串）压成纯文本。
// 图片等无法取回正文的段用占位标记，避免模型以为"什么都没有"。
func flattenSegments(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		var parts []string
		for _, it := range t {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			data, _ := m["data"].(map[string]any)
			switch typ {
			case "text":
				if s, _ := data["text"].(string); s != "" {
					parts = append(parts, s)
				}
			case "image":
				parts = append(parts, "[图片]")
			case "at":
				parts = append(parts, "@某人")
			case "reply":
				// 不递归展开引用，避免无限套娃
			case "forward":
				parts = append(parts, "[聊天记录]")
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

// parseIntAny 从若干候选字符串里解析出第一个合法整数（reply 段的 id 可能是字符串或数字）。
func parseIntAny(vals ...string) int64 {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}
