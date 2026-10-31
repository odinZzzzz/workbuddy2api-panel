package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 黑名单基本语义：拉黑、命中、过期、列表。
func TestModelDenyListBasics(t *testing.T) {
	d := newModelDenyList(time.Hour, "")
	if d.Denied("gemini-3.5-flash") {
		t.Fatal("初始不该命中")
	}
	d.Deny("cn:gemini-3.5-flash") // 带域前缀也要归一化
	if !d.Denied("gemini-3.5-flash") {
		t.Fatal("拉黑后应命中（跨域前缀归一化）")
	}
	if !d.Denied("CN:GEMINI-3.5-FLASH") {
		t.Error("大小写与前缀都应归一化")
	}
	if got := d.List(); len(got) != 1 || got[0] != "gemini-3.5-flash" {
		t.Errorf("List=%v", got)
	}
	if d.Denied("") {
		t.Error("空模型名不该命中")
	}
	d.Deny("") // 不该 panic / 不该写入空键
	if len(d.List()) != 1 {
		t.Errorf("空键不该入表: %v", d.List())
	}

	// 过期后自动失效
	short := newModelDenyList(30*time.Millisecond, "")
	short.Deny("x-model")
	if !short.Denied("x-model") {
		t.Fatal("刚拉黑应命中")
	}
	time.Sleep(45 * time.Millisecond)
	if short.Denied("x-model") {
		t.Error("过期后不该命中")
	}
	if len(short.List()) != 0 {
		t.Errorf("过期项应从表里清掉: %v", short.List())
	}
}

// 被拉黑的模型不再出现在 /v1/models 里（这就是"过滤不支持的模型"）。
func TestModelListFiltersDeniedModel(t *testing.T) {
	dynamicModelsCache.Lock()
	dynamicModelsCache.ids = nil
	dynamicModelsCache.fetched = time.Time{}
	dynamicModelsCache.lastFail = time.Time{}
	dynamicModelsCache.Unlock()

	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, `{"code":0,"data":{"models":[{"id":"good-model"},{"id":"gemini-3.5-flash"}]}}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	h.modelDeny.Deny("gemini-3.5-flash")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	found, denied := false, false
	for _, m := range resp.Data {
		if m.ID == "cn:good-model" {
			found = true
		}
		if strings.Contains(m.ID, "gemini-3.5-flash") {
			denied = true
		}
	}
	if !found {
		t.Error("正常模型不应被过滤")
	}
	if denied {
		t.Error("被拉黑的模型仍出现在 /v1/models 里")
	}
}

// 请求被拉黑的模型：直接 400 model_not_allowed，且完全不碰上游（不打号、不换号）。
func TestChatDeniedModelFailsFast(t *testing.T) {
	var calls int32
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return 200, `{"code":0,"data":{"choices":[{"message":{"content":"hi"}}]}}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	h.modelDeny.Deny("gemini-3.5-flash")

	body := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model_not_allowed") {
		t.Errorf("body 应含 model_not_allowed: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "no_healthy_account") {
		t.Error("不该再退化成 no_healthy_account")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("不该打上游，实际调用 %d 次", n)
	}

	// 未被拉黑的模型仍然正常放行（对照）
	okBody := `{"model":"good-model","messages":[{"role":"user","content":"hi"}],"stream":false}`
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(okBody)))
	if rec2.Code == http.StatusBadRequest && strings.Contains(rec2.Body.String(), "model_not_allowed") {
		t.Error("正常模型不该被拦")
	}
}

// 黑名单必须能跨重启：否则每次发版后 /v1/models 又把不可用模型列出来，
// 客户端再撞一次、号池再被跑空一遍。
func TestModelDenyPersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/model_deny.json"

	// 第一次运行：拉黑然后"进程退出"
	first := newModelDenyList(time.Hour, path)
	first.Deny("global:gemini-3.5-flash")
	if !first.Denied("gemini-3.5-flash") {
		t.Fatal("同进程内应命中")
	}

	// 第二次运行：新实例从磁盘恢复
	second := newModelDenyList(time.Hour, path)
	if !second.Denied("gemini-3.5-flash") {
		t.Fatal("重启后应仍然命中（否则 /v1/models 会重新列出不可用模型）")
	}
	if got := second.List(); len(got) != 1 {
		t.Errorf("List=%v want 1 条", got)
	}

	// 过期项不该被恢复
	third := newModelDenyList(time.Millisecond, path)
	third.Deny("x-model")
	time.Sleep(20 * time.Millisecond)
	fourth := newModelDenyList(time.Hour, path)
	if fourth.Denied("x-model") {
		t.Error("已过期条目不该在重启后恢复")
	}

	// 文件损坏时按空表启动，不 panic、不阻塞
	bad := t.TempDir() + "/bad.json"
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if newModelDenyList(time.Hour, bad).Denied("anything") {
		t.Error("损坏文件应按空表处理")
	}
}
