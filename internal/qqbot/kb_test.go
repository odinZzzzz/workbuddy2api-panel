package qqbot

import (
	"os"
	"path/filepath"
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
	if !strings.HasPrefix(got, "【权威资料") {
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

// 改枪码直出：枪名必须词边界匹配（M7 不能命中 M700），没提枪名时给汇总卡。
func TestGunsmithCardMatch(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("改枪码-M7.md", "keys: M7, M7改枪\n---\n改枪码：M7-CODE-AAAA\n")
	write("改枪码-M700.md", "keys: M700, M700改枪\n---\n改枪码：M700-CODE-BBBB\n")
	write("改枪码-汇总.md", "keys: 改枪码, 改枪\n---\n目前收录的枪型：M7、M700\n")

	c := Default()
	c.KBDir = dir
	c.MemoryFile = ""
	b := New(c)

	// M7 必须拿到 M7 的卡，不能是 M700
	if got, ok := b.gunsmithCard("M7 改枪码"); !ok || !strings.Contains(got, "M7-CODE-AAAA") {
		t.Errorf("M7 应命中 M7 卡，实际 ok=%v got=%.60s", ok, got)
	}
	// M700 拿 M700
	if got, ok := b.gunsmithCard("M700 改枪码"); !ok || !strings.Contains(got, "M700-CODE-BBBB") {
		t.Errorf("M700 应命中 M700 卡，实际 ok=%v got=%.60s", ok, got)
	}
	// 不传枪名 → 汇总卡
	if got, ok := b.gunsmithCard(""); !ok || !strings.Contains(got, "目前收录") {
		t.Errorf("泛问应给汇总，实际 ok=%v got=%.60s", ok, got)
	}
	// 未收录的枪 → 不返回卡片
	if _, ok := b.gunsmithCard("K416"); ok {
		t.Error("未收录的枪不该返回卡片")
	}
}
