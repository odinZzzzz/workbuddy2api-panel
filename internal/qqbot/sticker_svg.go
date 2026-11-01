package qqbot

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"math"
	"regexp"
	"strconv"
)

// sticker_svg.go —— 让模型自己画表情包（SVG → PNG → 发出去）。
//
// 为什么走 SVG：模型没法直接产出位图，但很擅长写 SVG（矢量、纯文本、可校验）。
// 服务端用 oksvg + rasterx 纯 Go 光栅化，不引 headless 浏览器——那台机器 1.7G
// 内存，再起个 Chromium 就为了把几个圆和字画成图片不值当。
//
// 约束（防止一条离谱的 SVG 把服务拖死）：
//   - SVG 源码 ≤ 64KB
//   - 画布边长 ≤ 512（QQ 表情包够用，光栅化开销可控）
//   - 解析/绘制 panic 一律 recover 成错误（第三方渲染库遇到畸形输入会 panic）

// stickerStyleGuide 给模型看的"能画什么"清单（基于实测，不是抄文档）。
//
// 实测矩阵（128px 渲染后数非透明像素）：
//
//	可用：path / circle / ellipse / rect(rx) / polygon / polyline / line、
//	      fill / stroke / stroke-width / stroke-linecap / stroke-dasharray / opacity、
//	      linearGradient / radialGradient（url(#id)）、<use href>
//	不可用：<text>（完全不渲染）、<g transform="...">（整组消失）、clipPath/mask/filter（静默忽略）、
//	       <style>/CSS、外部图片
//
// 所以规范里最要紧的两条是：**别写文字**、**别用 transform 分组**（坐标要自己算好）。
const stickerStyleGuide = `
【两种模式，先判断要哪种】
A《表情贴纸》：256 画布、6~10 个形状、粗描边、夸张表情 → 用来怼人/吐槽。
B《插图》：要"像一张普通图片"时用 → 有形体、有明暗、有分层、60~90 个形状。
   用户说"画个猫/画个风景/画张图/画个角色"= B；说"来个表情/贴纸"= A。

──────────────── A：表情贴纸 ────────────────
脸圆心 (128,132) 半径 86，描边 #1b2330 / 10px；五官到圆心 ≤74（禁止穿出轮廓，
穿出去就是"飘在脸外的黑棍"）；标记放四角留 10px；形状 6~10 个。
眉毛 stroke 13~14 + round；眼睛实心圆 r≈12~13 + 白高光 r≈4.5 左上偏；嘴一条 path。

──────────────── B：插图 ────────────────
画布 viewBox="0 0 320 170"（趴姿/横构图）或 "0 0 256 256"（正面/方形），导出 512px。

【1】形状 60~90 个。画出来像"色块拼图"就是因为形状太少。

【2】同一色系 5~8 级明度阶（这条最关键）
   不是"三色"——主体一个色相要拉出明暗阶，例（蓝发）：
     #cdeeff  #bfe6ff  #9fd4f7  #8ecdf7  #7cc4f7  #5f9ad0  #4a8fd8
   用法：base 铺面 → +2 阶画受光面 → +3 阶画高光 → -2 阶画背光/褶皱 → -3 阶画最深交界。
   肤色例：#ffe9de（亮） #f6c9b8（暗） #e8a894（阴影交界）。

【3】描边 1.5~2.5px 细线（不是贴纸那种 10px）。
   线色用同色系更深一阶（蓝发的线用 #5f9ad0），或肤色线条 #e8a894。
   大色块交界可以完全不描边，靠明度差分开。

【4】部件分层，按这个顺序叠（先画=在下面）：
   躯干/道具 → 大色块（头发主团/衣服） → 发丝与形体（用椭圆堆，别用直边）
   → 面部 → 五官（眉/眼/腮红/嘴分开画） → 配饰（星星/发饰）
   → 特效层（泡泡、星星、Zzz、音符——让画面"活"）

【5】体积感：每个大色块上叠 2~3 个浅色椭圆当高光，边缘留一条更深的压边。
   没有 filter 可用，柔和过渡只能靠"同形状 + opacity 0.2~0.4"反复叠。

【6】眼睛是脸的门面：白眼球 + 渐变虹膜 + 竖瞳 + 两个高光（一大一小），
   再加一道上眼睑深色压边；闭眼用弧线 + 睫毛。

禁止：一个大圆 + 两个点当脸；直边三角块当毛发（会渲染成浮空围兜）。

──────────────── 通用限制 ────────────────
只能用：path、circle、ellipse、rect、polygon、polyline、line。
只能设：fill、stroke、stroke-width、stroke-linecap、stroke-dasharray、opacity、fill-opacity、
  linearGradient/radialGradient（url(#id) 引用）、<use href="#id">。
**禁止**：<text>（一个字都不出来）、<g transform>（整组不画）、clipPath/mask/filter/<style>、外部图片。
没有 transform → 所有坐标手算成绝对坐标。

流程：先 list_sticker_templates 看有没有接近的参考（结果里带本规范），
有就 get_sticker_template 取它的**画法规格**（配色/部件/线宽），照着规格**自己重画**。
**禁止原样照抄模板**——那是复制粘贴，不是画。没有合适的才完全凭空画。
`

