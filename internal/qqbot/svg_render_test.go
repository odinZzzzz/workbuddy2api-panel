package qqbot

// svg_render_test.go 渲染层两个真实故障的回归：
//   1. oksvg 不认 rgb()/rgba() 颜色函数 → 整份 SVG 报 "param mismatch"（鲸鱼娘实测）。
//   2. RenderSVG 强制方形，320×170 的横构图会被拉扁 → 必须按 viewBox 比例导出。

import (
	"strings"
	"testing"
)

func TestNormalizeSVGColors(t *testing.T) {
	cases := []struct{ in, want string }{
		{`fill="rgba(30,60,120,.1)"`, `fill="#1e3c78" opacity=".1"`},
		{`stroke="rgba(255,0,0,0.5)"`, `stroke="#ff0000" stroke-opacity="0.5"`},
		{`fill="rgb(255,0,0)"`, `fill="#ff0000"`},
		{`stop-color="rgba(0,0,0,0)"`, `stop-color="#000000" stop-opacity="0"`},
	}
	for _, c := range cases {
		if got := normalizeSVGColors(c.in); got != c.want {
			t.Errorf("normalize(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	// 不含 rgb 的文本应原样返回（零开销路径）
	plain := `<path d="M0 0 L10 10" fill="#8ecdf7"/>`
	if normalizeSVGColors(plain) != plain {
		t.Error("无 rgb 的 SVG 不应被改写")
	}
}

// rgba 颜色必须真的能渲染出来（之前是整份 SVG 解析失败）。
func TestRenderRGBAImage(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
		<ellipse cx="50" cy="50" rx="40" ry="20" fill="rgba(30,60,120,.4)"/>
	</svg>`
	png, w, h, err := RenderSVGAuto(src, 200)
	if err != nil {
		t.Fatalf("rgba 颜色应能渲染，实际报错: %v", err)
	}
	if len(png) == 0 {
		t.Fatal("渲染结果为空")
	}
	if w != 200 || h != 200 {
		t.Errorf("正方形画布应为 200x200，实际 %dx%d", w, h)
	}
}

// 横构图必须保持宽高比，不能被拉成正方形。
func TestAspectSize(t *testing.T) {
	svg := `<svg viewBox="0 0 320 170"></svg>`
	w, h := aspectSize(svg, 512)
	if w != 512 || h != 272 {
		t.Errorf("320x170 @512 → %dx%d，应为 512x272（宽高比不能丢）", w, h)
	}
	// 竖构图
	tall := `<svg viewBox="0 0 100 200"></svg>`
	w2, h2 := aspectSize(tall, 400)
	if w2 != 200 || h2 != 400 {
		t.Errorf("100x200 @400 → %dx%d，应为 200x400", w2, h2)
	}
	// 没有 viewBox 时回退方形
	w3, h3 := aspectSize("<svg/>", 256)
	if w3 != 256 || h3 != 256 {
		t.Errorf("无 viewBox → %dx%d，应回退 256x256", w3, h3)
	}
}

// 鲸鱼娘模板必须真的能渲染（回归：曾整份解析失败）。
func TestWhaleGirlRenders(t *testing.T) {
	b := New(Default())
	src, err := b.TemplateSource("whale-girl")
	if err != nil {
		t.Fatalf("取不到鲸鱼娘模板: %v", err)
	}
	png, w, h, err := RenderSVGAuto(src, 512)
	if err != nil {
		t.Fatalf("鲸鱼娘渲染失败: %v", err)
	}
	if w <= h {
		t.Errorf("鲸鱼娘是横构图，导出应为 512x272 类，实际 %dx%d", w, h)
	}
	if ink, e := inkPixels(png); e != nil || ink < 3000 {
		t.Errorf("鲸鱼娘应有充足内容（ink=%d, err=%v）", ink, e)
	}
	if strings.Contains(src, "rgba(") {
		t.Log("源文件含 rgba（渲染层已归一化，符合预期）")
	}
}
