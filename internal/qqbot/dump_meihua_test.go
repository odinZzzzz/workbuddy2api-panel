package qqbot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 渲染卦象样张到 ../../sticker-dump/（人工验收）。
func TestDumpMeihuaChart(t *testing.T) {
	out := filepath.Join("..", "..", "sticker-dump")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	font := "/usr/share/fonts/chinese/simhei.ttf"
	if _, err := os.Stat(font); err != nil {
		font = `C:\Windows\Fonts\simhei.ttf` // 本地验收用
	}
	if _, err := os.Stat(font); err != nil {
		font = ""
	}
	cases := []struct {
		name string
		r    MeihuaResult
	}{
		{"mh-1", MeihuaByNumber(3, 8)},
		{"mh-2", MeihuaByUserTime(941628696, time.Date(2026, 11, 2, 16, 0, 0, 0, time.Local))},
		{"mh-3", MeihuaByNumber(1, 1)},
	}
	for _, c := range cases {
		png, err := RenderMeihuaChart(c.r, font)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		path := filepath.Join(out, c.name+".png")
		if err := os.WriteFile(path, png, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s → %s (%d bytes) 本卦=%s 动爻=%d %s/%s",
			c.name, path, len(png), c.r.Name, c.r.Moving, c.r.Relation, c.r.Verdict)
	}
}