// inkPixels 数非透明像素（判断"画出来了没有"）。
func inkPixels(pngBytes []byte) (int, error) {
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return 0, err
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
	return n, nil
}

const (
	maxSVGBytes = 64 << 10
	// 插图需要细节：导出 512 起步，上限放宽到 1024（QQ 端显示约 150~300px，
	// 但源图大一点在相册/转发里不糊）。256 只适合纯表情贴纸。
	maxSVGSide     = 1024
	defaultSVGSide = 512
)

// RenderSVGTo 按指定宽高渲染（保持 viewBox 比例，用于宽图/柱状图）。
func RenderSVGTo(svg string, w, h int) ([]byte, error) {
	if strings.TrimSpace(svg) == "" {
		return nil, fmt.Errorf("svg 为空")
	}
	if len(svg) > maxSVGBytes {
		return nil, fmt.Errorf("svg 太大（%d 字节，上限 %d）", len(svg), maxSVGBytes)
	}
	if !strings.Contains(svg, "<svg") {
		return nil, fmt.Errorf("不像 SVG（缺少 <svg> 标签）")
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("宽高必须为正")
	}
	if w > 2000 || h > 2000 {
		return nil, fmt.Errorf("画布过大（%dx%d，上限 2000）", w, h)
	}
	var out []byte
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("渲染 SVG 失败: %v", r)
			}
		}()
		icon, e := oksvg.ReadIconStream(strings.NewReader(normalizeSVGColors(svg)), oksvg.WarnErrorMode)
		if e != nil {
			err = fmt.Errorf("解析 SVG: %w", e)
			return
		}
		icon.SetTarget(0, 0, float64(w), float64(h))
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		scanner := rasterx.NewScannerGV(w, h, img, img.Bounds())
		dasher := rasterx.NewDasher(w, h, scanner)
		icon.Draw(dasher, 1.0)
		var buf bytes.Buffer
		if e := png.Encode(&buf, img); e != nil {
			err = fmt.Errorf("编码 PNG: %w", e)
			return
		}
		out = buf.Bytes()
	}()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RenderSVG 把 SVG 源码渲染成 PNG 字节。
func RenderSVG(svg string, side int) ([]byte, error) {
	if strings.TrimSpace(svg) == "" {
		return nil, fmt.Errorf("svg 为空")
	}
	if len(svg) > maxSVGBytes {
		return nil, fmt.Errorf("svg 太大（%d 字节，上限 %d）", len(svg), maxSVGBytes)
	}
	if !strings.Contains(svg, "<svg") {
		return nil, fmt.Errorf("不像 SVG（缺少 <svg> 标签）")
	}
	if side <= 0 {
		side = defaultSVGSide
	}
	if side > maxSVGSide {
		side = maxSVGSide
	}

	var out []byte
	var err error
	func() {
		// 第三方渲染库对畸形输入会 panic，这里兜住——一条坏 SVG 不该让桥重启。
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("渲染 SVG 失败: %v", r)
			}
		}()
		icon, e := oksvg.ReadIconStream(strings.NewReader(normalizeSVGColors(svg)), oksvg.WarnErrorMode)
		if e != nil {
			err = fmt.Errorf("解析 SVG: %w", e)
			return
		}
		icon.SetTarget(0, 0, float64(side), float64(side))
		img := image.NewRGBA(image.Rect(0, 0, side, side))
		scanner := rasterx.NewScannerGV(side, side, img, img.Bounds())
		dasher := rasterx.NewDasher(side, side, scanner)
		icon.Draw(dasher, 1.0)
		var buf bytes.Buffer
		if e := png.Encode(&buf, img); e != nil {
			err = fmt.Errorf("编码 PNG: %w", e)
			return
		}
		out = buf.Bytes()
	}()
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("渲染结果为空")
	}
	return out, nil
}

