package qqbot

import (
	"fmt"
	"testing"
)

func TestShortHexName(t *testing.T) {
	cases := map[string]string{
		"火地晋": "晋", "天火同人": "同人", "火雷噬嗑": "噬嗑",
		"乾为天": "乾", "坤为地": "坤", "山地剥": "剥",
		"水火既济": "既济", "地火明夷": "明夷",
	}
	for full, want := range cases {
		if got := shortHexName(full); got != want {
			t.Errorf("shortHexName(%s) = %s, want %s", full, got, want)
		}
	}
	r := MeihuaByNumber(3, 8)
	fmt.Printf("本卦=%s 简写=%s 符号=%s\n", r.Name, r.ShortName(), r.Symbol())
}
