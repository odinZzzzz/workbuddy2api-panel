package qqbot

import "testing"

// 验证：图片 URL 能正确转成网关的 image_url 内容数组。
func TestVisionPayload(t *testing.T) {
	msgs := []chatMsg{{Role: "user", Content: "这是什么表情 [图片×1]", ImageURLs: []string{"https://example.com/a.webp"}}}
	out := toPayloadMessages(msgs)
	if len(out) == 0 {
		t.Fatal("无消息")
	}
	parts, ok := out[0]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content 不是数组: %#v", out[0]["content"])
	}
	found := false
	for _, p := range parts {
		if p["type"] == "image_url" {
			found = true
		}
	}
	if !found {
		t.Errorf("未产出 image_url: %#v", parts)
	}
	t.Logf("parts=%d %#v", len(parts), parts)
}