// SaveGeneratedSticker 把渲染好的 PNG 存到表情目录（生成的图也能被后续复用）。
func (b *Bridge) SaveGeneratedSticker(pngBytes []byte) (string, error) {
	dir := strings.TrimSpace(b.cfg().StickerDir)
	if dir == "" {
		return "", fmt.Errorf("未配置 sticker_dir，无法保存自绘表情")
	}
	gen := filepath.Join(dir, "generated")
	if err := os.MkdirAll(gen, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("drawn_%d.png", time.Now().UnixNano())
	path := filepath.Join(gen, name)
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// svgViewBoxRe 抓 viewBox（宽松匹配：属性顺序/空白都不敏感）。
var svgViewBoxRe = regexp.MustCompile(`viewBox\s*=\s*"([^"]+)"`)

// aspectSize 按 viewBox 比例算导出尺寸（最长边 = maxSide）。
//
// 为什么需要：RenderSVG 会把内容 SetTarget 到正方形，320×170 的横构图插图
// 会被**拉扁**（鲸鱼娘那种趴姿图尤其明显）。渲染前先按 viewBox 算好宽高，
// 再走 RenderSVGTo，宽高比就对了。
func aspectSize(svg string, maxSide int) (int, int) {
	if m := svgViewBoxRe.FindStringSubmatch(svg); m != nil {
		if f := strings.Fields(m[1]); len(f) == 4 {
			w, e1 := strconv.ParseFloat(f[2], 64)
			h, e2 := strconv.ParseFloat(f[3], 64)
			if e1 == nil && e2 == nil && w > 0 && h > 0 {
				if w >= h {
					return maxSide, int(math.Round(float64(maxSide) * h / w))
				}
				return int(math.Round(float64(maxSide) * w / h)), maxSide
			}
		}
	}
	return maxSide, maxSide
}

// RenderSVGAuto 按 viewBox 比例渲染（最长边 maxSide），返回 PNG 与实际绘制尺寸。
func RenderSVGAuto(svg string, maxSide int) ([]byte, int, int, error) {
	if maxSide <= 0 {
		maxSide = defaultSVGSide
	}
	if maxSide > maxSVGSide {
		maxSide = maxSVGSide
	}
	w, h := aspectSize(svg, maxSide)
	png, err := RenderSVGTo(svg, w, h)
	return png, w, h, err
}

// svgRGBARe 匹配 rgb()/rgba() 颜色写法。
var svgRGBARe = regexp.MustCompile(`rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([0-9.]+)\s*)?\)`)

// normalizeSVGColors 把 rgb()/rgba() 归一化成 #rrggbb + 透明度属性。
//
// 为什么必须做：oksvg 的颜色解析器**不认 CSS 颜色函数**——`fill="rgba(30,60,120,.1)"`
// 会让整份 SVG 解析失败（报 "param mismatch"），一张图直接画不出来。而 rgba 是模型
// 最顺手的写法（半透明阴影、光斑几乎必写），不归一化等于大量"画不出来"。
//
// 处理：rgba(r,g,b,a) → fill/stroke/stop-color="#rrggbb" + 对应的 *-opacity="a"；
// rgb(r,g,b) → "#rrggbb"。
func normalizeSVGColors(svg string) string {
	if !strings.Contains(svg, "rgb") {
		return svg
	}
	// 逐属性替换，才能知道该写 opacity 还是 stroke-opacity / stop-opacity。
	attrRe := regexp.MustCompile(`(fill|stroke|stop-color)="(rgba?\([^")]*\))"`)
	return attrRe.ReplaceAllStringFunc(svg, func(m string) string {
		sub := attrRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		attr, val := sub[1], sub[2]
		nums := svgRGBARe.FindStringSubmatch(val)
		if len(nums) < 4 {
			return m
		}
		r, g, b := nums[1], nums[2], nums[3]
		alpha := ""
		if len(nums) > 4 {
			alpha = nums[4]
		}
		hex := "#" + pad2(itoaHex(r)) + pad2(itoaHex(g)) + pad2(itoaHex(b))
		if alpha == "" || alpha == "1" {
			return attr + `="` + hex + `"`
		}
		opAttr := "opacity"
		if attr == "stroke" {
			opAttr = "stroke-opacity"
		} else if attr == "stop-color" {
			opAttr = "stop-opacity"
		}
		return attr + `="` + hex + `" ` + opAttr + `="` + alpha + `"`
	})
}

// itoaHex 十进制字符串 → 两位小写十六进制（输入是 0~255 的十进制文本）。
func itoaHex(s string) string {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 || n > 255 {
		return "00"
	}
	return strconv.FormatInt(int64(n), 16)
}

func pad2(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// countSVGShapes 数 SVG 里的可绘制元素数（用于插图质量闸门）。
func countSVGShapes(src string) int {
	n := 0
	for _, tag := range []string{"path", "ellipse", "circle", "rect", "polygon", "polyline", "line"} {
		n += len(svgTagRe(tag).FindAllString(src, -1))
	}
	return n
}
