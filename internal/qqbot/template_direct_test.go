package qqbot

// template_direct_test.go 「画鲸鱼娘」必须直接出那张资源，而不是让模型重新猜。

import (
	"encoding/json"
	"strings"
	"testing"
)

// template=cat-portrait 应直接渲染并尝试发图（测试环境无 OneBot 会发送失败，
// 但绝不能因为"参数缺失"或"太简陋"被挡）。
func TestDrawStickerTemplateDirect(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	args, _ := json.Marshal(map[string]string{"template": "cat-portrait"})
	out, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "draw_sticker", Arguments: string(args)},
	})
	if err != nil {
		if strings.Contains(err.Error(), "参数") || strings.Contains(err.Error(), "太简陋") ||
			strings.Contains(err.Error(), "不存在") {
			t.Fatalf("模板直出被错误拦截: %v", err)
		}
		t.Logf("渲染完成，发送阶段失败（测试环境无 OneBot，符合预期）: %v", err)
		return
	}
	if !strings.Contains(out, "cat-portrait") {
		t.Errorf("应报告发出了 cat-portrait，实际 %s", out)
	}
}

// 不传 template 也不传 svg：应给出明确指引，而不是 panic。
func TestDrawStickerRequiresOneInput(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	_, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "draw_sticker", Arguments: `{}`},
	})
	if err == nil || !strings.Contains(err.Error(), "要么传 template") {
		t.Errorf("两个参数都缺时应给出明确指引，实际 %v", err)
	}
}

// 不存在的模板名：报错并提示先列模板。
func TestDrawStickerUnknownTemplate(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	args, _ := json.Marshal(map[string]string{"template": "no-such-thing"})
	_, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "draw_sticker", Arguments: string(args)},
	})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Errorf("未知模板应报不存在，实际 %v", err)
	}
}
