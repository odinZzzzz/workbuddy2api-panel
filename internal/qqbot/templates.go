package qqbot

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
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
