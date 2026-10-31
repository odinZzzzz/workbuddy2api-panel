package qqbot

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
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

	add("daily_password", "查三角洲行动今日地图密码（零号大坝/长弓溪谷/巴克什/航天基地/潮汐监狱/AZ3）。数据来自 kkrb.net 实时接口，有 10 分钟缓存。有人问密码、密码门、今天密码时调用。",
		obj(map[string]any{}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			if !tc.b.cfg().KkrbEnabled {
				return "实时数据源已关闭（kkrb_enabled=false）", nil
			}
			ttl := time.Duration(tc.b.cfg().KkrbCacheMinutes) * time.Minute
			ov, err := kkrbSingleton.Overview(ttl)
			if err != nil {
				return "", fmt.Errorf("查每日密码失败: %w", err)
			}
			var sb strings.Builder
			sb.WriteString("今日地图密码：\n")
			for _, it := range ov.Passwords {
				sb.WriteString("- " + it.Map + "：" + it.Code)
				if it.Updated != "" {
					sb.WriteString("（" + it.Updated + " 更新）")
				}
				sb.WriteString("\n")
			}
			return sb.String(), nil
		})

	add("draw_chart", "把数据画成柱状图发到当前会话（图文结合）。items 传 [{label,value}]，最多 12 条。回答涉及数值对比（利润、价格、伤害）时用。",
		obj(map[string]any{
			"title": map[string]any{"type": "string", "description": "图标题，简短"},
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"label": map[string]any{"type": "string"},
						"value": map[string]any{"type": "number"},
					},
					"required": []string{"label", "value"},
				},
			},
		}, "title", "items"),
		func(tc toolContext, args json.RawMessage) (string, error) {
			var in struct {
				Title string      `json:"title"`
				Items []ChartItem `json:"items"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("参数解析失败: %w", err)
			}
			png, err := RenderBarChart(in.Title, in.Items, tc.b.cfg().ChartFont)
			if err != nil {
				return "", err
			}
			path, err := tc.b.SaveGeneratedSticker(png)
			if err != nil {
				return "", err
			}
			if err := tc.b.sendImage(tc.ev, path); err != nil {
				return "", err
			}
			tc.b.state.note(RecentEvent{Type: "tool", Group: tc.ev.GroupID, User: tc.ev.UserID, Action: "drawn_chart"})
			return fmt.Sprintf("已画出柱状图（%d 条数据）并发到当前会话", len(in.Items)), nil
		})

	add("chart_market", "把三角洲市场数据画成柱状图（一次调用：取数 + 画图 + 发送）。kind=craft（制造利润）/material（材料当前价）/ammo（子弹兑换利润）。",
		obj(map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"craft", "material", "ammo"}},
		}, "kind"),
		func(tc toolContext, args json.RawMessage) (string, error) {
			var in struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			ov, err := tc.b.kkrbOverview()
			if err != nil {
				return "", err
			}
			var title string
			var items []ChartItem
			switch in.Kind {
			case "material":
				title = "材料当前价"
				for _, m := range ov.Materials {
					items = append(items, ChartItem{Label: m.Item, Value: float64(m.Current), Icon: m.Pic})
				}
			case "ammo":
				title = "子弹兑换利润"
				for _, a := range ov.AmmoProfit {
					items = append(items, ChartItem{Label: a.Item, Value: a.Profit, Icon: a.Pic})
				}
			default:
				title = "特勤处制造利润"
				for _, c := range ov.Crafts {
					items = append(items, ChartItem{Label: c.Item, Value: c.Profit, Icon: c.Pic})
				}
			}
			if len(items) == 0 {
				return "没有可画的数据", nil
			}
			png, err := RenderBarChart(title, items, tc.b.cfg().ChartFont)
			if err != nil {
				return "", err
			}
			path, err := tc.b.SaveGeneratedSticker(png)
			if err != nil {
				return "", err
			}
			if err := tc.b.sendImage(tc.ev, path); err != nil {
				return "", err
			}
			tc.b.state.note(RecentEvent{Type: "tool", Group: tc.ev.GroupID, User: tc.ev.UserID, Action: "drawn_chart"})
			return fmt.Sprintf("已把「%s」画成柱状图并发送（%d 条）", title, len(items)), nil
		})

	add("market_craft", "查三角洲特勤处制造推荐（技术中心/工作台/制药台/防具台各一条：产物、利润、材料成本、建议卖出时段）。有人问制造赚什么、特勤处做什么时调用。",
		obj(map[string]any{}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			ov, err := tc.b.kkrbOverview()
			if err != nil {
				return "", err
			}
			if len(ov.Crafts) == 0 {
				return "今天没有制造推荐数据", nil
			}
			var sb strings.Builder
			sb.WriteString("特勤处制造推荐：\n")
			for _, c := range ov.Crafts {
				fmt.Fprintf(&sb, "- %s：%s，利润 %.0f（材料 %.0f / 售价 %.0f）", c.Place, c.Item, c.Profit, c.Material, c.Price)
				if c.SellHint != "" {
					sb.WriteString("，最高价时段 " + c.SellHint)
				}
				sb.WriteString("\n")
			}
			return sb.String(), nil
		})

	add("market_material", "查三角洲价格浮动大的制造材料（当前价、昨日最高/最低与时段）。有人问什么材料值得囤/什么时候买时调用。",
		obj(map[string]any{}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			ov, err := tc.b.kkrbOverview()
			if err != nil {
				return "", err
			}
			if len(ov.Materials) == 0 {
				return "今天没有材料浮动数据", nil
			}
			var sb strings.Builder
			sb.WriteString("材料价格浮动（当前 / 昨日高 / 昨日低）：\n")
			for _, m := range ov.Materials {
				fmt.Fprintf(&sb, "- %s：%d / %d（%s）/ %d（%s）\n", m.Item, m.Current, m.High, m.HighTime, m.Low, m.LowTime)
			}
			return sb.String(), nil
		})

	add("market_ammo", "查三角洲热门子弹与子弹兑换利润榜。有人问哪种子弹好卖/兑换子弹赚不赚时调用。",
		obj(map[string]any{}),
		func(tc toolContext, args json.RawMessage) (string, error) {
			ov, err := tc.b.kkrbOverview()
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			if len(ov.AmmoProfit) > 0 {
				sb.WriteString("子弹兑换利润（前 5）：\n")
				for i, a := range ov.AmmoProfit {
					if i >= 5 {
						break
					}
					fmt.Fprintf(&sb, "- %s：%.0f\n", a.Item, a.Profit)
				}
			}
			if len(ov.AmmoHot) > 0 {
				sb.WriteString("热门子弹（前 5，当前价）：\n")
				for i, a := range ov.AmmoHot {
					if i >= 5 {
						break
					}
					fmt.Fprintf(&sb, "- %s：%d\n", a.Item, a.Current)
				}
			}
			if sb.Len() == 0 {
				return "今天没有子弹数据", nil
			}
			return sb.String(), nil
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
