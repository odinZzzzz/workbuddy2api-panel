package qqbot

// tweak.go 对现成 SVG 资源做"参数化微调"。
//
// 为什么需要：用户要"复用之前的资源，只做微调"——让模型重画必然走样（实测三次输出
// 与模板逐字节相同，或者干脆画崩）。正确做法是在**原画上改色**：结构、线条、构图
// 全部保留，只动色相/饱和度/明度，于是质量与参考图完全一致，观感却是新的。
//
// 实现是纯函数式的：解析所有 #rrggbb → HSL → 施加偏移 → 转回 hex。
// 灰色（饱和度极低，如描边/眼白/瞳孔）自动跳过，避免把线稿也染色。

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Tweak 微调参数（零值 = 原样）。
type Tweak struct {
	Hue       float64 // 色相偏移（度，-180~180）
	Sat       float64 // 饱和度缩放（1 = 不变，0.5 = 更淡，1.5 = 更艳）
	Light     float64 // 明度偏移（-1~1，正数更亮）
	SkipGray  bool    // 是否跳过低饱和色（默认 true）
	grayThres float64 // 内部：饱和度低于此值视为灰
}

// Empty 判断是否没有任何调整（此时应原样返回，零开销）。
func (t Tweak) Empty() bool {
	return t.Hue == 0 && t.Sat == 1 && t.Light == 0 && !t.SkipGray
}

var svgHexColorOnlyRe = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)

// ApplyTweak 对 SVG 里所有 6 位 hex 颜色做微调。
func ApplyTweak(src string, t Tweak) string {
	if t.Empty() {
		return src
	}
	sat := t.Sat
	if sat <= 0 {
		sat = 1
	}
	grayThres := t.grayThres
	if grayThres <= 0 {
		grayThres = 0.12
	}
	return svgHexColorOnlyRe.ReplaceAllStringFunc(src, func(hex string) string {
		r, g, b, ok := parseHex(hex)
		if !ok {
			return hex
		}
		h, s, l := rgbToHSL(r, g, b)
		// 灰色（线稿/眼白/黑瞳）不动：染了会让画面变脏
		if t.SkipGray && s < grayThres {
			return hex
		}
		h = math.Mod(h+t.Hue+360, 360)
		s = clamp01(s * sat)
		l = clamp01(l + t.Light)
		nr, ng, nb := hslToRGB(h, s, l)
		return fmt.Sprintf("#%02x%02x%02x", nr, ng, nb)
	})
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func parseHex(hex string) (float64, float64, float64, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255, true
}

// rgbToHSL 标准转换（r,g,b ∈ [0,1]，返回 h∈[0,360), s,l∈[0,1]）。
func rgbToHSL(r, g, b float64) (float64, float64, float64) {
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l := (max + min) / 2
	if max == min {
		return 0, 0, l
	}
	d := max - min
	s := d / (1 - math.Abs(2*l-1))
	var h float64
	switch max {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, l
}

// hslToRGB 反变换，返回 0~255 的整数分量。
func hslToRGB(h, s, l float64) (int, int, int) {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return int(math.Round((r + m) * 255)), int(math.Round((g + m) * 255)), int(math.Round((b + m) * 255))
}

// DescribeTweak 把参数渲染成一句人话（日志/回复用）。
func DescribeTweak(t Tweak) string {
	var parts []string
	if t.Hue != 0 {
		parts = append(parts, fmt.Sprintf("色相%+.0f°", t.Hue))
	}
	if t.Sat != 1 && t.Sat > 0 {
		parts = append(parts, fmt.Sprintf("饱和×%.2f", t.Sat))
	}
	if t.Light != 0 {
		parts = append(parts, fmt.Sprintf("明度%+.2f", t.Light))
	}
	if len(parts) == 0 {
		return "无调整"
	}
	return strings.Join(parts, "，")
}
