package qqbot

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// admin_stats_test.go —— /admin/api/state 的三类运维统计（表情库 / 知识库 / 工具）。
//
// 统计只读、允许退化：目录不存在、权限不足都必须是 0，而不是 500——面板靠这个
// 接口渲染配置页，不能因为磁盘抖动整页挂掉。

// adminStateGET 直接打 AdminHandler，拿到解析后的 state JSON。
func adminStateGET(t *testing.T, b *Bridge) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	// 未配 admin_token 时只允许回环来源，这里显式伪装成 127.0.0.1。
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	b.AdminHandler().ServeHTTP(rec, req)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("state 不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	return rec.Code, out
}

// stateNum 取顶层对象里的数字字段（JSON 数字统一是 float64）。
func stateNum(t *testing.T, obj map[string]any, key string) float64 {
	t.Helper()
	v, ok := obj[key]
	if !ok {
		t.Fatalf("缺少字段 %q", key)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("字段 %q 不是数字: %#v", key, v)
	}
	return f
}

// stateObj 取顶层对象字段。
func stateObj(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("字段 %q 不是对象: %#v", key, m[key])
	}
	return v
}

// 造一个带真实文件的 Bridge：表情库 2 张 + 收藏 1 张，知识库 1 张改枪码卡 + 1 张普通卡。
func newStatsBridge(t *testing.T) (*Bridge, string) {
	t.Helper()
	root := t.TempDir()
	stickerDir := filepath.Join(root, "stickers")
	collectedDir := filepath.Join(stickerDir, collectSubdir)
	kbDir := filepath.Join(root, "kb")

	write := func(path string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 表情库根目录：2 张 png（100 + 50 字节）
	write(filepath.Join(stickerDir, "a.png"), bytes.Repeat([]byte{1}, 100))
	write(filepath.Join(stickerDir, "b.PNG"), bytes.Repeat([]byte{2}, 50))
	// 收藏目录：1 张 png（7 字节）
	write(filepath.Join(collectedDir, "c.png"), bytes.Repeat([]byte{3}, 7))
	// 干扰项：非图片不计、子目录不递归
	write(filepath.Join(stickerDir, "readme.txt"), bytes.Repeat([]byte{4}, 999))
	write(filepath.Join(stickerDir, "sub", "deep.png"), bytes.Repeat([]byte{5}, 500))

	// 知识库：1 张改枪码卡 + 1 张普通卡
	write(filepath.Join(kbDir, "改枪码-M700.md"),
		[]byte("keys: M700, M700改枪\n---\n改枪码：M700-CODE-BBBB\n"))
	write(filepath.Join(kbDir, "地图.md"),
		[]byte("keys: 地图\n---\n当前六张图。\n"))
	// 干扰项：非 .md 不算卡片
	write(filepath.Join(kbDir, "notes.txt"), []byte("not a card"))

	cfg := Default()
	cfg.StickerDir = stickerDir
	cfg.KBDir = kbDir
	cfg.MemoryFile = ""
	return New(cfg), root
}

// 三类统计要按真实目录算对，且不影响 state 里已有字段。
func TestAdminStateStats(t *testing.T) {
	b, _ := newStatsBridge(t)
	code, state := adminStateGET(t, b)
	if code != http.StatusOK {
		t.Fatalf("state 应返回 200，实际 %d", code)
	}

	// —— 表情库 ——
	st := stateObj(t, state, "stickers")
	if got := stateNum(t, st, "library"); got != 2 {
		t.Errorf("stickers.library=%v，期望 2（不递归、只算图片）", got)
	}
	if got := stateNum(t, st, "collected"); got != 1 {
		t.Errorf("stickers.collected=%v，期望 1", got)
	}
	if got := stateNum(t, st, "bytes"); got != 157 {
		t.Errorf("stickers.bytes=%v，期望 157（100+50+7）", got)
	}

	// —— 知识库 ——
	kb := stateObj(t, state, "kb")
	if got := stateNum(t, kb, "cards"); got != 2 {
		t.Errorf("kb.cards=%v，期望 2", got)
	}
	if got := stateNum(t, kb, "gunsmith"); got != 1 {
		t.Errorf("kb.gunsmith=%v，期望 1", got)
	}

	// —— 工具 ——
	wantTools := float64(len(b.buildTools().defs))
	if wantTools == 0 {
		t.Fatal("注册表里不该没有工具")
	}
	if got := stateNum(t, state, "tools"); got != wantTools {
		t.Errorf("tools=%v，期望 %v", got, wantTools)
	}

	// 已有字段语义不变（只做存在性/类型校验，避免绑死具体值）
	if ok, _ := state["ok"].(bool); !ok {
		t.Error("ok 字段应保持 true")
	}
	if _, ok := state["config"].(map[string]any); !ok {
		t.Error("config 字段应保持对象")
	}
	stats := stateObj(t, state, "stats")
	if _, ok := stats["events"]; !ok {
		t.Error("stats.events 应保持存在")
	}
	if _, ok := state["startedAt"].(string); !ok {
		t.Error("startedAt 应保持字符串")
	}

	// 把新增字段原样打出来（报告里贴的就是这段真实 JSON）
	frag, err := json.MarshalIndent(map[string]any{
		"stickers": state["stickers"],
		"kb":       state["kb"],
		"tools":    state["tools"],
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state 新增字段：%s", frag)
}

// tools_enabled=false 时工具数为 0（其余统计照旧）。
func TestAdminStateStatsToolsDisabled(t *testing.T) {
	b, _ := newStatsBridge(t)
	cfg := b.cfg()
	cfg.ToolsEnabled = false
	b.applyConfig(cfg)

	code, state := adminStateGET(t, b)
	if code != http.StatusOK {
		t.Fatalf("state 应返回 200，实际 %d", code)
	}
	if got := stateNum(t, state, "tools"); got != 0 {
		t.Errorf("tools_enabled=false 时 tools=%v，期望 0", got)
	}
	if got := stateNum(t, stateObj(t, state, "stickers"), "library"); got != 2 {
		t.Errorf("关工具不该影响表情统计，library=%v", got)
	}
}

// 目录不存在：三类统计全部 0，接口仍然 200（不能因为磁盘问题 500）。
func TestAdminStateStatsMissingDirs(t *testing.T) {
	root := t.TempDir()
	cfg := Default()
	cfg.StickerDir = filepath.Join(root, "no-such-stickers")
	cfg.KBDir = filepath.Join(root, "no-such-kb")
	cfg.MemoryFile = ""
	b := New(cfg)

	code, state := adminStateGET(t, b)
	if code != http.StatusOK {
		t.Fatalf("目录不存在时 state 仍应 200，实际 %d", code)
	}
	if _, bad := state["error"]; bad {
		t.Errorf("目录不存在不该返回 error: %v", state["error"])
	}
	st := stateObj(t, state, "stickers")
	for _, k := range []string{"library", "collected", "bytes"} {
		if got := stateNum(t, st, k); got != 0 {
			t.Errorf("目录不存在时 stickers.%s=%v，期望 0", k, got)
		}
	}
	kb := stateObj(t, state, "kb")
	for _, k := range []string{"cards", "gunsmith"} {
		if got := stateNum(t, kb, k); got != 0 {
			t.Errorf("目录不存在时 kb.%s=%v，期望 0", k, got)
		}
	}

	// sticker_dir 为空时也不能退化成扫当前目录
	cfg2 := Default()
	cfg2.StickerDir = ""
	cfg2.KBDir = ""
	cfg2.MemoryFile = ""
	b2 := New(cfg2)
	if lib, col, n := stickerStats(""); lib != 0 || col != 0 || n != 0 {
		t.Errorf("空目录应全 0，实际 library=%d collected=%d bytes=%d", lib, col, n)
	}
	code2, state2 := adminStateGET(t, b2)
	if code2 != http.StatusOK {
		t.Fatalf("空配置时 state 应 200，实际 %d", code2)
	}
	if got := stateNum(t, stateObj(t, state2, "stickers"), "library"); got != 0 {
		t.Errorf("空配置 stickers.library=%v，期望 0", got)
	}
}
