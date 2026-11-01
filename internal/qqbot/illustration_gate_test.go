package qqbot

// illustration_gate_test.go 插图质量闸门与"只给规格不给源码"的回归。
//
// 用户原话："至少要做到 能画出来 跟原来一样的资源"——
// 之前模型要么原样照抄模板（sha 与模板一致），要么只画一个大圆加两个点。
// 两个修法：模板只给画法规格（没有可抄的源码）；形状数不足的插图直接打回让补齐。

import (
	"encoding/json"
	"strings"
	"testing"
)

// 规格里不该出现可照抄的 SVG 源码。
func TestDescribeSVGIsSpecNotSource(t *testing.T) {
	b := New(Default())
	src, err := b.TemplateSource("cat-portrait")
	if err != nil {
		t.Fatal(err)
	}
	spec := DescribeSVG(src)
	if strings.Contains(spec, "<svg") || strings.Contains(spec, "<path d=") {
		t.Error("规格里混进了可照抄的 SVG 源码")
	}
	if !strings.Contains(spec, "配色") {
		t.Error("规格应包含配色阶")
	}
	if !strings.Contains(spec, "形状数") {
		t.Error("规格应包含形状数")
	}
	if !strings.Contains(spec, "不要照抄") {
		t.Error("规格应明确要求不要照抄")
	}
	// 应给出可执行的量化信息
	if !strings.Contains(spec, "形状数") {
		t.Errorf("规格应含形状数: %s", spec[:120])
	}
}

// 只有几个形状的"插图"必须被打回，而不是发给用户。
func TestDrawStickerRejectsSimplisticIllustration(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	simple := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">
		<rect width="256" height="256" fill="#eee"/>
		<circle cx="128" cy="128" r="60" fill="#f90"/>
		<circle cx="100" cy="120" r="8"/>
		<circle cx="156" cy="120" r="8"/>
	</svg>`
	args, _ := json.Marshal(map[string]string{"svg": simple})
	_, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "draw_sticker", Arguments: string(args)},
	})
	if err == nil {
		t.Fatal("只有 4 个形状的插图应被打回")
	}
	if !strings.Contains(err.Error(), "太简陋") {
		t.Errorf("打回原因应指出太简陋，实际: %v", err)
	}
}

// 贴纸模式不受形状数闸门限制（表情包就是要少形状）。
func TestDrawStickerStickerModeSkipsGate(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	simple := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">
		<rect width="256" height="256" fill="#eee"/>
		<circle cx="128" cy="128" r="60" fill="#f90"/>
		<circle cx="100" cy="120" r="8"/>
		<circle cx="156" cy="120" r="8"/>
	</svg>`
	args, _ := json.Marshal(map[string]string{"svg": simple, "mode": "sticker"})
	// 可能因发图失败报错（测试环境无 OneBot），但**不能**是因为"太简陋"。
	_, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "draw_sticker", Arguments: string(args)},
	})
	if err != nil && strings.Contains(err.Error(), "太简陋") {
		t.Errorf("贴纸模式不该被形状数闸门拦下: %v", err)
	}
}
