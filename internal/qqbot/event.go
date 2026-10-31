package qqbot

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Event OneBot v11 上报事件里本服务用到的字段子集。
//
// 只解析需要的字段：OneBot 事件体很大（含 sender/群名片/头像等），全量结构体既
// 冗长又容易随上游版本漂移。用 json.RawMessage 接 message，再按需解析。
type Event struct {
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	SelfID      int64           `json:"self_id"`
	GroupID     int64           `json:"group_id"`
	UserID      int64           `json:"user_id"`
	MessageID   int64           `json:"message_id"`
	RawMessage  string          `json:"raw_message"`
	Message     json.RawMessage `json:"message"`
	Sender      struct {
		Nickname string `json:"nickname"`
		Card     string `json:"card"`
	} `json:"sender"`
}

// segment OneBot 数组格式消息段。
type segment struct {
	Type string `json:"type"`
	Data struct {
		Text string `json:"text"`
		QQ   string `json:"qq"`
	} `json:"data"`
}

// ParseText 从 message 字段抽出纯文本，并报告是否 @ 了指定机器人。
//
// 兼容两种上报格式：
//   - messagePostFormat=array：[{type:"text",data:{text:"hi"}},{type:"at",data:{qq:"123"}}]
//   - messagePostFormat=string："[CQ:at,qq=123] hi"
//
// 为什么要自己抽文本：网关只吃纯文本；@ 段、图片段、表情段对模型都是噪声。
func ParseText(raw json.RawMessage, rawMessage string, selfID int64) (text string, atBot bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && strings.HasPrefix(trimmed, "[") {
		var segs []segment
		if err := json.Unmarshal(raw, &segs); err == nil {
			var b strings.Builder
			for _, s := range segs {
				switch s.Type {
				case "text":
					b.WriteString(s.Data.Text)
				case "at":
					if selfID != 0 && (s.Data.QQ == strconv.FormatInt(selfID, 10) || s.Data.QQ == "all") {
						atBot = true
					}
				case "face", "image", "mface", "record", "video":
					// 非文本段直接丢弃
				}
			}
			return strings.TrimSpace(b.String()), atBot
		}
	}
	// string 形态（或 array 解析失败时的兜底）：剥 CQ 码
	src := rawMessage
	if strings.TrimSpace(src) == "" && trimmed != "" {
		src = trimmed
	}
	if selfID != 0 {
		marker := fmt.Sprintf("[CQ:at,qq=%d]", selfID)
		if strings.Contains(src, marker) || strings.Contains(src, "[CQ:at,qq=all]") {
			atBot = true
		}
	}
	return strings.TrimSpace(stripCQ(src)), atBot
}

// stripCQ 去掉 [CQ:...] 标记，保留可见文本。
func stripCQ(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "[CQ:")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i:], "]")
		if j < 0 {
			break
		}
		s = s[i+j+1:]
	}
	return b.String()
}

// Command 解析内置指令；返回 (指令名, 参数, 是否是指令)。
//
// 支持的指令刻意少：/help /clear /model /ping。其余一律当聊天内容。
func Command(text string) (name, arg string, ok bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") {
		return "", "", false
	}
	parts := strings.SplitN(t, " ", 2)
	name = strings.ToLower(strings.TrimSpace(parts[0]))
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	switch name {
	case "/help", "/clear", "/model", "/ping", "/new":
		return name, arg, true
	default:
		return "", "", false
	}
}

// chatKey 会话键。
//
// per_user（默认）：群聊按「群+人」隔离——龙王记得住每个人各自说过什么，A 的梗
// 不会串到 B 身上；私聊天然按人。
// per_chat：整个群共享一段上下文（旧行为，适合"群里是一个集体"的场景）。
func chatKey(ev *Event, scopePerUser bool) string {
	if ev.MessageType == "group" {
		key := "group:" + strconv.FormatInt(ev.GroupID, 10)
		if scopePerUser {
			key += ":u:" + strconv.FormatInt(ev.UserID, 10)
		}
		return key
	}
	return "private:" + strconv.FormatInt(ev.UserID, 10)
}

// displayName 日志里用的人名（优先群名片）。
func displayName(ev *Event) string {
	if ev.Sender.Card != "" {
		return ev.Sender.Card
	}
	if ev.Sender.Nickname != "" {
		return ev.Sender.Nickname
	}
	return strconv.FormatInt(ev.UserID, 10)
}
