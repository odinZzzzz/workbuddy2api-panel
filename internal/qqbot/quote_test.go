package qqbot

// quote_test.go 引用回复 / 合并转发的解析与回填。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseQuoteIDs(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"reply","data":{"id":"123456"}},
		{"type":"text","data":{"text":"你觉得呢"}}
	]`)
	rid, fid := parseQuoteIDs(raw)
	if rid != 123456 {
		t.Errorf("reply id = %d, want 123456", rid)
	}
	if fid != "" {
		t.Errorf("forward id 应为空，实际 %q", fid)
	}

	raw2 := json.RawMessage(`[{"type":"forward","data":{"id":"abc-999"}}]`)
	rid2, fid2 := parseQuoteIDs(raw2)
	if rid2 != 0 || fid2 != "abc-999" {
		t.Errorf("forward: reply=%d forward=%q, want 0/abc-999", rid2, fid2)
	}

	// 非数组（string 格式上报）不应 panic
	if rid3, fid3 := parseQuoteIDs(json.RawMessage(`"hi"`)); rid3 != 0 || fid3 != "" {
		t.Errorf("string 格式应无引用: %d %q", rid3, fid3)
	}
}

func TestFlattenSegments(t *testing.T) {
	// 数组段：文本 + 图片 + @
	got := flattenSegments([]any{
		map[string]any{"type": "text", "data": map[string]any{"text": "看这个"}},
		map[string]any{"type": "image", "data": map[string]any{"file": "x.jpg"}},
		map[string]any{"type": "at", "data": map[string]any{"qq": "123"}},
	})
	if !strings.Contains(got, "看这个") || !strings.Contains(got, "[图片]") || !strings.Contains(got, "@某人") {
		t.Errorf("展开不完整: %q", got)
	}
	// 纯字符串直传
	if flattenSegments("纯文本") != "纯文本" {
		t.Errorf("字符串应直传")
	}
	// 引用不递归（防套娃）
	got2 := flattenSegments([]any{
		map[string]any{"type": "reply", "data": map[string]any{"id": "9"}},
		map[string]any{"type": "text", "data": map[string]any{"text": "正文"}},
	})
	if strings.Contains(got2, "9") {
		t.Errorf("reply 段不应展开: %q", got2)
	}
}

func TestParseIntAny(t *testing.T) {
	if n := parseIntAny("", "42", "x"); n != 42 {
		t.Errorf("parseIntAny = %d, want 42", n)
	}
	if n := parseIntAny("abc"); n != 0 {
		t.Errorf("非数字应为 0，实际 %d", n)
	}
}

// 被引用的消息如果是合并转发，必须能取出 forward id 去展开正文。
func TestForwardIDOf(t *testing.T) {
	body := []any{
		map[string]any{"type": "forward", "data": map[string]any{"id": "7703400622907597409"}},
	}
	if got := forwardIDOf(body); got != "7703400622907597409" {
		t.Errorf("forwardIDOf = %q", got)
	}
	// 普通文本消息没有 forward
	if got := forwardIDOf([]any{
		map[string]any{"type": "text", "data": map[string]any{"text": "hi"}},
	}); got != "" {
		t.Errorf("普通消息应为空，实际 %q", got)
	}
	// 非数组不 panic
	if got := forwardIDOf("str"); got != "" {
		t.Errorf("非数组应为空，实际 %q", got)
	}
}

// 合并转发的正文嵌在 forward.data.content 里（实测 NapCat 结构），必须展开。
func TestFlattenNestedForward(t *testing.T) {
	inner := []any{
		map[string]any{"type": "text", "data": map[string]any{"text": "第一句"}},
		map[string]any{"type": "image", "data": map[string]any{"file": "a.jpg"}},
	}
	outer := []any{
		map[string]any{"type": "forward", "data": map[string]any{"id": "1", "content": inner}},
	}
	got := flattenSegments(outer)
	if !strings.Contains(got, "第一句") || !strings.Contains(got, "[图片]") {
		t.Fatalf("嵌套转发未展开: %q", got)
	}
	if strings.Contains(got, "[聊天记录]") {
		t.Errorf("有内容时不该只剩占位符: %q", got)
	}
	// 没有 content（只有 id）时回落到占位符
	noContent := []any{map[string]any{"type": "forward", "data": map[string]any{"id": "9"}}}
	if g := flattenSegments(noContent); !strings.Contains(g, "[聊天记录]") {
		t.Errorf("无 content 应回落到占位符: %q", g)
	}
}
