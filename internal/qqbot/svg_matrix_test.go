package qqbot

import (
	"bytes"
	"image/png"
	"testing"
)

// probe 渲染一段 SVG，返回"有内容的像素数"（alpha>0 且非纯透明）。
//
// 用像素数当判据而不是"有没有报错"：oksvg 对不支持的标签只是跳过（WarnErrorMode
// 打一行警告），渲染照样"成功"——只有数像素才能看出那个特性到底画没画出来。
func probe(t *testing.T, name, svg string) int {
	t.Helper()
	out, err := RenderSVG(svg, 128)
	if err != nil {
		t.Logf("%-22s ERROR: %v", name, err)
		return 0
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Logf("%-22s PNG 解码失败: %v", name, err)
		return 0
	}
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a > 0x1000 {
				n++
			}
		}
	}
	t.Logf("%-22s ink=%d", name, n)
	return n
}

// TestSVGFeatureMatrix 探测 oksvg 实际支持哪些 SVG 特性。
//
// 目的不是"测代码"，而是产出**给模型看的白名单**：模型只应该用这些能真正画出来的
// 特性，否则它辛苦写的 SVG 会静默少一半（实测 <text> 就是不渲染的）。
func TestSVGFeatureMatrix(t *testing.T) {
	head := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">`
	tail := `</svg>`

	cases := []struct{ name, body string }{
		{"path-fill", `<path d="M40 40 L216 40 L128 216 Z" fill="#4a7bd6"/>`},
		{"circle-stroke", `<circle cx="128" cy="128" r="80" fill="none" stroke="#e06c6c" stroke-width="12"/>`},
		{"rect-rounded", `<rect x="40" y="40" width="176" height="176" rx="32" fill="#3ddc97"/>`},
		{"ellipse", `<ellipse cx="128" cy="128" rx="90" ry="60" fill="#f5b544"/>`},
		{"polygon", `<polygon points="128,32 216,208 40,208" fill="#8b7cf6"/>`},
		{"polyline-stroke", `<polyline points="32,200 96,64 160,200 224,64" fill="none" stroke="#4fc3f7" stroke-width="10"/>`},
		{"line", `<line x1="32" y1="128" x2="224" y2="128" stroke="#ffffff" stroke-width="16"/>`},
		{"group-transform", `<g transform="translate(128,128) rotate(30) scale(0.8)"><rect x="-60" y="-60" width="120" height="120" fill="#5b7cfa"/></g>`},
		{"opacity", `<circle cx="128" cy="128" r="90" fill="#e06c9c" opacity="0.5"/>`},
		{"linear-gradient", `<defs><linearGradient id="g1" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#5b7cfa"/><stop offset="1" stop-color="#3ddc97"/></linearGradient></defs><rect x="24" y="24" width="208" height="208" fill="url(#g1)"/>`},
		{"radial-gradient", `<defs><radialGradient id="g2"><stop offset="0" stop-color="#ffffff"/><stop offset="1" stop-color="#4a7bd6"/></radialGradient></defs><circle cx="128" cy="128" r="96" fill="url(#g2)"/>`},
		{"dasharray", `<line x1="24" y1="128" x2="232" y2="128" stroke="#ffffff" stroke-width="14" stroke-dasharray="16 12"/>`},
		{"text", `<text x="128" y="140" font-size="48" fill="#ffffff" text-anchor="middle">怒</text>`},
		{"clipPath", `<defs><clipPath id="c1"><circle cx="128" cy="128" r="70"/></clipPath></defs><rect x="0" y="0" width="256" height="256" fill="#e06c6c" clip-path="url(#c1)"/>`},
		{"stroke-linecap", `<line x1="64" y1="128" x2="192" y2="128" stroke="#ffffff" stroke-width="24" stroke-linecap="round"/>`},
		{"use-ref", `<defs><circle id="dot" cx="0" cy="0" r="20" fill="#f5b544"/></defs><use href="#dot" x="128" y="128"/>`},
	}

	supported, unsupported := 0, 0
	for _, c := range cases {
		n := probe(t, c.name, head+c.body+tail)
		if n > 50 {
			supported++
		} else {
			unsupported++
		}
	}
	t.Logf("可用特性 %d 个 / 不可用（或几乎无输出）%d 个", supported, unsupported)
}
