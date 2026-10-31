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

const (
	maxSVGBytes    = 64 << 10
	maxSVGSide     = 512
	defaultSVGSide = 256
)

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
