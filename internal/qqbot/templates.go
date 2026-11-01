package qqbot

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// templates.go —— 名画/风格模板库。
//
// 为什么给模板而不是让模型凭空画：凭空画容易画出"抽象到看不懂"的东西；给一幅
// 结构清晰的底稿（浪、星空、呐喊、珍珠耳环），模型只需要**改配色/换表情/加元素**，
// 出来的东西立刻有"名画感"。模板全部用手写 SVG，且只用 oksvg 实测支持的元素
// （无 <text>、无 <g transform>、无 clipPath），保证能渲染出来。
//
// 同时支持覆盖：sticker_template_dir 里放同名 .svg 会优先于内置版本，方便你自己
// 换素材而不必重新编译。

//go:embed templates/*.svg
var builtinTemplates embed.FS

// TemplateInfo 模板元信息（给模型看）。
type TemplateInfo struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// templateDesc 内置模板的中文说明（模型据此挑）。
var templateDesc = map[string]string{
	"wave":   "神奈川冲浪里（葛饰北斋）：蓝色巨浪+白色浪花+富士山，适合「浪」「翻」「冲」这类情绪",
	"starry": "星月夜（梵高）：旋转星空+月亮+柏树+小村，适合「失眠」「浪漫」「发疯」",
	"scream": "呐喊（蒙克）：橙红扭曲天空+桥上惊恐的人，适合「崩溃」「救命」「破防」",
	"pearl":  "戴珍珠耳环的少女（维米尔）：深色背景+侧脸+珍珠，适合「回眸」「不服」「高冷」",
	// ── 插图系模板：用户要"像普通图片"，就得有场景/主体/光影/层次。
	// 形状数 40~120 个、渲染 512px 以上；表情模板只适合"要个表情"的场合。
	"sunset-lake":  "黄昏湖景插图：紫橙渐变天空+落日+多层山影+水面倒影+芦苇剪影，适合「安静」「收工」「风景」",
	"cat-portrait": "橘猫半身像插图：柔光背景+绒毛渐变+琥珀眼+胡须+胸前白毛，适合「猫」「可爱」「陪我」",
	// ── 表情系模板（不是名画）：日常对话里"生气/无语/得意"出现频率最高，
	// 而模型凭空画这三种最容易崩（眉毛画成飘在脸外的黑棍、五官糊成一团）。
	// 给一张结构正确的底稿，模型只需改配色/换五官细节就能出成品。
	"angry":   "怒脸：黄圆脸+压在脸内的粗眉+咧嘴獠牙+右上怒火符号，适合「生气」「破防」「被气到」",
	"deadpan": "无语脸：冷灰底+白脸+两条闭眼横条+平嘴+右下汗滴，适合「无语」「叹气」「不想说话」",
	"smug":    "得意脸：深紫底+高光黄脸+一挑一平的眉+单眼眯+勾嘴笑+两颗星，适合「得意」「看戏」「我早说了」",
}

// ListTemplates 列出可用模板（内置 + 目录覆盖）。
func (b *Bridge) ListTemplates() []TemplateInfo {
	seen := map[string]bool{}
	out := make([]TemplateInfo, 0, 8)
	for name := range templateDesc {
		seen[name] = true
		out = append(out, TemplateInfo{Name: name, Desc: templateDesc[name]})
	}
	// 目录里的自定义模板
	if dir := strings.TrimSpace(b.cfg().StickerTemplateDir); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".svg") {
					continue
				}
				name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
				if seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, TemplateInfo{Name: name, Desc: "自定义模板（来自 " + dir + "）"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TemplateSource 取模板 SVG 源码（目录优先，其次内置）。
func (b *Bridge) TemplateSource(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\..") {
		return "", fmt.Errorf("模板名不合法: %q", name)
	}
	if dir := strings.TrimSpace(b.cfg().StickerTemplateDir); dir != "" {
		path := filepath.Join(dir, name+".svg")
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return string(raw), nil
		}
	}
	raw, err := builtinTemplates.ReadFile("templates/" + name + ".svg")
	if err != nil {
		return "", fmt.Errorf("没有模板 %q（可用：%s）", name, templateNamesCSV(b.ListTemplates()))
	}
	return string(raw), nil
}

func templateNamesCSV(list []TemplateInfo) string {
	names := make([]string, 0, len(list))
	for _, t := range list {
		names = append(names, t.Name)
	}
	return strings.Join(names, " / ")
}

// ── 模板"画法规格"提取 ──────────────────────────────────────────────
// 为什么需要：原来 get_sticker_template 直接返回 SVG 源码，模型拿到就原样照抄
// （线上实测：三次输出 sha256 与模板逐字节相同）——那不是"学会画"，那是复制粘贴。
// 现在只给**规格**：画布、部件结构、配色明度阶、线宽、形状数。模型只能照着规格
// 自己重画，没有可抄的源码。

// DescribeSVG 把一份 SVG 提炼成可读的画法规格。
func DescribeSVG(src string) string {
	var sb strings.Builder
	// 画布
	if m := svgViewBoxRe.FindStringSubmatch(src); m != nil {
		sb.WriteString("画布：viewBox=\"" + m[1] + "\"。\n")
	}
	// 部件（带 id 的组）
	ids := svgGroupIDRe.FindAllStringSubmatch(src, -1)
	if len(ids) > 0 {
		names := make([]string, 0, len(ids))
		for _, it := range ids {
			names = append(names, it[1])
		}
		sb.WriteString("部件（先画谁后画谁就按这个顺序）：" + strings.Join(names, " → ") + "\n")
	}
	// 元素计数
	sb.WriteString("形状数：")
	parts := []string{}
	for _, tag := range []string{"path", "ellipse", "circle", "rect", "polygon", "polyline", "line"} {
		if n := len(svgTagRe(tag).FindAllString(src, -1)); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 个", tag, n))
		}
	}
	sb.WriteString(strings.Join(parts, "、") + "\n")
	// 配色：按出现次数排序的前 8 个颜色
	sb.WriteString("配色（按用量排序，靠前的就是主色）：")
	colors := map[string]int{}
	for _, c := range svgHexColorRe.FindAllString(src, -1) {
		colors[strings.ToLower(c)]++
	}
	type cc struct {
		hex string
		n   int
	}
	var list []cc
	for k, v := range colors {
		list = append(list, cc{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	if len(list) > 8 {
		list = list[:8]
	}
	cs := make([]string, 0, len(list))
	for _, c := range list {
		cs = append(cs, c.hex)
	}
	sb.WriteString(strings.Join(cs, " ") + "\n")
	// 线宽
	widths := svgStrokeWidthRe.FindAllStringSubmatch(src, -1)
	if len(widths) > 0 {
		ws := []string{}
		for _, w := range widths {
			ws = append(ws, w[1])
		}
		sb.WriteString("线宽：" + strings.Join(ws, " / ") + "\n")
	}
	sb.WriteString("\n以上是**画法规格**，不是源码。不要照抄任何一行 SVG——按这套结构、配色阶和线宽自己重画。\n")
	return sb.String()
}

var (
	svgGroupIDRe     = regexp.MustCompile(`<g[^>]*id="([^"]+)"`)
	svgHexColorRe    = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
	svgStrokeWidthRe = regexp.MustCompile(`stroke-width="([\d.]+)"`)
)

func svgTagRe(tag string) *regexp.Regexp {
	return regexp.MustCompile(`<` + tag + `[\s/>]`)
}
