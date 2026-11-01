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
	NoticeType  string          `json:"notice_type"`
	TargetID    int64           `json:"target_id"`
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
		File string `json:"file"`
		URL  string `json:"url"`
	} `json:"data"`
}

// ParseImages 抽出消息里的图片/表情（file 或 url），返回可用的引用列表。
//
// 为什么返回"引用"而不是直接下载：下载要落盘、要限流、还要判断是不是超大图；
// 先拿到引用，需要时再取（发送时 NapCat 也接受 url/file 直接引用）。
func ParseImages(raw json.RawMessage) []string {
	var out []string
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return out
	}
	var segs []segment
	if json.Unmarshal(raw, &segs) != nil {
		return out
	}
	for _, sg := range segs {
		if sg.Type == "image" || sg.Type == "mface" {
			ref := sg.Data.URL
			if ref == "" {
				ref = sg.Data.File
			}
			if ref != "" {
				out = append(out, ref)
			}
		}
	}
	return out
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
// 支持：/help /clear /new /model /ping /summary(/whois) 以及中文「总结」。
// 其余一律当聊天内容。
func Command(text string) (name, arg string, ok bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", "", false
	}
	// 中文指令：允许不带斜杠（群里没人爱打斜杠）
	for _, zh := range []string{"总结"} {
		if strings.HasPrefix(t, zh) {
			return "/summary", strings.TrimSpace(strings.TrimPrefix(t, zh)), true
		}
	}
	if !strings.HasPrefix(t, "/") {
		return "", "", false
	}
	parts := strings.SplitN(t, " ", 2)
	name = strings.ToLower(strings.TrimSpace(parts[0]))
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	switch name {
	case "/help", "/clear", "/new", "/model", "/ping", "/summary", "/whois":
		return name, arg, true
	default:
		return "", "", false
	}
}

// ParseAtTargets 抽出消息里被 @ 的所有 QQ 号（用于「总结 @某人」这类指令）。
func ParseAtTargets(raw json.RawMessage, rawMessage string) []int64 {
	var out []int64
	seen := map[int64]bool{}
	add := func(v string) {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && strings.HasPrefix(trimmed, "[") {
		var segs []segment
		if json.Unmarshal(raw, &segs) == nil {
			for _, sg := range segs {
				if sg.Type == "at" {
					add(sg.Data.QQ)
				}
			}
			return out
		}
	}
	// string 形态：扫 [CQ:at,qq=xxx]
	rest := rawMessage
	for {
		i := strings.Index(rest, "[CQ:at,qq=")
		if i < 0 {
			break
		}
		rest = rest[i+len("[CQ:at,qq="):]
		j := strings.IndexByte(rest, ']')
		if j < 0 {
			break
		}
		add(rest[:j])
		rest = rest[j+1:]
	}
	return out
}

// chatKey 会话键。
//
// per_user（默认）：群聊按「群+人」隔离——龙王记得住每个人各自说过什么，A 的梗
// 不会串到 B 身上；私聊天然按人。
// per_chat：整个群共享一段上下文（旧行为）。
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

// displayName 日志/记忆里用的人名（优先群名片）。
func displayName(ev *Event) string {
	if ev.Sender.Card != "" {
		return ev.Sender.Card
	}
	if ev.Sender.Nickname != "" {
		return ev.Sender.Nickname
	}
	return strconv.FormatInt(ev.UserID, 10)
}
