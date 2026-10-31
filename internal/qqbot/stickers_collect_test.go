package qqbot

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 收藏群聊表情：能下载、去重、识别格式，并且能被 pickSticker 挑到。
func TestCollectStickers(t *testing.T) {
	// 造一张「够大」的 PNG（>2KB）：纯渐变压缩后太小，加噪声保证体积
	img := image.NewRGBA(image.Rect(0, 0, 240, 240))
	seed := uint32(12345)
	for y := 0; y < 240; y++ {
		for x := 0; x < 240; x++ {
			seed = seed*1664525 + 1013904223 // 简易 LCG，别引额外依赖
			img.Set(x, y, color.RGBA{uint8(seed >> 24), uint8(seed >> 16), uint8(seed >> 8), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if len(data) < 2048 {
		t.Fatalf("测试图太小（%d），构造失败", len(data))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := Default()
	cfg.StickerDir = dir
	cfg.StickerCollectMaxKB = 1024
	b := New(cfg)

	// 同一张图收两次 → 只存一份（内容哈希去重）
	b.collectStickers([]string{srv.URL + "/a.png"})
	b.collectStickers([]string{srv.URL + "/b.png"})
	if n := collectedCount(dir); n != 1 {
		t.Fatalf("去重失败：收藏了 %d 张，期望 1 张", n)
	}

	// 收藏的图要能被随机发表情挑到
	if got := b.pickSticker(); got == "" {
		t.Fatal("pickSticker 没挑到收藏的表情")
	}

	// 非 http 源要跳过（不 panic、不写文件）
	before := collectedCount(dir)
	b.collectStickers([]string{"file:///etc/passwd", "base64://xxx", ""})
	if collectedCount(dir) != before {
		t.Error("非 http 源不该被收藏")
	}

	// 关掉开关就不收
	cfg2 := Default()
	cfg2.StickerDir = dir
	cfg2.DisableStickerCollect = true
	b2 := New(cfg2)
	b2.collectStickers([]string{srv.URL + "/c.png"})
	if collectedCount(dir) != before {
		t.Error("关闭开关后不该继续收藏")
	}

	// 目录可读、文件非空
	entries, err := os.ReadDir(dir + "/" + collectSubdir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("收藏目录异常: %v", err)
	}
}

// 格式识别：常见图片头要认得出。
func TestStickerExt(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 0}
	jpg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	gif := []byte("GIF89a................")
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 8)...)
	cases := map[string][]byte{".png": png, ".jpg": jpg, ".gif": gif, ".webp": webp}
	for want, data := range cases {
		if got := stickerExt(data, ""); got != want {
			t.Errorf("stickerExt=%q want %q", got, want)
		}
	}
	if got := stickerExt([]byte("not an image"), "https://x/a.webp"); got != ".webp" {
		t.Errorf("URL 兜底失败: %q", got)
	}
	if got := stickerExt([]byte("nope"), ""); got != "" {
		t.Errorf("认不出应返回空: %q", got)
	}
}

// 收藏目录超总容量时按「最旧优先」淘汰，且不会把目录清空到一张不剩。
func TestEnforceStickerQuota(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, collectSubdir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// 三张 100KB 的图，修改时间依次变新
	now := time.Now()
	for i, name := range []string{"old.png", "mid.png", "new.png"} {
		p := filepath.Join(sub, name)
		if err := os.WriteFile(p, make([]byte, 100*1024), 0o644); err != nil {
			t.Fatal(err)
		}
		mod := now.Add(time.Duration(i-2) * time.Hour)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	// 上限 250KB → 必须删掉最旧那张
	enforceStickerQuota(sub, 250*1024)
	left, _ := os.ReadDir(sub)
	if len(left) != 2 {
		t.Fatalf("应剩 2 张，实际 %d 张", len(left))
	}
	if _, err := os.Stat(filepath.Join(sub, "old.png")); err == nil {
		t.Error("最旧的 old.png 应该被删掉")
	}
	if _, err := os.Stat(filepath.Join(sub, "new.png")); err != nil {
		t.Error("最新的 new.png 不该被删")
	}

	// 上限很大时不删任何东西
	enforceStickerQuota(sub, 100*1024*1024)
	left2, _ := os.ReadDir(sub)
	if len(left2) != 2 {
		t.Errorf("未超限不该删，实际剩 %d 张", len(left2))
	}
}
