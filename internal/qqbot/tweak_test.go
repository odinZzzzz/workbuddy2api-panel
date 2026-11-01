package qqbot

// tweak_test.go "复用资源只做微调"的回归：改色不改形。

import (
	"strings"
	"testing"
)

func TestApplyTweakChangesColorsNotStructure(t *testing.T) {
	b := New(Default())
	src, err := b.TemplateSource("cat-portrait")
	if err != nil {
		t.Fatal(err)
	}
	tweaked := ApplyTweak(src, Tweak{Hue: 40, Sat: 1.1, SkipGray: true})
	if tweaked == src {
		t.Fatal("微调后颜色应发生变化")
	}
	// 结构不变：元素数、部件、路径逐个一致
	if countSVGShapes(tweaked) != countSVGShapes(src) {
		t.Errorf("形状数变了: %d → %d", countSVGShapes(src), countSVGShapes(tweaked))
	}
	if got, want := len(svgGroupIDRe.FindAllString(tweaked, -1)), len(svgGroupIDRe.FindAllString(src, -1)); got != want {
		t.Errorf("部件数变了: %d → %d", want, got)
	}
	if strings.Count(tweaked, "d=\"") != strings.Count(src, "d=\"") {
		t.Error("路径数量变了（不该动结构）")
	}
	// 纯白/纯灰（眼白、高光）饱和度 0，必须原样保留——染了会让眼睛发脏。
	// 注意鲸鱼娘的描边是蓝色 #5f9ad0（饱和度高），被一起染色是**预期**的：
	// 换色调时线稿跟着变才协调。
	if strings.Count(tweaked, "#ffffff") != strings.Count(src, "#ffffff") {
		t.Errorf("纯白被误染: %d → %d", strings.Count(src, "#ffffff"), strings.Count(tweaked, "#ffffff"))
	}
}

func TestApplyTweakEmptyIsNoop(t *testing.T) {
	src := `<rect fill="#8ecdf7" stroke="#1b2330"/>`
	if got := ApplyTweak(src, Tweak{}); got != src {
		t.Errorf("零参数应原样返回: %s", got)
	}
}
