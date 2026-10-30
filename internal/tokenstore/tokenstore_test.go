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
	tok, err := s.Create("客户端 A", 100, time.Time{}, nil, "备注", 0, 0)
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
	disabled, _ := s.Create("disabled", 0, time.Time{}, nil, "", 0, 0)
	s.SetEnabled(disabled.ID, false)
	if _, err := s.Authenticate(disabled.Key); err != ErrDisabled {
		t.Fatalf("禁用令牌应 ErrDisabled，得到 %v", err)
	}

	// 过期
	expired, _ := s.Create("expired", 0, time.Now().Add(-time.Minute), nil, "", 0, 0)
	if _, err := s.Authenticate(expired.Key); err != ErrExpired {
		t.Fatalf("过期令牌应 ErrExpired，得到 %v", err)
	}

	// 配额用尽
	quota, _ := s.Create("quota", 1, time.Time{}, nil, "", 0, 0)
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
	tok, _ := s.Create("持久化", 10, time.Time{}, nil, "", 0, 0)
	s.AddUsage(tok.ID, 0.25)
	s.AddUsage(tok.ID, 0.5)
	// 用量是合并落盘的（每请求不同步写），这里显式 Flush 后再验证持久化。
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

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
	tok, _ := s.Create("待删", 0, time.Time{}, nil, "", 0, 0)
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

// rpm 限流：窗口内超过上限应拒绝，且被拒的请求不计入窗口（否则重试风暴自锁）。
func TestAcquireRPM(t *testing.T) {
	s, _ := Open("")
	tok, _ := s.Create("rpm", 0, time.Time{}, nil, "", 3, 0)

	for i := 0; i < 3; i++ {
		release, err := s.Acquire(tok.ID)
		if err != nil {
			t.Fatalf("第 %d 次应放行，得到 %v", i+1, err)
		}
		release()
	}
	if _, err := s.Acquire(tok.ID); err != ErrRPMExceeded {
		t.Fatalf("超过 rpm 应 ErrRPMExceeded，得到 %v", err)
	}
	// 被拒的请求不该消耗配额：再试仍是 ErrRPMExceeded 而不是别的
	if _, err := s.Acquire(tok.ID); err != ErrRPMExceeded {
		t.Fatalf("连续被拒应稳定返回 ErrRPMExceeded，得到 %v", err)
	}
}

// 并发上限：未 release 前超过上限应拒绝；release 后恢复。
func TestAcquireConcurrency(t *testing.T) {
	s, _ := Open("")
	tok, _ := s.Create("conc", 0, time.Time{}, nil, "", 0, 2)

	r1, err1 := s.Acquire(tok.ID)
	r2, err2 := s.Acquire(tok.ID)
	if err1 != nil || err2 != nil {
		t.Fatalf("前两个应放行: %v %v", err1, err2)
	}
	if got := s.InFlight(tok.ID); got != 2 {
		t.Fatalf("在途=%d want 2", got)
	}
	if _, err := s.Acquire(tok.ID); err != ErrConcurrencyExceeded {
		t.Fatalf("超过并发上限应 ErrConcurrencyExceeded，得到 %v", err)
	}
	r1()
	if got := s.InFlight(tok.ID); got != 1 {
		t.Fatalf("release 后在途=%d want 1", got)
	}
	r3, err3 := s.Acquire(tok.ID)
	if err3 != nil {
		t.Fatalf("release 后应可再放行，得到 %v", err3)
	}
	r2()
	r3()
	if got := s.InFlight(tok.ID); got != 0 {
		t.Fatalf("全部 release 后在途=%d want 0", got)
	}
	// release 幂等：重复调用不应把计数打成负数
	r1()
	if got := s.InFlight(tok.ID); got != 0 {
		t.Fatalf("重复 release 后仍在途=%d", got)
	}
}

// 未配置限流的令牌走零开销路径（不限流）。
func TestAcquireUnlimited(t *testing.T) {
	s, _ := Open("")
	tok, _ := s.Create("free", 0, time.Time{}, nil, "", 0, 0)
	for i := 0; i < 50; i++ {
		release, err := s.Acquire(tok.ID)
		if err != nil {
			t.Fatalf("不限流令牌不应被拒: %v", err)
		}
		release()
	}
}

