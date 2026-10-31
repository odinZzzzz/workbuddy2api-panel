package qqbot

import (
	"bytes"
	"image/png"
	"testing"
)

// 柱状图：必须产出合法 PNG；字体缺失时降级（不报错、仍出图）。
func TestRenderBarChart(t *testing.T) {
	items := []ChartItem{
		{Label: "技术中心·消音器", Value: 37034},
		{Label: "防具台·重甲", Value: 230056},
		{Label: "工作台·子弹", Value: 320896},
	}
	pngBytes, err := RenderBarChart("特勤处制造利润", items, "/usr/share/fonts/chinese/simhei.ttf")
	if err != nil {
		t.Fatalf("RenderBarChart: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("解码 PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 600 || b.Dy() < 200 {
		t.Fatalf("尺寸异常: %v", b)
	}
	// 字体不存在时也应出图（只少了文字）
	if _, err := RenderBarChart("x", items, "/nonexistent.ttf"); err != nil {
		t.Fatalf("字体缺失时应降级出图，得到错误: %v", err)
	}
}

// 空数据要报错而不是画一张空图。
func TestRenderBarChartEmpty(t *testing.T) {
	if _, err := RenderBarChart("空", nil, ""); err == nil {
		t.Fatal("空数据应报错")
	}
}

// 数值格式化：千分位 + 小数。
func TestFormatChartValue(t *testing.T) {
	cases := map[float64]string{
		37034:   "37,034",
		1234567: "1,234,567",
		12:      "12",
		1.5:     "1.5",
	}
	for in, want := range cases {
		if got := formatChartValue(in); got != want {
			t.Errorf("formatChartValue(%v)=%q want %q", in, got, want)
		}
	}
}
