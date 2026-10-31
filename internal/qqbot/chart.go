package qqbot

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "image/jpeg"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

// chart.go —— 图文结合的数据卡。
//
// 设计取向（按用户反馈迭代过三轮）：
//   - **数字与名字是主角**：数值 34px 粗体，名字 18px；进度条只留 6px 细条当配角；
//   - **横向排列**：一行放 4 个（超过 8 条折成两行），像排行榜横排卡片；
//   - **统一配色**：所有数值用同一个主色（LayUI 蓝 #1e9fff），不按名次换色；
//     只有第一名额外加一条橙色下划线做"冠军标记"，其余完全一致；
//   - **配色抄 kkrb**：LayUI 主题 —— 底色 #f2f2f2、卡片白、正文 #333/#666；
//   - **图标借用 kkrb 的物品图**（playerhub PNG 直链），有就画、没有跳过。
//
// 文字为什么自己画：oksvg 不渲染 <text>（实测 ink=0），所以图形用 SVG/直接像素画、
// 文字用系统字体（simhei.ttf）叠在 PNG 上。

// ChartItem 一条数据。
type ChartItem struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Icon  string  `json:"icon,omitempty"` // 图标直链（可空）
}

// 统一配色（LayUI / kkrb 主题）
var (
	colPageBG = color.RGBA{0xf2, 0xf2, 0xf2, 0xff} // 页面底
	colCardBG = color.RGBA{0xff, 0xff, 0xff, 0xff} // 卡片
	colTitle  = color.RGBA{0x33, 0x33, 0x33, 0xff}
	colName   = color.RGBA{0x66, 0x66, 0x66, 0xff}
	colMuted  = color.RGBA{0x99, 0x99, 0x99, 0xff}
	colTrack  = color.RGBA{0xee, 0xee, 0xee, 0xff}
	colValue  = color.RGBA{0x1e, 0x9f, 0xff, 0xff} // 唯一主色（蓝）
	colChamp  = color.RGBA{0xff, 0x57, 0x22, 0xff} // 冠军标记（橙）
	colDiv    = color.RGBA{0xe6, 0xe6, 0xe6, 0xff}
)

const (
	chartWidth  = 720
	chartPerRow = 4
	chartPadL   = 24
	chartPadT   = 92
	chartPadB   = 26
	chartCellH  = 168
	chartIconSz = 40
	chartBarH   = 6
)

// fontCache 按 (路径,字号) 缓存 face，避免每张图都解析 9MB 字体。
var fontCache struct {
	mu    sync.Mutex
	faces map[string]font.Face
}

