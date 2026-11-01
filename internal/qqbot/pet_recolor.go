package qqbot

// pet_recolor.go 对官方立绘（位图）做程序化改色。
//
// 为什么需要：用户问"立绘 AI 能直接改嘛"。真正的语义级重绘需要 img2img 模型，
// 但**换配色**这件事不需要 AI——直接对像素做 HSL 变换即可：画面内容、线条、
// 表情 100% 保持原画，只换色相/饱和/明度。质量下限 = 官方立绘本身。
//
// 与 tweak.go（作用于 SVG 的十六进制颜色）互补：那份改的是矢量颜色定义，
// 这份改的是已栅格化的像素。

import (
	"bytes"
	"image"
	"image/png"
	"math"
)

// RecolorPNG 对 PNG 做 HSL 调整，返回新 PNG 字节。
//
// 暂不启用：用户明确"别改色了，保持配色就行"，send_pet 已不再调用它。
// 保留实现与测试（含肤色保护），将来若要做主题色变体可直接接回。
// hueDeg 色相偏移（度）；satScale 饱和度倍数（1=不变）；lightDelta 明度偏移（-1~1）。
// 低饱和像素（接近灰/白/黑，如眼白、深色描边）默认跳过，避免线稿发脏。
func RecolorPNG(pngBytes []byte, hueDeg, satScale, lightDelta float64) ([]byte, error) {
	if hueDeg == 0 && satScale == 1 && lightDelta == 0 {
		return pngBytes, nil
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	if satScale <= 0 {
		satScale = 1
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r16, g16, b16, a16 := img.At(x, y).RGBA()
			if a16 == 0 {
				continue // 全透明像素原样保留（保持背景透明）
			}
			r := float64(r16>>8) / 255
			g := float64(g16>>8) / 255
			bl := float64(b16>>8) / 255
			h, s, l := rgbToHSL(r, g, bl)
			// 低饱和（线稿/眼白/黑瞳）不动：染了会脏。
			// 肤色保护：脸/手是橙-黄低饱和区间，染了会变"病态绿"——跳过。
			if s >= 0.12 && !(h >= 10 && h <= 58 && l > 0.42 && s < 0.75) {
				h = math.Mod(h+hueDeg+360, 360)
				s = clamp01(s * satScale)
				l = clamp01(l + lightDelta)
			}
			nr, ng, nb := hslToRGB(h, s, l)
			i := dst.PixOffset(x-b.Min.X, y-b.Min.Y)
			dst.Pix[i] = uint8(nr)
			dst.Pix[i+1] = uint8(ng)
			dst.Pix[i+2] = uint8(nb)
			dst.Pix[i+3] = uint8(a16 >> 8)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
