package qqbot

import (
	"strings"
	"testing"
)

// 每个内置模板都必须真的渲染出内容（防"模板里混进不支持的元素导致空白"）。
func TestBuiltinTemplatesRender(t *testing.T) {
	b := New(Default())
	list := b.ListTemplates()
	if len(list) < 4 {
		t.Fatalf("内置模板应有 4 个以上，实际 %d", len(list))
	}
	for _, tp := range list {
		if !strings.Contains(strings.Join([]string{"wave", "starry", "scream", "pearl"}, ","), tp.Name) {
			continue // 跳过外部目录里的自定义模板
		}
		src, err := b.TemplateSource(tp.Name)
		if err != nil {
			t.Fatalf("%s: 取模板失败: %v", tp.Name, err)
		}
		if !strings.Contains(src, "<svg") {
			t.Fatalf("%s: 不是 SVG", tp.Name)
		}
		// 模板里不允许出现 oksvg 画不出来的元素
		for _, banned := range []string{"<text", "<g transform", "clipPath", "<filter", "<style"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s: 模板含不支持的元素 %q（会静默不渲染）", tp.Name, banned)
			}
		}
		png, err := RenderSVG(src, 256)
		if err != nil {
			t.Fatalf("%s: 渲染失败: %v", tp.Name, err)
		}
		ink, err := inkPixels(png)
		if err != nil {
			t.Fatalf("%s: 数像素失败: %v", tp.Name, err)
		}
		if ink < 5000 {
			t.Errorf("%s: 渲染内容太少（ink=%d），模板可能有问题", tp.Name, ink)
		}
		t.Logf("%-8s ink=%d bytes=%d", tp.Name, ink, len(png))
	}
}

// 模板名非法（路径穿越）必须被拒。
func TestTemplateNameGuard(t *testing.T) {
	b := New(Default())
	for _, bad := range []string{"", "../etc/passwd", "a/b", "..\\x"} {
		if _, err := b.TemplateSource(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}
