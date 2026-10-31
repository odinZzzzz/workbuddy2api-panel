package qqbot

import (
	"strings"
	"testing"
)

// QQ 表情解析：中文名、数字 id、带后缀的写法都要认，瞎写的要给可用名单。
func TestResolveFace(t *testing.T) {
	cases := map[string]string{
		"白眼":   "22",
		"发怒":   "11",
		"调皮":   "12",
		"微笑":   "14",
		"抠鼻":   "41",
		"白眼表情": "22",
		"12":   "12",
		"0":    "0",
	}
	for in, want := range cases {
		got, err := resolveFace(in)
		if err != nil {
			t.Errorf("resolveFace(%q) 报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("resolveFace(%q)=%q want %q", in, got, want)
		}
	}
	if _, err := resolveFace("胡说八道"); err == nil {
		t.Error("未知表情名应报错")
	} else if !strings.Contains(err.Error(), "可用") {
		t.Errorf("报错里应带可用名单: %v", err)
	}
	if _, err := resolveFace("  "); err == nil {
		t.Error("空名字应报错")
	}
	// 名单不能是空的，否则模型没法挑
	if faceNames() == "" {
		t.Error("faceNames 为空")
	}
}
