package qqbot

// 渲染鲸鱼娘全部状态到 ../../sticker-dump/state-*.png（仅人工看效果）。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDumpWhaleStates(t *testing.T) {
	out := filepath.Join("..", "..", "sticker-dump")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	b := New(Default())
	src, err := b.TemplateSource("whale-girl")
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"idle", "sleep", "work", "cry", "happy", "angry", "sing", "spout", "shy"} {
		s, name := WhaleGirlState(src, st)
		png, w, h, err := RenderSVGAuto(s, 512)
		if err != nil {
			t.Errorf("%s: %v", st, err)
			continue
		}
		path := filepath.Join(out, "state-"+name+".png")
		if err := os.WriteFile(path, png, 0o644); err != nil {
			t.Fatal(err)
		}
		ink, _ := inkPixels(png)
		t.Logf("%-6s → %dx%d ink=%d %d bytes", name, w, h, ink, len(png))
	}
}
