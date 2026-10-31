package qqbot

import (
	"strings"
	"testing"
)

// 知识库命中：问密码/地图/工具时应命中对应事实卡。
func TestMatchKB(t *testing.T) {
	cases := map[string]string{
		"今天密码是多少":  "每日地图密码",
		"钥匙房值得开吗":  "钥匙房",
		"卡战备怎么配":   "卡战备",
		"特勤处制造赚什么": "特勤处",
		"烽火地带怎么玩":  "烽火地带",
		"在哪个网站查点位": "kkrb.net",
	}
	for msg, want := range cases {
		hits := matchKB(msg, 2)
		if len(hits) == 0 {
			t.Errorf("%q 应命中知识库", msg)
			continue
		}
		joined := ""
		for _, h := range hits {
			joined += h.Text
		}
		if !strings.Contains(joined, want) {
			t.Errorf("%q 命中内容应包含 %q，实际: %.80s", msg, want, joined)
		}
	}
	// 无关消息不注入
	if hits := matchKB("今天天气不错", 2); len(hits) != 0 {
		t.Errorf("无关消息不该命中，实际 %d 条", len(hits))
	}
	b := New(Default())
	if b.kbContext("今天天气不错") != "" {
		t.Error("无命中时 kbContext 应为空")
	}
}

// 命中时 kbContext 要带上"参考资料"抬头，避免模型把它当用户发言。
func TestKBContextHeader(t *testing.T) {
	got := New(Default()).kbContext("今天密码是多少")
	if !strings.HasPrefix(got, "【参考资料") {
		t.Errorf("应有参考资料抬头: %.60s", got)
	}
}

// 时间格式化：接口给 20261031000001，要变成 2026-10-31。
func TestFormatKkrbUpdated(t *testing.T) {
	if got := formatKkrbUpdated("20261031000001"); got != "2026-10-31" {
		t.Errorf("got %q", got)
	}
	if got := formatKkrbUpdated("bad"); got != "" {
		t.Errorf("非法输入应返回空: %q", got)
	}
}
