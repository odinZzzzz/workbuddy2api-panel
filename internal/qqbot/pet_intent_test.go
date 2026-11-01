package qqbot

// pet_intent_test.go "要鲸鱼娘的图"必须走确定性直连（发官方立绘），不进模型生成。

import "testing"

func TestWhaleArtIntent(t *testing.T) {
	cases := []struct {
		text string
		want string // 空 = 不该命中
	}{
		{"画个生气的鲸鱼娘", "angry"},
		{"来一张鲸鱼娘的图", "greet"},
		{"鲸鱼娘睡觉", "sleep"},
		{"给我看看摸鱼的鲸鱼娘", "work-slack"},
		{"画个害羞的鲸鱼娘", "blush"},
		{"鲸鱼娘比心", "meme-heart"},
		{"鲸鱼娘", ""},     // 只有主体没有"要图"动词 → 交给正常对话
		{"鲸鱼好吃吗", ""},   // 闲聊，不是要图
		{"今天天气怎么样", ""}, // 无关
		{"/help", ""},   // 指令
	}
	for _, c := range cases {
		got, ok := WhaleArtIntent(c.text)
		if c.want == "" {
			if ok {
				t.Errorf("%q 不该命中直连，实际 state=%s", c.text, got)
			}
			continue
		}
		if !ok {
			t.Errorf("%q 应命中直连", c.text)
			continue
		}
		if got != c.want {
			t.Errorf("%q → %s，期望 %s", c.text, got, c.want)
		}
	}
}
