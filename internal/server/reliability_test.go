package server

// reliability_test.go 中转站可靠性三件套的回归：
//   1. handler panic 必须变成 500 JSON，而不是"连接被重置"；
//   2. 可重试错误（503/429）必须带 Retry-After，否则客户端会立刻重试放大流量；
//   3. 超大请求体必须 413，而不是整读进内存把进程 OOM 掉。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// panic 被兜住并回 500（不崩进程、不裸断连接）。
func TestServeSafelyRecoversPanic(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{"code":0}`, false })
	p := testPoolWith()
	h := NewHandler(Config{Pool: p, Upstream: up})
	h.mux = http.NewServeMux()
	h.mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := httptest.NewRecorder()
	h.serveSafely(rec, httptest.NewRequest("GET", "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500（panic 应被兜成 500，而不是断连接）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("body 应含 internal_error: %s", rec.Body.String())
	}
	// 兜底后再访问其它路由仍然正常（说明没有污染状态）
	rec2 := httptest.NewRecorder()
	h.serveSafely(rec2, httptest.NewRequest("GET", "/healthz", nil))
	if rec2.Code == 0 {
		t.Error("panic 之后服务应继续可用")
	}
}

// 503（号池无可用号）必须带 Retry-After。
func TestServiceUnavailableHasRetryAfter(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{"code":0}`, false })
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: up}) // 空池 → Pick 失败 → 503
	body := `{"model":"cn:deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("503 应带 Retry-After（否则客户端会立刻重试放大流量）")
	}
}

// 超过请求体上限 → 413，且不把整个 body 读进内存。
func TestOversizedBodyRejected(t *testing.T) {
	old := maxClientBodyBytes
	maxClientBodyBytes = 1024
	defer func() { maxClientBodyBytes = old }()

	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{"code":0}`, false })
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: up})
	huge := `{"model":"cn:deepseek-v4-flash","pad":"` + strings.Repeat("x", 4096) + `"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(huge)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code=%d want 413, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request_too_large") {
		t.Errorf("body 应含 request_too_large: %s", rec.Body.String())
	}
}

// 防止回归：Retry-After 的取值口径（503 短、429 长）。
func TestRetryAfterSeconds(t *testing.T) {
	if v := retryAfterSeconds(http.StatusServiceUnavailable); v != "5" {
		t.Errorf("503 Retry-After=%q want 5", v)
	}
	if v := retryAfterSeconds(http.StatusTooManyRequests); v != "30" {
		t.Errorf("429 Retry-After=%q want 30", v)
	}
	_ = time.Second
}
