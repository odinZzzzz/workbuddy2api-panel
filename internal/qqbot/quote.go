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
	"log"
	"strings"
)

// 上限刻意放宽（用户要求"别限制了"）：聊天记录是重要上下文，裁剪太狠等于没有。
// 只在极端情况下兜底，避免一次转发把 prompt 撑爆（单条消息上限约 6000 字）。
const maxQuoteRunes = 6000

// maxForwardItems 合并转发最多回填多少条（放宽到 60，覆盖绝大多数聊天记录）。
const maxForwardItems = 60

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
	if replyID != 0 || forwardID != "" {
		log.Printf("[qqbot] quote ids: reply=%d forward=%q", replyID, forwardID)
	}
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
//
// 特例：被引用的可能是**别人转发的聊天记录合集**（forward）。那种消息 get_msg
// 只会返回一个 forward 段（里面只有一个 id），正文要再 get_forward_msg 展开。
// 实测这就是"引用了聊天记录却读不到"的原因：只回填成 [聊天记录] 占位符。
// 只展开一层（嵌套转发不再递归），避免套娃把 prompt 撑爆。
func (b *Bridge) fetchQuotedMessage(id int64) string {
	res, err := b.callActionResult("get_msg", map[string]any{"message_id": id})
	if err != nil {
		log.Printf("[qqbot] 引用回填失败 get_msg(%d): %v", id, err)
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

	// 被引用的是合并转发 → 展开聊天记录正文（一层）
	if fid := forwardIDOf(res["message"]); fid != "" {
		if inner := b.fetchForwardMessages(fid); inner != "" {
			body = "聊天记录：" + inner
		}
	}
	if body == "" {
		return ""
	}
	if sender != "" {
		return truncateRunes(sender+"说："+body, maxQuoteRunes)
	}
	return truncateRunes(body, maxQuoteRunes)
}

// forwardIDOf 从消息体里取 forward 段的 id（没有则空）。
func forwardIDOf(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != "forward" {
			continue
		}
		data, _ := m["data"].(map[string]any)
		if id, _ := data["id"].(string); id != "" {
			return id
		}
	}
	return ""
}

// fetchForwardMessages 用 get_forward_msg 取合并转发里的前若干条。
func (b *Bridge) fetchForwardMessages(id string) string {
	res, err := b.callActionResult("get_forward_msg", map[string]any{"message_id": id})
	if err != nil {
		log.Printf("[qqbot] 聊天记录回填失败 get_forward_msg(%s): %v", id, err)
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
//
// 关键：**合并转发的内容直接嵌在 forward 段的 data.content 里**（实测 NapCat 的
// get_forward_msg 返回就是这样，嵌套时 content 里还有 content），不用再请求一次。
// 之前这里只输出 "[聊天记录]" 占位符 —— 那就是"引用了聊天记录却读不到"的根因。
func flattenSegments(v any) string {
	return flattenSegmentsDepth(v, 0)
}

// maxForwardDepth 转发嵌套展开层数上限（实测有套娃转发，防无限递归）。
const maxForwardDepth = 3

func flattenSegmentsDepth(v any, depth int) string {
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
			// 转发 content 里装的是**完整消息对象**（{sender, message:[…]}），
			// 没有 type 字段。之前只认 type → 全部跳过，展开结果只剩占位符
			// （用线上真实样本一测就暴露了）。这里补上这种形态。
			if typ == "" {
				if inner, ok := m["message"]; ok {
					nick := ""
					if s, ok := m["sender"].(map[string]any); ok {
						if n, _ := s["nickname"].(string); n != "" {
							nick = n
						} else if c, _ := s["card"].(string); c != "" {
							nick = c
						}
					}
					body := flattenSegmentsDepth(inner, depth)
					if body != "" {
						if nick != "" {
							parts = append(parts, nick+"："+body)
						} else {
							parts = append(parts, body)
						}
					}
					continue
				}
			}
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
				// 展开聊天记录正文（content 里是消息数组）
				if depth >= maxForwardDepth {
					parts = append(parts, "[聊天记录]")
					continue
				}
				if content, ok := data["content"]; ok {
					if inner := flattenSegmentsDepth(content, depth+1); inner != "" {
						parts = append(parts, "〔聊天记录："+inner+"〕")
						continue
					}
				}
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
