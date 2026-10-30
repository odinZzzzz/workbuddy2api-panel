package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// corsOriginAllowed 的匹配语义：精确、通配子域、*，以及不该放行的近似串。
func TestCorsOriginAllowed(t *testing.T) {
	cases := []struct {
		allow  []string
		origin string
		want   bool
	}{
		{nil, "https://a.example.com", false},
		{[]string{}, "https://a.example.com", false},
		{[]string{"*"}, "https://anything.test", true},
		{[]string{"https://panel.example.com"}, "https://panel.example.com", true},
		{[]string{"https://panel.example.com"}, "https://other.example.com", false},
		{[]string{"https://*.example.com"}, "https://panel.example.com", true},
		{[]string{"https://*.example.com"}, "https://a.b.example.com", true},
		{[]string{"https://*.example.com"}, "https://example.com", false},
		{[]string{"https://*.example.com"}, "https://evil-example.com", false},
		{[]string{"https://*.example.com"}, "http://panel.example.com", false},
		{[]string{"  https://panel.example.com  "}, "https://panel.example.com", true},
	}
	for _, c := range cases {
		if got := corsOriginAllowed(c.allow, c.origin); got != c.want {
			t.Errorf("corsOriginAllowed(%v, %q) = %v, want %v", c.allow, c.origin, got, c.want)
		}
	}
}

// 未配置 CORSOrigins 时行为必须与改造前一致：不写任何 CORS 头。
func TestApplyCORSUndisabled(t *testing.T) {
	p := &Panel{cfg: Config{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/panel/api/tokens", nil)
	r.Header.Set("Origin", "https://panel.example.com")
	if p.applyCORS(w, r) {
		t.Fatal("未配置白名单时不应拦截请求")
	}
	if v := w.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("未配置白名单时不应写 CORS 头，得到 %q", v)
	}
}

// 允许来源的预检请求应直接返回 204，并带齐 CORS 头。
func TestApplyCORSPreflight(t *testing.T) {
	p := &Panel{cfg: Config{CORSOrigins: []string{"https://panel.example.com"}}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/panel/api/tokens", nil)
	r.Header.Set("Origin", "https://panel.example.com")
	if !p.applyCORS(w, r) {
		t.Fatal("预检请求应被 applyCORS 完整应答")
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("预检状态码 = %d, want 204", w.Code)
	}
	h := w.Header()
	if got := h.Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := h.Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("缺少 Allow-Methods")
	}
	if got := h.Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("缺少 Allow-Headers")
	}
}

// 不在白名单的来源：不拦截、也不写 CORS 头（浏览器侧自然被拒）。
func TestApplyCORSRejectedOrigin(t *testing.T) {
	p := &Panel{cfg: Config{CORSOrigins: []string{"https://panel.example.com"}}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/panel/api/tokens", nil)
	r.Header.Set("Origin", "https://evil.example.com")
	if p.applyCORS(w, r) {
		t.Fatal("非法来源不应被当作预检处理")
	}
	if v := w.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("非法来源不应写 Allow-Origin，得到 %q", v)
	}
}

// WebDir 回退：目录不存在时必须静默返回内置副本，不能返回空内容。
func TestAssetFallsBackToEmbedded(t *testing.T) {
	p := &Panel{cfg: Config{WebDir: "Z:/definitely/not/here"}}
	embedded := []byte("embedded-copy")
	if got := string(p.asset(embedded, "app.js")); got != "embedded-copy" {
		t.Fatalf("目录缺失时应回退内置副本，得到 %q", got)
	}
	// 未配置 WebDir 同样走内置副本
	p2 := &Panel{cfg: Config{}}
	if got := string(p2.asset(embedded, "app.js")); got != "embedded-copy" {
		t.Fatalf("未配置 WebDir 时应使用内置副本，得到 %q", got)
	}
	// 非法文件名不得越出目录
	if got := string(p2.asset(embedded, "../secret")); got != "embedded-copy" {
		t.Fatalf("非法文件名应被拒绝并回退，得到 %q", got)
	}
}
