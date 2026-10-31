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

// 群友投稿改枪码：只能给已收录的枪加、码要校验、重复不加、写盘后能被读回。
func TestAddGunsmithCode(t *testing.T) {
	dir := t.TempDir()
	card := "keys: M700, M700改枪\n---\n改枪码（烽火地带）：\n\n- 短管红点版：6LKJIG80EU90O684D8QL5\n\n视频链接：https://x\n"
	if err := os.WriteFile(filepath.Join(dir, "改枪码-M700.md"), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.KBDir = dir
	c.MemoryFile = ""
	b := New(c)

	// 正常加一条
	msg, err := b.AddGunsmithCode("M700", "6LNEWCODE1234567890ABC", "长枪管版", "怪我咯")
	if err != nil {
		t.Fatalf("加码失败: %v", err)
	}
	if !strings.Contains(msg, "已给 M700") {
		t.Errorf("返回文案不对: %s", msg)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "改枪码-M700.md"))
	if !strings.Contains(string(raw), "6LNEWCODE1234567890ABC") || !strings.Contains(string(raw), "投稿") {
		t.Errorf("卡片没写进去或缺投稿标记:\n%s", raw)
	}
	// 码要插在「视频链接」之前，保持结构
	if strings.Index(string(raw), "6LNEWCODE1234567890ABC") > strings.Index(string(raw), "视频链接") {
		t.Error("新码应插在视频链接之前")
	}

	// 重复的码不再加
	msg2, err := b.AddGunsmithCode("M700", "6LNEWCODE1234567890ABC", "", "")
	if err != nil {
		t.Fatalf("重复加报错: %v", err)
	}
	if !strings.Contains(msg2, "已经在库里") {
		t.Errorf("重复码应提示已存在: %s", msg2)
	}

	// 没收录的枪要拒绝
	if _, err := b.AddGunsmithCode("K437", "6LNEWCODE1234567890ABC", "", ""); err == nil {
		t.Error("未收录的枪应拒绝")
	}
	// 码格式不对要拒绝
	// 小写码会被自动转大写接受（用户粘小写很常见），不算非法
	if _, err := b.AddGunsmithCode("M700", "6llowercase1234567890ab", "小写投稿", ""); err != nil {
		t.Errorf("小写码应自动转大写接受: %v", err)
	}
	for _, bad := range []string{"abc", "123", "6L", "6L NEW CODE 1234"} {
		if _, err := b.AddGunsmithCode("M700", bad, "", ""); err == nil {
			t.Errorf("非法码 %q 应拒绝", bad)
		}
	}
	// 纯数字要拒绝（那是 ID 不是码）
	if _, err := b.AddGunsmithCode("M700", "1234567890123456", "", ""); err == nil {
		t.Error("纯数字应拒绝")
	}
}