func loadFace(path string, size float64) (font.Face, error) {
	key := fmt.Sprintf("%s|%.0f", path, size)
	fontCache.mu.Lock()
	defer fontCache.mu.Unlock()
	if fontCache.faces == nil {
		fontCache.faces = map[string]font.Face{}
	}
	if f, ok := fontCache.faces[key]; ok {
		return f, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读字体失败(%s): %w", path, err)
	}
	f, err := opentype.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("解析字体失败(%s，建议用 .ttf 而非 .ttc): %w", path, err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	fontCache.faces[key] = face
	return face, nil
}

// drawText 画一行字；字体缺失时静默跳过（图照样出）。
func drawText(dst *image.RGBA, fontPath string, size float64, x, y int, s string, col color.Color) {
	face, err := loadFace(fontPath, size)
	if err != nil {
		return
	}
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(col), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

// textWidth 量文字宽度（字体缺失时按 0.6*size 估算）。
func textWidth(fontPath string, size float64, s string) int {
	face, err := loadFace(fontPath, size)
	if err != nil {
		return int(float64(len([]rune(s))) * size * 0.6)
	}
	return font.MeasureString(face, s).Ceil()
}

func ellipsize(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// fitText 按像素宽度截断（中文按实际宽度算，比按字数准）。
func fitText(fontPath string, size float64, s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if textWidth(fontPath, size, s) <= maxW {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if textWidth(fontPath, size, string(r)+"…") <= maxW {
			return string(r) + "…"
		}
	}
	return string(r)
}

// iconCache 图标缓存（URL → 小图）。
var iconCache struct {
	mu   sync.Mutex
	m    map[string]image.Image
	http *http.Client
}

func fetchIcon(url string) image.Image {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	iconCache.mu.Lock()
	if iconCache.m == nil {
		iconCache.m = map[string]image.Image{}
		iconCache.http = &http.Client{Timeout: 8 * time.Second}
	}
	if img, ok := iconCache.m[url]; ok {
		iconCache.mu.Unlock()
		return img
	}
	client := iconCache.http
	iconCache.mu.Unlock()

	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil
	}
	iconCache.mu.Lock()
	iconCache.m[url] = img
	iconCache.mu.Unlock()
	return img
}

// drawIcon 把图标缩放到 (x,y) 的 size×size 区域（最近邻，够用且便宜）。
func drawIcon(dst *image.RGBA, src image.Image, x, y, size int) {
	if src == nil {
		return
	}
	sb := src.Bounds()
	for dy := 0; dy < size; dy++ {
		for dx := 0; dx < size; dx++ {
			sx := sb.Min.X + dx*sb.Dx()/size
			sy := sb.Min.Y + dy*sb.Dy()/size
			dst.Set(x+dx, y+dy, src.At(sx, sy))
		}
	}
}

// fillRect 填充矩形。
func fillRect(dst *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(dst, r.Intersect(dst.Bounds()), &image.Uniform{c}, image.Point{}, draw.Src)
}

// RenderBarChart 画横向排列的数据卡：图标 + 名字 + 大号数字 + 细比例条。
func RenderBarChart(title string, items []ChartItem, fontPath string) ([]byte, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("没有数据可画")
	}
	if len(items) > 8 {
		items = items[:8] // 横排两行最多 8 个，再多就看不清了
	}
	sorted := append([]ChartItem(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })
	maxV := 0.0
	for _, it := range sorted {
		if math.Abs(it.Value) > maxV {
			maxV = math.Abs(it.Value)
		}
	}
	if maxV <= 0 {
		maxV = 1
	}

	rows := (len(sorted) + chartPerRow - 1) / chartPerRow
	h := chartPadT + rows*chartCellH + chartPadB
	canvas := image.NewRGBA(image.Rect(0, 0, chartWidth, h))

	// 底：页面灰 + 白色卡片 + 顶部主色条（统一视觉语言）
	fillRect(canvas, canvas.Bounds(), colPageBG)
	fillRect(canvas, canvas.Bounds(), colCardBG)
	fillRect(canvas, image.Rect(0, 0, chartWidth, 6), colValue)

	// 标题 + 分割线
	drawText(canvas, fontPath, 30, chartPadL, 60, fitText(fontPath, 30, title, chartWidth-2*chartPadL), colTitle)
	fillRect(canvas, image.Rect(chartPadL, chartPadT-22, chartWidth-chartPadL, chartPadT-21), colDiv)

	// 网格：横排 cell
	cellW := (chartWidth - 2*chartPadL) / chartPerRow
	for i, it := range sorted {
		row := i / chartPerRow
		col := i % chartPerRow
		x0 := chartPadL + col*cellW
		y0 := chartPadT + row*chartCellH

		// 冠军标记：第一名加一条橙色顶条（其余不加，保持统一）
		if i == 0 {
			fillRect(canvas, image.Rect(x0+8, y0-6, x0+cellW-8, y0-2), colChamp)
		}

		// 图标（居中）
		iconY := y0 + 6
		if img := fetchIcon(it.Icon); img != nil {
			drawIcon(canvas, img, x0+(cellW-chartIconSz)/2, iconY, chartIconSz)
		}

		// 名字（居中、按宽度截断）
		nameMaxW := cellW - 24
		name := fitText(fontPath, 18, it.Label, nameMaxW)
		nw := textWidth(fontPath, 18, name)
		drawText(canvas, fontPath, 18, x0+(cellW-nw)/2, iconY+chartIconSz+26, name, colName)

		// 数字（居中、大号、统一主色）
		valStr := formatChartValue(it.Value)
		vw := textWidth(fontPath, 34, valStr)
		drawText(canvas, fontPath, 34, x0+(cellW-vw)/2, iconY+chartIconSz+66, valStr, colValue)

		// 细比例条（配角，居中，统一主色）
		barW := cellW - 48
		if barW < 40 {
			barW = 40
		}
		barX := x0 + (cellW-barW)/2
		barY := iconY + chartIconSz + 82
		fillRect(canvas, image.Rect(barX, barY, barX+barW, barY+chartBarH), colTrack)
		fillW := int(math.Round(float64(barW) * math.Abs(it.Value) / maxV))
		if fillW < 4 {
			fillW = 4
		}
		fillRect(canvas, image.Rect(barX, barY, barX+fillW, barY+chartBarH), colValue)

		// 名次（左上小灰字）
		drawText(canvas, fontPath, 16, x0+8, y0+22, fmt.Sprintf("#%d", i+1), colMuted)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// formatChartValue 数值格式化：千分位；小数保留一位。
func formatChartValue(v float64) string {
	if math.Abs(v) >= 1000 {
		n := int64(math.Round(v))
		s := fmt.Sprintf("%d", n)
		var out []byte
		for i, c := range []byte(s) {
			if i > 0 && (len(s)-i)%3 == 0 && c != '-' {
				out = append(out, ',')
			}
			out = append(out, c)
		}
		return string(out)
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
