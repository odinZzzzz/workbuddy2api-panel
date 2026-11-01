package qqbot

// pet_deco.go 官方立绘的**程序化叠加**：腮红 / 贴纸 / 文字 / 边框。
//
// 定位：语义级重绘（换姿势、换衣服）需要 img2img 模型，本项目不做；
// 但"加装饰"完全可以用纯 Go 画——原画一个像素都不改，只在上面叠元素。
// 复用 chart.go 的字体缓存与绘制工具（simhei.ttf），零新依赖。

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
)

// Deco 叠加参数（零值 = 不加任何装饰）。
type Deco struct {
	Blush   bool   // 腮红（两团半透明粉）
	Frame   bool   // 圆角描边
	Heart   bool   // 右上角爱心
	Star    bool   // 右上角星星
	Caption string // 底部字幕
	Font    string // 字体路径（空 = 不画字）
}

// Empty 判断是否无叠加。
func (d Deco) Empty() bool {
	return !d.Blush && !d.Frame && !d.Heart && !d.Star && strings.TrimSpace(d.Caption) == ""
}

// Describe 把叠加项渲染成一句话（回执用）。
func (d Deco) Describe() string {
	var parts []string
	if d.Blush {
		parts = append(parts, "加腮红")
	}
	if d.Frame {
		parts = append(parts, "加边框")
	}
	if d.Heart {
		parts = append(parts, "加爱心")
	}
	if d.Star {
		parts = append(parts, "加星星")
	}
	if cap := strings.TrimSpace(d.Caption); cap != "" {
		parts = append(parts, "加字幕「"+cap+"」")
	}
	return strings.Join(parts, "、")
}

