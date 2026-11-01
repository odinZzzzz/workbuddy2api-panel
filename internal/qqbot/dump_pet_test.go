package qqbot

// 渲染立绘改色/叠加样张到 ../../sticker-dump/（仅人工看效果）。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDumpPetVariants(t *testing.T) {
	out := filepath.Join("..", "..", "sticker-dump")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	font := "/usr/share/fonts/chinese/simhei.ttf"
	if _, err := os.Stat(font); err != nil {
		font = "" // Windows 上跑测试时没有该字体，字幕会跳过
	}
	base, err := petPNG("angry")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		tw   Tweak
		deco Deco
	}{
		{"orig", Tweak{}, Deco{}},
		{"purple", Tweak{Hue: 40, Sat: 1.1, SkipGray: true}, Deco{}},
		{"deco", Tweak{}, Deco{Blush: true, Frame: true, Heart: true, Caption: "气死本王了"}},
		{"combo", Tweak{Hue: 40, Sat: 1.1, SkipGray: true}, Deco{Blush: true, Frame: true, Star: true, Caption: "气死本王了"}},
	}
	for _, c := range cases {
		b := base
		if !c.tw.Empty() {
			if b, err = RecolorPNG(b, c.tw.Hue, c.tw.Sat, c.tw.Light); err != nil {
				t.Fatalf("%s recolor: %v", c.name, err)
			}
		}
		if !c.deco.Empty() {
			if b, err = ApplyDeco(b, c.deco, font); err != nil {
				t.Fatalf("%s deco: %v", c.name, err)
			}
		}
		path := filepath.Join(out, "pet-"+c.name+".png")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes) %s %s", path, len(b), DescribeTweak(c.tw), c.deco.Describe())
	}
}
