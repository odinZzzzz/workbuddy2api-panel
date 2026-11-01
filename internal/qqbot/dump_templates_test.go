package qqbot

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDumpTemplates 把内置模板渲染成 PNG 落到 ../../sticker-dump/（仅用于人工看效果）。
func TestDumpTemplates(t *testing.T) {
	out := filepath.Join("..", "..", "sticker-dump")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	b := New(Default())
	for _, tp := range b.ListTemplates() {
		src, err := b.TemplateSource(tp.Name)
		if err != nil {
			continue
		}
		png, _, _, err := RenderSVGAuto(src, 512)
		if err != nil {
			t.Logf("%s: %v", tp.Name, err)
			continue
		}
		path := filepath.Join(out, tp.Name+".png")
		if err := os.WriteFile(path, png, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(png))
	}
}