// ApplyDeco 在 PNG 上叠加装饰，返回新 PNG。
func ApplyDeco(pngBytes []byte, d Deco, fontPath string) ([]byte, error) {
	if d.Empty() {
		return pngBytes, nil
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()

	if d.Blush {
		drawBlush(rgba, w, h)
	}
	if d.Frame {
		drawRoundFrame(rgba, w, h)
	}
	if d.Heart {
		drawHeart(rgba, w, h)
	}
	if d.Star {
		drawStar(rgba, w, h)
	}
	if cap := strings.TrimSpace(d.Caption); cap != "" {
		fp := fontPath
		if fp == "" {
			fp = d.Font
		}
		drawCaption(rgba, w, h, cap, fp)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawBlush 两团腮红：位置按立绘常见比例（脸在画面中上部），用多层椭圆做出柔和边。
func drawBlush(dst *image.RGBA, w, h int) {
	cx := []float64{0.365, 0.635}
	cy := 0.545
	for _, fx := range cx {
		px := int(fx * float64(w))
		py := int(cy * float64(h))
		for layer := 0; layer < 3; layer++ {
			rx := int(float64(w) * (0.045 - float64(layer)*0.010))
			ry := int(float64(h) * (0.026 - float64(layer)*0.006))
			alpha := uint8(34 + layer*16)
			fillEllipseAlpha(dst, px, py, rx, ry, color.NRGBA{255, 140, 150, alpha})
		}
	}
}

// drawRoundFrame 圆角描边：四段直线 + 四个 1/4 圆（避免整块矩形盖住画面）。
func drawRoundFrame(dst *image.RGBA, w, h int) {
	thick := int(math.Max(6, float64(w)*0.018))
	col := color.NRGBA{58, 44, 96, 210}
	radius := int(float64(w) * 0.09)
	inset := thick / 2

	// 上下左右四条边（留出圆角）
	draw.Draw(dst, image.Rect(radius, inset, w-radius, inset+thick),
		&image.Uniform{col}, image.Point{}, draw.Over)
	draw.Draw(dst, image.Rect(radius, h-inset-thick, w-radius, h-inset),
		&image.Uniform{col}, image.Point{}, draw.Over)
	draw.Draw(dst, image.Rect(inset, radius, inset+thick, h-radius),
		&image.Uniform{col}, image.Point{}, draw.Over)
	draw.Draw(dst, image.Rect(w-inset-thick, radius, w-inset, h-radius),
		&image.Uniform{col}, image.Point{}, draw.Over)

	// 四个圆角：用环形逐点判断
	corners := [][2]int{
		{radius, radius}, {w - radius, radius},
		{radius, h - radius}, {w - radius, h - radius},
	}
	for _, c := range corners {
		for dy := -radius; dy <= radius; dy++ {
			for dx := -radius; dx <= radius; dx++ {
				r := math.Hypot(float64(dx), float64(dy))
				if r <= float64(radius) && r >= float64(radius-thick) {
					x, y := c[0]+dx, c[1]+dy
					if image.Pt(x, y).In(dst.Bounds()) {
						dst.Set(x, y, col)
					}
				}
			}
		}
	}
}

// drawHeart 右上角爱心（两个圆 + 一个倒三角）。
func drawHeart(dst *image.RGBA, w, h int) {
	cx := int(float64(w) * 0.84)
	cy := int(float64(h) * 0.14)
	s := float64(w) * 0.055
	col := color.NRGBA{255, 92, 140, 235}
	r := int(s * 0.62)
	fillCircleAlpha(dst, cx-r, cy-r/2, r, col)
	fillCircleAlpha(dst, cx+r, cy-r/2, r, col)
	// 下半三角
	for dy := 0; dy < int(s*1.5); dy++ {
		half := int(s * 1.15 * (1 - float64(dy)/float64(s*1.5)))
		y := cy + dy - r/2
		for dx := -half; dx <= half; dx++ {
			x := cx + dx
			if image.Pt(x, y).In(dst.Bounds()) {
				dst.Set(x, y, col)
			}
		}
	}
}

// drawStar 右上角四角星。
func drawStar(dst *image.RGBA, w, h int) {
	cx := int(float64(w) * 0.85)
	cy := int(float64(h) * 0.13)
	outer := float64(w) * 0.055
	inner := outer * 0.34
	col := color.NRGBA{255, 214, 102, 240}
	pts := make([][2]float64, 0, 8)
	for i := 0; i < 8; i++ {
		r := outer
		if i%2 == 1 {
			r = inner
		}
		a := -math.Pi/2 + float64(i)*math.Pi/4
		pts = append(pts, [2]float64{float64(cx) + r*math.Cos(a), float64(cy) + r*math.Sin(a)})
	}
	fillPolygon(dst, pts, col)
}

// drawCaption 底部字幕条：半透明底 + 居中文字（字体缺失时退化为纯色条）。
func drawCaption(dst *image.RGBA, w, h int, text, fontPath string) {
	size := float64(w) * 0.062
	barH := int(size * 2.1)
	barTop := h - barH - int(float64(h)*0.035)
	// 圆角感：用逐行内缩的矩形近似
	radius := barH / 2
	for y := barTop; y < barTop+barH && y < h; y++ {
		inset := 0
		dy := y - barTop
		if dy < radius {
			inset = int(float64(radius) * (1 - math.Sin(math.Pi/2*float64(dy)/float64(radius))))
		} else if barTop+barH-y < radius {
			dy2 := barTop + barH - y
			inset = int(float64(radius) * (1 - math.Sin(math.Pi/2*float64(dy2)/float64(radius))))
		}
		draw.Draw(dst, image.Rect(inset+int(float64(w)*0.06), y, w-inset-int(float64(w)*0.06), y+1),
			&image.Uniform{color.NRGBA{30, 24, 48, 190}}, image.Point{}, draw.Over)
	}
	if fontPath == "" {
		return
	}
	shown := fitText(fontPath, size, text, w-int(float64(w)*0.20))
	tw := textWidth(fontPath, size, shown)
	x := (w - tw) / 2
	y := barTop + barH/2 + int(size*0.36)
	drawText(dst, fontPath, size, x, y, shown, color.NRGBA{255, 246, 236, 255})
}

// ── 基础图元 ─────────────────────────────────────────────

func fillEllipseAlpha(dst *image.RGBA, cx, cy, rx, ry int, c color.NRGBA) {
	if rx <= 0 || ry <= 0 {
		return
	}
	for y := cy - ry; y <= cy+ry; y++ {
		for x := cx - rx; x <= cx+rx; x++ {
			dx := float64(x-cx) / float64(rx)
			dy := float64(y-cy) / float64(ry)
			if dx*dx+dy*dy <= 1 && image.Pt(x, y).In(dst.Bounds()) {
				blendPixel(dst, x, y, c)
			}
		}
	}
}

func fillCircleAlpha(dst *image.RGBA, cx, cy, r int, c color.NRGBA) {
	fillEllipseAlpha(dst, cx, cy, r, r, c)
}

func fillPolygon(dst *image.RGBA, pts [][2]float64, c color.NRGBA) {
	if len(pts) < 3 {
		return
	}
	minX, maxX := pts[0][0], pts[0][0]
	minY, maxY := pts[0][1], pts[0][1]
	for _, p := range pts {
		minX = math.Min(minX, p[0])
		maxX = math.Max(maxX, p[0])
		minY = math.Min(minY, p[1])
		maxY = math.Max(maxY, p[1])
	}
	for y := int(minY); y <= int(maxY); y++ {
		for x := int(minX); x <= int(maxX); x++ {
			if pointInPolygon(float64(x)+0.5, float64(y)+0.5, pts) && image.Pt(x, y).In(dst.Bounds()) {
				blendPixel(dst, x, y, c)
			}
		}
	}
}

func pointInPolygon(x, y float64, pts [][2]float64) bool {
	inside := false
	j := len(pts) - 1
	for i := 0; i < len(pts); i++ {
		yi, yj := pts[i][1], pts[j][1]
		if (yi > y) != (yj > y) {
			xi, xj := pts[i][0], pts[j][0]
			if x < (xj-xi)*(y-yi)/(yj-yi)+xi {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

// blendPixel 手动 alpha 混合（draw.Over 对单像素不便）。
func blendPixel(dst *image.RGBA, x, y int, c color.NRGBA) {
	if c.A == 0 {
		return
	}
	i := dst.PixOffset(x, y)
	sa := float64(c.A) / 255
	da := float64(dst.Pix[i+3]) / 255
	outA := sa + da*(1-sa)
	if outA <= 0 {
		return
	}
	for k, sc := range []uint8{c.R, c.G, c.B} {
		dc := dst.Pix[i+k]
		v := (float64(sc)*sa + float64(dc)*da*(1-sa)) / outA
		dst.Pix[i+k] = uint8(math.Round(v))
	}
	dst.Pix[i+3] = uint8(math.Round(outA * 255))
}
