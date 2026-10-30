package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestResolveModelPlan 跨域路由（auto:）的候选域序列解析。
//
// 契约：cn:/global: 单域；auto: 按优先级（默认 global→cn）；裸名与未知前缀维持
// cn 单域——历史语义零回归（现有客户端全部走裸名，不能因为新增 auto 改变它们）。
func TestResolveModelPlan(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		priority []string
		want     []string
		wantBare string
	}{
		{"裸名", "glm-5.2", nil, []string{"cn"}, "glm-5.2"},
		{"cn 前缀", "cn:glm-5.2", nil, []string{"cn"}, "glm-5.2"},
		{"global 前缀", "global:glm-5.2", nil, []string{"global"}, "glm-5.2"},
		{"auto 默认优先级", "auto:deepseek-v4.1-flash", nil, []string{"global", "cn"}, "deepseek-v4.1-flash"},
		{"auto 自定义优先级", "auto:x", []string{"cn", "global"}, []string{"cn", "global"}, "x"},
		{"auto 非法值被丢弃 + 大小写不敏感", "auto:x", []string{"bogus", "CN"}, []string{"cn"}, "x"},
		{"auto 去重", "auto:x", []string{"global", "global"}, []string{"global"}, "x"},
		{"auto 全非法回落默认", "auto:x", []string{"bogus"}, []string{"global", "cn"}, "x"},
		{"未知前缀按裸名处理", "weird:x", nil, []string{"cn"}, "weird:x"},
		{"auto 空裸名", "auto:", nil, []string{"global", "cn"}, ""},
	}
	for _, c := range cases {
		got, bare := resolveModelPlan(c.model, c.priority)
		if len(got) != len(c.want) {
			t.Errorf("%s: realms=%v want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: realms=%v want %v", c.name, got, c.want)
				break
			}
		}
		if bare != c.wantBare {
			t.Errorf("%s: bare=%q want %q", c.name, bare, c.wantBare)
		}
	}
}

func TestIsAutoModel(t *testing.T) {
	if !isAutoModel("auto:glm-5.2") {
		t.Error("auto:glm-5.2 应判为跨域")
	}
	for _, m := range []string{"glm-5.2", "cn:glm-5.2", "global:glm-5.2", "autox:glm-5.2", "auto"} {
		if isAutoModel(m) {
			t.Errorf("%q 不应判为跨域", m)
		}
	}
}

// TestChatAutoRealmPrefersGlobal 两个域都健康时，auto: 必须落在第一个候选域（global）。
func TestChatAutoRealmPrefersGlobal(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999},
		&auth.Auth{UID: "gl1", AccessToken: "at-gl", ExpiresAt: 9999999999, Domain: "user.workbuddy.ai"},
	)
	var used string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		used = authz
		return 200, sseOK, true
	})
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute, RealmPriority: []string{"global", "cn"}})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"auto:glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if used != "Bearer at-gl" {
		t.Fatalf("auto 应优先用 global 号，实际用了 %q", used)
	}
}

// TestChatAutoRealmFallsBackToCN 第一个候选域整域不可用（禁用/冷却/无额度）时，
// auto: 必须退到下一个域，而不是 503——这正是跨域路由存在的理由。
func TestChatAutoRealmFallsBackToCN(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999},
		&auth.Auth{UID: "gl1", AccessToken: "at-gl", ExpiresAt: 9999999999, Domain: "user.workbuddy.ai"},
	)
	p.Disable("gl1", "test: global 整域不可用")
	var used string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		used = authz
		return 200, sseOK, true
	})
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute, RealmPriority: []string{"global", "cn"}})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"auto:glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("global 不可用时应回退 cn，得到 code=%d body=%s", rec.Code, rec.Body)
	}
	if used != "Bearer at-cn" {
		t.Fatalf("应回退到 cn 号，实际用了 %q", used)
	}
}

// TestChatAutoRealmBothUnavailable 两个域都不可用 → 503（且不是 panic/空响应）。
func TestChatAutoRealmBothUnavailable(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "cn1", AccessToken: "at-cn", ExpiresAt: 9999999999},
		&auth.Auth{UID: "gl1", AccessToken: "at-gl", ExpiresAt: 9999999999, Domain: "user.workbuddy.ai"},
	)
	p.Disable("gl1", "test")
	p.Disable("cn1", "test")
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute, RealmPriority: []string{"global", "cn"}})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"auto:glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("两域都不可用应 503，得到 %d body=%s", rec.Code, rec.Body)
	}
}
