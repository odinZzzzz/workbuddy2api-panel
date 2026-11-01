package qqbot

// 把鲸鱼娘的几个微调变体渲染到 ../../sticker-dump/（仅人工看效果）。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDumpTweaks(t *testing.T) {
	out := filepath.Join("..", "..", "sticker-dump")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	b := New(Default())
	src, err := b.TemplateSource("cat-portrait")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		tw   Tweak
	}{
		{"base", Tweak{SkipGray: true}},
		{"purple", Tweak{Hue: 40, Sat: 1.1, SkipGray: true}},
		{"pink", Tweak{Hue: 120, Sat: 1.15, SkipGray: true}},
		{"mint", Tweak{Hue: -70, Sat: 0.9, Light: 0.04, SkipGray: true}},
		{"dusk", Tweak{Hue: -20, Sat: 0.85, Light: -0.06, SkipGray: true}},
	}
	for _, c := range cases {
		s := src
		if !c.tw.Empty() {
			s = ApplyTweak(src, c.tw)
		}
		png, _, _, err := RenderSVGAuto(s, 512)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		path := filepath.Join(out, "tweak-"+c.name+".png")
		if err := os.WriteFile(path, png, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes) %s", path, len(png), DescribeTweak(c.tw))
	}
	_ = fmt.Sprint()
}
