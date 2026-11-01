package qqbot

// whalegirl_test.go 同一份美术资源切状态的回归。

import (
	"strings"
	"testing"
)

func TestWhaleGirlStatesRenderUnique(t *testing.T) {
	b := New(Default())
	src, err := b.TemplateSource("whale-girl")
	if err != nil {
		t.Fatal(err)
	}
	if !hasStateGroups(src) {
		t.Fatal("模板里应保留全部状态组（闭眼/笔记本/Zzz/眼泪…），否则没法切状态")
	}
	seen := map[string]bool{}
	for _, st := range []string{"idle", "sleep", "work", "cry", "happy", "angry", "sing", "spout", "shy"} {
		s, name := WhaleGirlState(src, st)
		if name != st {
			t.Errorf("状态名回环错误: 请求 %s 得到 %s", st, name)
		}
		png, _, _, err := RenderSVGAuto(s, 512)
		if err != nil {
			t.Fatalf("%s 渲染失败: %v", st, err)
		}
		if ink, e := inkPixels(png); e != nil || ink < 1000 {
			t.Errorf("%s 内容不足: ink=%d err=%v", st, ink, e)
		}
		// 状态元素应该真的被裁掉：sleep 不应还有睁眼组
		if st == "sleep" && containsID(s, "wlgEyes\"") && containsID(s, `id="wlgEyes"`) {
			t.Error("sleep 状态下睁眼组没被裁掉")
		}
		seen[st] = true
	}
	if len(seen) != 9 {
		t.Errorf("应覆盖 9 个状态，实际 %d", len(seen))
	}
}

func TestWhaleGirlUnknownStateFallsBackToIdle(t *testing.T) {
	b := New(Default())
	src, _ := b.TemplateSource("whale-girl")
	_, name := WhaleGirlState(src, "不存在的状态")
	if name != "idle" {
		t.Errorf("未知状态应回落 idle，实际 %s", name)
	}
}

func hasStateGroups(src string) bool {
	for _, id := range []string{`id="wlgEyesClosed"`, `id="wlgLaptop"`, `id="wlgZzz"`, `id="wlgTear"`, `id="wlgSpout"`} {
		if !containsID(src, id) {
			return false
		}
	}
	return true
}

func containsID(src, needle string) bool {
	return len(src) > 0 && strings.Contains(src, needle)
}