// SetLimits 可运行期改限流；负数钳到 0（= 不限）。
func TestSetLimits(t *testing.T) {
	s, _ := Open("")
	tok, _ := s.Create("t", 0, time.Time{}, nil, "", 0, 0)
	got, ok := s.SetLimits(tok.ID, 5, -3)
	if !ok {
		t.Fatal("SetLimits 应返回 ok")
	}
	if got.RPM != 5 || got.MaxConcurrency != 0 {
		t.Fatalf("limits=%+v want rpm=5 conc=0", got)
	}
}

// 用量记账必须**合并落盘**：N 次 AddUsage 只产生 1 次写盘（外加建令牌那一次）。
//
// 动机：AddUsage 是每请求都调的，早期实现每请求全量重写 tokens.json（marshal +
// rename + 持锁），把请求吞吐绑在磁盘 I/O 上。这里用 SaveCount 把"合并真的生效"
// 变成可断言的数字，避免以后又被人改回同步写。
func TestUsageWritesAreCoalesced(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWithFlush(dir+"/tokens.json", time.Hour) // 长间隔：只靠显式 Flush
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	tok, _ := s.Create("c", 0, time.Time{}, nil, "", 0, 0)
	base := s.SaveCount() // 建令牌 = 1 次同步落盘

	for i := 0; i < 50; i++ {
		s.AddUsage(tok.ID, 0.01)
	}
	if got := s.SaveCount(); got != base {
		t.Fatalf("50 次 AddUsage 不应触发落盘：saves=%d want %d", got, base)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := s.SaveCount(); got != base+1 {
		t.Fatalf("Flush 应只写一次：saves=%d want %d", got, base+1)
	}
	// 再 Flush 一次：没有脏数据，不该写盘
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush2: %v", err)
	}
	if got := s.SaveCount(); got != base+1 {
		t.Fatalf("无脏数据时 Flush 不应写盘：saves=%d want %d", got, base+1)
	}

	// 落盘内容包含累计用量
	re, err := OpenWithFlush(dir+"/tokens.json", 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	list := re.List()
	if len(list) != 1 || list[0].UsedRequests != 50 {
		t.Fatalf("重开后用量不对: %+v", list)
	}
}

// 结构变更（建/删/启停/改限流/重置）必须**立刻**持久，不能等合并窗口。
func TestStructuralChangesPersistImmediately(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tokens.json"
	s, err := OpenWithFlush(path, time.Hour)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	tok, _ := s.Create("立刻持久", 0, time.Time{}, nil, "", 0, 0)
	// 不 Flush 直接重开：建令牌必须已经在盘上
	re, err := OpenWithFlush(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(re.List()) != 1 {
		t.Fatalf("建令牌应立即落盘，重开后令牌数=%d", len(re.List()))
	}

	s.SetEnabled(tok.ID, false)
	re2, _ := OpenWithFlush(path, 0)
	if got := re2.List()[0]; got.Enabled {
		t.Fatalf("禁用应立即落盘，重开后仍 enabled")
	}

	s.SetLimits(tok.ID, 120, 8)
	re3, _ := OpenWithFlush(path, 0)
	if got := re3.List()[0]; got.RPM != 120 || got.MaxConcurrency != 8 {
		t.Fatalf("改限流应立即落盘，重开后=%+v", got)
	}
}

// Close 应停止后台 flusher 并做最后一次落盘（优雅退出的兜底路径）。
func TestCloseFlushesAndStops(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tokens.json"
	s, err := OpenWithFlush(path, time.Hour)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tok, _ := s.Create("x", 0, time.Time{}, nil, "", 0, 0)
	s.AddUsage(tok.ID, 1.5)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	re, _ := OpenWithFlush(path, 0)
	if got := re.List()[0]; got.UsedRequests != 1 || got.UsedCredits != 1.5 {
		t.Fatalf("Close 应落盘用量，重开后=%+v", got)
	}
	// 重复 Close 不应 panic（stopOnce 保护）
	if err := s.Close(); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
}
