package qqbot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDigitsAndAgeTier(t *testing.T) {
	if DigitsOf(941628696) != 9 {
		t.Errorf("941628696 应为 9 位，得到 %d", DigitsOf(941628696))
	}
	cases := map[int]string{5: "老号（资深网龄）", 6: "老号（资深网龄）", 8: "中等号", 9: "较新号", 0: "未知"}
	for d, want := range cases {
		if got := AccountAgeTier(d); got != want {
			t.Errorf("AccountAgeTier(%d)=%q want %q", d, got, want)
		}
	}
	if AvatarURL(941628696) != "https://q1.qlogo.cn/g?b=qq&nk=941628696&s=640" {
		t.Errorf("头像 URL 不对: %s", AvatarURL(941628696))
	}
}

// 画像解析：把 OneBot 的字段（含驼峰/下划线两套）都吃进来。
func TestFetchProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/get_stranger_info") {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"retcode":0,"data":{
			"nickname":"怪我咯","remark":"群名片","sex":"male","age":30,
			"qqLevel":92,"qid":"","longNick":"活着就是为了整活",
			"regTime":1330000000,"province":"广东","city":"深圳","login_days":1234}}`)
	}))
	defer srv.Close()

	cfg := Default()
	cfg.OneBotAPI = srv.URL
	b := New(cfg)
	p, err := b.fetchProfile(941628696)
	if err != nil {
		t.Fatalf("fetchProfile: %v", err)
	}
	if p.Nick != "怪我咯" || p.QQLevel != 92 || p.Sign != "活着就是为了整活" || p.Age != 30 {
		t.Fatalf("解析不对: %+v", p)
	}
	if p.Digits != 9 || p.Avatar == "" || p.RegTime == 0 {
		t.Fatalf("派生字段不对: %+v", p)
	}
	line := p.Line()
	for _, want := range []string{"怪我咯", "9 位", "较新号", "QQ 等级 92", "签名「活着就是为了整活」", "男·30 岁", "深圳"} {
		if !strings.Contains(line, want) {
			t.Errorf("画像行缺 %q: %s", want, line)
		}
	}
}

// 画像入库后要出现在 prompt 注入里（否则只是存着没人用）。
func TestProfileInjectedIntoDescribe(t *testing.T) {
	m, _ := OpenMemory("", 3)
	m.Observe(941628696, "怪我咯", "你好")
	m.SetProfile(Profile{
		UID: 941628696, Nick: "怪我咯", Sex: "male", Age: 30,
		QQLevel: 92, Sign: "活着就是为了整活", City: "广东深圳", Digits: 9,
	})
	d := m.Describe(941628696)
	for _, want := range []string{"你对他的印象", "QQ 等级 92", "活着就是为了整活"} {
		if !strings.Contains(d, want) {
			t.Errorf("描述缺 %q: %s", want, d)
		}
	}
}

// 空画像不产生垃圾行。
func TestEmptyProfileLine(t *testing.T) {
	if got := (Profile{}).Line(); got != "" {
		t.Errorf("空画像应返回空串，得到 %q", got)
	}
}
