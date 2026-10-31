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
画布：viewBox="0 0 256 256"，背景铺满整块。
只能用的元素：path、circle、ellipse、rect（可用 rx 圆角）、polygon、polyline、line。
只能用的属性：fill、stroke、stroke-width、stroke-linecap、stroke-dasharray、opacity、
  fill-opacity、linearGradient/radialGradient（用 url(#id) 引用）、<use href="#id">。
**禁止**：<text>（渲染不出来）、<g transform>（整组不画）、clipPath/mask/filter、<style>/CSS、外部图片。
所有坐标直接算成绝对坐标（没有 transform 可用）。
画风：粗描边（stroke-width 6~12）+ 纯色/渐变填充 + 大脸 + 少元素（5~15 个形状），
  靠眼睛形状/眉毛角度/嘴型/汗滴/怒气符号表达情绪，不靠文字。
配色：主色 + 对比色 + 一个强调色，深色描边（#1b2330 之类）保证小图也清楚。
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
	maxSVGBytes    = 64 << 10
	maxSVGSide     = 512
	defaultSVGSide = 256
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
		icon, e := oksvg.ReadIconStream(strings.NewReader(svg), oksvg.WarnErrorMode)
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
		icon, e := oksvg.ReadIconStream(strings.NewReader(svg), oksvg.WarnErrorMode)
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
