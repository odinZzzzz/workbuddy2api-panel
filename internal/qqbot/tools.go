package qqbot

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// tools.go —— 给模型的工具（function calling）。
//
// 为什么值得做：桥里已经有一堆能力（发表情、自绘表情、戳一戳、查记忆、记笔记），
// 以前全靠关键词硬匹配——用户得猜"说哪个词才触发"。做成工具后，模型自己判断
// "该画图还是该查记忆"，人设也不用背一堆指令。
//
// 安全边界：工具是**白名单注册**的，参数只接受 JSON schema 里声明的字段；
// 发送类工具只作用于当前会话（不给模型任意指定群/人的能力），避免被诱导成广播器。

// ToolCall 模型请求的一次工具调用。
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolDef OpenAI tools 定义。
type ToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

// toolContext 工具执行上下文：只给"当前会话"，不给任意目标。
type toolContext struct {
	ev *Event
	b  *Bridge
}

// toolRegistry 工具名 → 定义 + 执行函数。
type toolRegistry struct {
	defs  []ToolDef
	execs map[string]func(tc toolContext, args json.RawMessage) (string, error)
}

// buildTools 构造工具集（参数顺序即模型看到的顺序）。
func (b *Bridge) buildTools() *toolRegistry {
	r := &toolRegistry{execs: map[string]func(toolContext, json.RawMessage) (string, error){}}

	add := func(name, desc string, params map[string]any, fn func(toolContext, json.RawMessage) (string, error)) {
		var d ToolDef
		d.Type = "function"
		d.Function.Name = name
		d.Function.Description = desc
		d.Function.Parameters = params
		r.defs = append(r.defs, d)
		r.execs[name] = fn
	}

	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}

	add("draw_sticker", "自己画一张表情包并发到当前会话（SVG 源码，≤64KB）。只在真的想发表情时调用。"+stickerStyleGuide,
		obj(map[string]any{
			"svg": map[string]any{"type": "string", "description": "完整 SVG 源码，必须以 <svg 开头，含 xmlns"},
		}, "svg"),
		func(tc toolContext, args json.RawMessage) (string, error) {
			var in struct {
				SVG string `json:"svg"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("参数解析失败: %w", err)
			}
			png, err := RenderSVG(in.SVG, 256)
			if err != nil {
				return "", err
			}
			// 空白自纠：渲染成功但几乎没内容，通常是用到了不支持的 <text>/<g transform>/clipPath。
			// 把原因回给模型，它可以在同一轮里改对再画（比静默发出一张空白图好得多）。
			if ink, e := inkPixels(png); e == nil && ink < 300 {
				return "", fmt.Errorf("画出来几乎空白（非透明像素 %d）。"+
					"检查是否用了不支持的 <text>、<g transform>、clipPath——这些不会渲染；"+
					"请只用 path/circle/ellipse/rect/polygon/polyline/line，坐标写绝对坐标", ink)
			}
			path, err := tc.b.SaveGeneratedSticker(png)
			if err != nil {
				return "", err
			}
			if err := tc.b.sendImage(tc.ev, path); err != nil {
				return "", err
			}
			tc.b.state.note(RecentEvent{Type: "tool", Group: tc.ev.GroupID, User: tc.ev.UserID, Action: "drawn_sticker"})
			return "已把自绘表情包发到当前会话", nil
		})

	add("list_sticker_templates", "列出可用的名画/风格 SVG 模板（神奈川冲浪里、星月夜、呐喊、珍珠耳环等）。想画「有名画感」的表情包时先看这里。",
		obj(map[string]any{}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			var sb strings.Builder
			for _, tp := range tc.b.ListTemplates() {
				sb.WriteString("- " + tp.Name + "：" + tp.Desc + "\n")
			}
			return sb.String(), nil
		})

	add("get_sticker_template", "取某个模板的完整 SVG 源码。拿到后改配色/换表情/加元素，再用 draw_sticker 画出来。",
		obj(map[string]any{
			"name": map[string]any{"type": "string", "description": "模板名，如 wave / starry / scream / pearl"},
		}, "name"),
		func(tc toolContext, args json.RawMessage) (string, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			return tc.b.TemplateSource(in.Name)
		})

	add("send_sticker", "从表情库里随机发一张表情包到当前会话。",
		obj(map[string]any{}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			ref := tc.b.pickSticker()
			if ref == "" {
				return "表情库是空的，没发出去", nil
			}
			if err := tc.b.sendImage(tc.ev, ref); err != nil {
				return "", err
			}
			tc.b.state.note(RecentEvent{Type: "tool", Group: tc.ev.GroupID, User: tc.ev.UserID, Action: "library_sticker"})
			return "已发一张表情包", nil
		})

	add("poke_user", "戳一戳当前跟你说话的人（QQ 的戳一戳动作）。",
		obj(map[string]any{}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			if err := tc.b.sendPoke(tc.ev); err != nil {
				return "", err
			}
			return "已戳对方一下", nil
		})

	add("recall_user", "查你对某个人的记忆（次数/原话/点评/好感度/画像）。默认查当前说话人；要看别人就传 uid（本条消息 @ 的人）。",
		obj(map[string]any{
			"uid": map[string]any{"type": "integer", "description": "要查的 QQ 号；不传=当前说话人"},
		}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			var in struct {
				UID int64 `json:"uid"`
			}
			_ = json.Unmarshal(args, &in)
			if in.UID > 0 {
				return tc.b.summaryOf(tc.ev, strconvFormat(in.UID), nil), nil
			}
			return tc.b.summaryOf(tc.ev, "", nil), nil
		})

	add("remember_note", "往对这个人的记忆里追加一条笔记（比如他的偏好、身份、约定）。简短一句话。",
		obj(map[string]any{
			"note": map[string]any{"type": "string", "description": "要记住的一句话，≤60 字"},
		}, "note"),
		func(tc toolContext, args json.RawMessage) (string, error) {
			var in struct {
				Note string `json:"note"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("参数解析失败: %w", err)
			}
			note := strings.TrimSpace(in.Note)
			if note == "" {
				return "空笔记，忽略", nil
			}
			if tc.b.mem != nil {
				tc.b.mem.Observe(tc.ev.UserID, "", "[笔记] "+note)
			}
			return "记住了", nil
		})

	return r
}

// exec 执行一次工具调用。
func (r *toolRegistry) exec(tc toolContext, call ToolCall) (string, error) {
	fn, ok := r.execs[call.Function.Name]
	if !ok {
		return "", fmt.Errorf("未知工具 %q", call.Function.Name)
	}
	log.Printf("[qqbot] tool: %s(%s)", call.Function.Name, truncateRunes(call.Function.Arguments, 120))
	return fn(tc, json.RawMessage(call.Function.Arguments))
}

// strconvFormat 小工具：把 uid 转字符串（避免为一个转换到处引 strconv）。
func strconvFormat(v int64) string { return itoa(v) }
