package qqbot

// pet_test.go 元气鲸鱼娘立绘（复用官方美术资源，不让模型画）。

import (
	"encoding/json"
	"strings"
	"testing"
)

// 每个对外状态都必须真的能解出 PNG（embed 资源缺失会在这里红）。
func TestPetStatesRender(t *testing.T) {
	if len(petStates) < 20 {
		t.Fatalf("状态太少: %d", len(petStates))
	}
	for _, s := range petStates {
		png, err := petPNG(s.Canonical)
		if err != nil {
			t.Errorf("%s: %v", s.Canonical, err)
			continue
		}
		if len(png) < 2000 {
			t.Errorf("%s 图片过小: %d bytes", s.Canonical, len(png))
		}
		if ink, e := inkPixels(png); e != nil || ink < 10000 {
			t.Errorf("%s 内容不足 ink=%d err=%v", s.Canonical, ink, e)
		}
	}
}

// 中文/别名都要能归一化到 canonical。
func TestResolvePetState(t *testing.T) {
	cases := map[string]string{
		"生气": "angry", "angry": "angry", "哭": "meme-cry", "摸鱼": "work-slack",
		"害羞": "blush", "睡觉": "sleep", "比心": "meme-heart", "得意": "meme-smug",
	}
	for in, want := range cases {
		got, ok := resolvePetState(in)
		if !ok || got != want {
			t.Errorf("resolve(%s) = %s/%v，期望 %s", in, got, ok, want)
		}
	}
	if _, ok := resolvePetState("不存在的状态"); ok {
		t.Error("未知状态不该解析成功")
	}
}

// 工具层：send_pet 用中文状态也能走通（发送阶段失败可接受，不能是参数错误）。
func TestSendPetTool(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	args, _ := json.Marshal(map[string]string{"state": "生气"})
	out, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "send_pet", Arguments: string(args)},
	})
	if err != nil {
		if strings.Contains(err.Error(), "参数") || strings.Contains(err.Error(), "没有「") {
			t.Fatalf("中文状态应被接受: %v", err)
		}
		t.Logf("发送阶段失败（测试环境无 OneBot，符合预期）: %v", err)
		return
	}
	if !strings.Contains(out, "angry") {
		t.Errorf("回执应含 angry，实际 %s", out)
	}
}

// 未知状态要给可选清单，而不是静默发错图。
func TestSendPetUnknownState(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	_, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "send_pet", Arguments: `{"state":"外星人"}`},
	})
	if err == nil || !strings.Contains(err.Error(), "可选") {
		t.Errorf("未知状态应给可选清单，实际 %v", err)
	}
}
