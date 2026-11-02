package qqbot

// 用**线上真实返回**（get_msg 一条聊天记录合集）验证展开是否拿到正文。
// 样本：testdata-quote.json（NapCat get_msg 的原始响应）。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestFlattenRealForwardSample(t *testing.T) {
	raw, err := os.ReadFile("testdata-quote.json")
	if err != nil {
		t.Skip("样本不存在")
	}
	var resp struct {
		Data struct {
			Message any `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("解析样本失败: %v", err)
	}
	got := flattenSegments(resp.Data.Message)
	t.Logf("展开结果(%d 字): %.300s", len([]rune(got)), got)
	if got == "" || got == "[聊天记录]" {
		t.Fatal("真实聊天记录样本没展开出正文")
	}
	// 样本里有这句真实文本
	if !strings.Contains(got, "武汉") && !strings.Contains(got, "[图片]") {
		t.Errorf("没展开出任何真实内容: %q", got)
	}
}
