package qqbot

// meihua_chart.go 把梅花易数卦象画成图（本卦/互卦/变卦 + 体用吉凶）。
//
// 为什么用 image.RGBA 而不是 SVG：oksvg 不支持 <text>，中文标签画不出来；
// chart.go 已经有字体渲染（simhei.ttf），直接复用。
// 设计目标"简洁明了"：三个卦并排、动爻高亮、底部一行结论。

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
)

// 配色（沿用图表那套：白卡片 + 军绿标题 + 深色文字）。
var (
	mhBG     = color.RGBA{0xf2, 0xf2, 0xf2, 0xff}
	mhCard   = color.RGBA{0xff, 0xff, 0xff, 0xff}
	mhTitle  = color.RGBA{0x9c, 0xbf, 0x3b, 0xff}
	mhInk    = color.RGBA{0x1b, 0x23, 0x30, 0xff}
	mhMuted  = color.RGBA{0x8a, 0x94, 0xa6, 0xff}
	mhMoving = color.RGBA{0xff, 0x8a, 0x00, 0xff} // 动爻
	mhGood   = color.RGBA{0x2e, 0x9e, 0x5b, 0xff}
	mhBad    = color.RGBA{0xd9, 0x3b, 0x3b, 0xff}
	mhWarn   = color.RGBA{0xd9, 0x8c, 0x1f, 0xff}
)

// RenderMeihuaChart 渲染卦象图。fontPath 为空时退回纯图形（无文字）。
func RenderMeihuaChart(r MeihuaResult, fontPath string) ([]byte, error) {
	const (
		W = 760
		H = 520
	)
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	fillRect(img, img.Bounds(), mhBG)

	// 顶部标题条
	fillRect(img, image.Rect(0, 0, W, 76), mhTitle)
	if fontPath != "" {
		drawText(img, fontPath, 30, 28, 50, "梅花易数 · "+r.Name, color.White)
	}

	// 三个卦卡片
	type panel struct {
		title string
		name  string
		lines [6]bool
		mark  int // 高亮爻（1-6），0 = 无
	}
	panels := []panel{
		{"本卦", r.Name, r.Lines, r.Moving},
		{"互卦", r.MutualName, hexLines(r.MutualName), 0},
		{"变卦", r.ChangedName, hexLines(r.ChangedName), 0},
	}
	cardW, cardH := 224, 286
	gap := (W - 3*cardW) / 4
	for i, p := range panels {
		x := gap + i*(cardW+gap)
		y := 100
		fillRect(img, image.Rect(x, y, x+cardW, y+cardH), mhCard)
		if fontPath != "" {
			drawText(img, fontPath, 17, x+18, y+30, p.title, mhMuted)
			name := fitText(fontPath, 24, p.name, cardW-36)
			drawText(img, fontPath, 24, x+18, y+66, name, mhInk)
		}
		// 六爻：自下而上，第 1 爻画在最下面
		yaoW, yaoH := 120, 13
		cx := x + cardW/2
		for k := 0; k < 6; k++ {
			// k=0 → 第 1 爻（最下）
			lineIdx := k // Lines[0] 是最下爻
			yy := y + cardH - 50 - k*30
			yang := p.lines[lineIdx]
			isMoving := p.mark == k+1
			col := mhInk
			if isMoving {
				col = mhMoving
			}
			drawYao(img, cx, yy, yaoW, yaoH, yang, col)
			// 动爻只靠橙色区分（早先加的小圆点会压在爻线上，去掉更干净）
		}
	}

	// 底部：体用 + 吉凶
	baseY := 408
	if fontPath != "" {
		line := fmt.Sprintf("体 %s（%s）　用 %s（%s）　%s",
			r.BodyName, r.BodyElem, r.UseName, r.UseElem, r.Relation)
		drawText(img, fontPath, 22, 28, baseY, fitText(fontPath, 22, line, W-56), mhInk)

		// 吉凶大字
		vcol := mhWarn
		switch r.Verdict {
		case "吉":
			vcol = mhGood
		case "凶":
			vcol = mhBad
		}
		drawText(img, fontPath, 40, 28, baseY+56, r.Verdict, vcol)
		// 断语（一行，超长省略）
		detail := fitText(fontPath, 19, r.Detail, W-180)
		drawText(img, fontPath, 19, 120, baseY+50, detail, mhMuted)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// hexLines 由卦名反查六爻（用于互卦/变卦这种只有名字的）。
//
// 用 64 卦表反查：遍历 hexNames 找到名字，再拼上下卦的三爻。
func hexLines(name string) [6]bool {
	for up := 1; up <= 8; up++ {
		for low := 1; low <= 8; low++ {
			if hexNames[up][low] != name {
				continue
			}
			var out [6]bool
			for i := 0; i < 3; i++ {
				out[i] = baguaList[low].Lines[i]
				out[i+3] = baguaList[up].Lines[i]
			}
			return out
		}
	}
	return [6]bool{}
}

// drawYao 画一条爻：阳爻实线、阴爻断开。
func drawYao(dst *image.RGBA, cx, y, w, h int, yang bool, col color.Color) {
	if yang {
		fillRect(dst, image.Rect(cx-w/2, y, cx+w/2, y+h), col)
		return
	}
	gapW := w / 4
	fillRect(dst, image.Rect(cx-w/2, y, cx-gapW/2, y+h), col)
	fillRect(dst, image.Rect(cx+gapW/2, y, cx+w/2, y+h), col)
}

// meihuaSummary 图之后单独发的那条文字解析（简短，图里已有卦象）。
func meihuaSummary(r MeihuaResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n", r.Method)
	fmt.Fprintf(&sb, "本卦 %s，动爻第 %d 爻；互卦 %s，变卦 %s\n", r.Name, r.Moving, r.MutualName, r.ChangedName)
	fmt.Fprintf(&sb, "体 %s（%s）· 用 %s（%s）→ %s，%s\n", r.BodyName, r.BodyElem, r.UseName, r.UseElem, r.Relation, r.Verdict)
	sb.WriteString(r.Detail)
	return sb.String()
}

var _ = draw.Draw // 保留 image/draw 依赖（fillRect 在 chart.go 里用它）
