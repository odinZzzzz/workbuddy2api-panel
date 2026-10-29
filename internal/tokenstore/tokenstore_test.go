package tokenstore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCreateAndAuthenticate(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tok, err := s.Create("客户端 A", 100, time.Time{}, nil, "备注")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tok.Key == "" || tok.Key[:3] != "sk-" {
		t.Fatalf("key 形态异常: %q", tok.Key)
	}
	if !tok.Enabled || tok.QuotaCredits != 100 {
		t.Fatalf("字段异常: %+v", tok)
	}

	got, err := s.Authenticate(tok.Key)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != tok.ID {
		t.Fatalf("命中错误令牌: %+v", got)
	}

	if _, err := s.Authenticate("sk-nope"); err != ErrNotFound {
		t.Fatalf("未知 key 应返回 ErrNotFound，得到 %v", err)
	}
	if _, err := s.Authenticate(""); err != ErrNotFound {
		t.Fatalf("空 key 应返回 ErrNotFound，得到 %v", err)
	}
}

func TestAuthenticateRejections(t *testing.T) {
	s, _ := Open("")

	// 禁用
	disabled, _ := s.Create("disabled", 0, time.Time{}, nil, "")
	s.SetEnabled(disabled.ID, false)
	if _, err := s.Authenticate(disabled.Key); err != ErrDisabled {
		t.Fatalf("禁用令牌应 ErrDisabled，得到 %v", err)
	}

	// 过期
	expired, _ := s.Create("expired", 0, time.Now().Add(-time.Minute), nil, "")
	if _, err := s.Authenticate(expired.Key); err != ErrExpired {
		t.Fatalf("过期令牌应 ErrExpired，得到 %v", err)
	}

	// 配额用尽
	quota, _ := s.Create("quota", 1, time.Time{}, nil, "")
	s.AddUsage(quota.ID, 1.5)
	if _, err := s.Authenticate(quota.Key); err != ErrQuotaExceeded {
		t.Fatalf("配额用尽应 ErrQuotaExceeded，得到 %v", err)
	}
	// 重置后可再用
	s.ResetUsage(quota.ID)
	if _, err := s.Authenticate(quota.Key); err != nil {
		t.Fatalf("重置用量后应可用，得到 %v", err)
	}
}

func TestAllowedModels(t *testing.T) {
	tok := &Token{AllowedModels: []string{"deepseek-v4-flash"}}
	cases := map[string]bool{
		"deepseek-v4-flash":    true,
		"cn:deepseek-v4-flash": true, // 带域前缀也认（按裸名匹配）
		"global:glm-5.3":       false,
	}
	for m, want := range cases {
		if got := tok.AllowsModel(m); got != want {
			t.Errorf("AllowsModel(%q)=%v want %v", m, got, want)
		}
	}
	// 空名单 = 全放行
	all := &Token{}
	if !all.AllowsModel("任意模型") {
		t.Error("空名单应放行所有模型")
	}
}

func TestUsageAccountingAndPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tok, _ := s.Create("持久化", 10, time.Time{}, nil, "")
	s.AddUsage(tok.ID, 0.25)
	s.AddUsage(tok.ID, 0.5)

	// 重新加载应看到累计值（验证原子落盘）
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	list := s2.List()
	if len(list) != 1 {
		t.Fatalf("令牌数=%d want 1", len(list))
	}
	if list[0].UsedRequests != 2 {
		t.Errorf("UsedRequests=%d want 2", list[0].UsedRequests)
	}
	if d := list[0].UsedCredits - 0.75; d > 1e-9 || d < -1e-9 {
		t.Errorf("UsedCredits=%v want 0.75", list[0].UsedCredits)
	}

	// 文件权限 0600
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows 的 NTFS 只有只读位，Go 会把 0600 映射成 0666，属预期；只在 Unix 断言。
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("tokens.json 权限=%o want 600", perm)
		}
	}
}

func TestDeleteAndMask(t *testing.T) {
	s, _ := Open("")
	tok, _ := s.Create("待删", 0, time.Time{}, nil, "")
	if !s.Delete(tok.ID) {
		t.Fatal("Delete 应返回 true")
	}
	if s.Delete(tok.ID) {
		t.Fatal("重复 Delete 应返回 false")
	}
	if _, err := s.Authenticate(tok.Key); err != ErrNotFound {
		t.Fatalf("删除后不应命中，得到 %v", err)
	}

	if got := MaskedKey("sk-0123456789abcdefghij"); got != "sk-01234…ghij" {
		t.Errorf("MaskedKey=%q", got)
	}
	if got := MaskedKey("short"); got != "short" {
		t.Errorf("短 key 不应打码，得到 %q", got)
	}
}
