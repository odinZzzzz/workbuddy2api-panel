package qqbot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stickers_collect.go —— 收藏群聊里收到的表情包。
//
// 群里斗图时别人发的图，其实就是最好的表情素材库；与其让用户手动上传，
// 不如收到就顺手存一份（去重、限大小），之后 send_sticker 随机发的时候能挑到。
//
// 注意：只存**普通图片**。QQ 的商城表情（mface 段）只有 emoji id、没有图源，
// 拿不到内容，只能跳过。

const collectSubdir = "collected"

// stickerHash 用内容哈希做文件名：天然去重，也不会因 URL 里的临时签名重复存。
func stickerHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// collectStickers 把本轮消息里的图片存进收藏目录（异步调用，失败静默）。
func (b *Bridge) collectStickers(urls []string) {
	cfg := b.cfg()
	if cfg.DisableStickerCollect || len(urls) == 0 {
		return
	}
	dir := strings.TrimSpace(cfg.StickerDir)
	if dir == "" {
		return
	}
	out := filepath.Join(dir, collectSubdir)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return
	}
	maxBytes := int64(cfg.StickerCollectMaxKB)
	if maxBytes <= 0 {
		maxBytes = 1024
	}
	maxBytes *= 1024

	client := &http.Client{Timeout: 12 * time.Second}
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			continue
		}
		resp, err := client.Get(u)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		resp.Body.Close()
		if err != nil || int64(len(body)) > maxBytes || len(body) < 2048 {
			continue // 太大（可能是照片）或太小（头像/图标），都不收
		}
		ext := stickerExt(body, u)
		if ext == "" {
			continue
		}
		name := stickerHash(body) + ext
		path := filepath.Join(out, name)
		if _, err := os.Stat(path); err == nil {
			continue // 已经收过
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			continue
		}
		log.Printf("[qqbot] 收藏表情 %s（%d KB）", name, len(body)/1024)
		b.state.note(RecentEvent{Time: time.Now(), Type: "sticker", Action: "sticker_collected"})
	}
}

// stickerExt 判断图片类型（只认表情常用格式）；认不出返回空。
func stickerExt(data []byte, url string) string {
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		return ".jpg"
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return ".png"
	case len(data) > 6 && (string(data[0:3]) == "GIF"):
		return ".gif"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	}
	// 兜底看 URL 后缀
	for _, e := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.Contains(strings.ToLower(url), e) {
			if e == ".jpeg" {
				return ".jpg"
			}
			return e
		}
	}
	return ""
}

// collectedStickers 列出收藏目录里的图片。
func collectedStickers(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, collectSubdir))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp":
			out = append(out, filepath.Join(dir, collectSubdir, e.Name()))
		}
	}
	return out
}

// collectedCount 收藏了多少张（给后台/日志用）。
func collectedCount(dir string) int { return len(collectedStickers(dir)) }

var _ = fmt.Sprintf
