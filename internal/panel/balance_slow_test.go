package panel

// balance_slow_test.go 「刷新余额」不能被慢号拖死。
//
// 现场：余额 RPC 走通用 HTTP client（总超时 120s），RunBalanceRefreshNow 用
// wg.Wait() 等全部账号——某个号卡住，面板按钮就白等（实测线上"卡五秒"）。
// 现在到 balanceAllDeadline 就先回当前快照，刷新在后台继续写回池内。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/scheduler"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// slowPanel 造一个上游极慢的面板（模拟卡住的账号）。
func slowPanel(t *testing.T, delay time.Duration) *Panel {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"Accounts":[]}}`))
	}))
	t.Cleanup(srv.Close)

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "u1", AccessToken: "at", Nickname: "慢号", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	sch := scheduler.New(scheduler.Config{Pool: pl, Upstream: up})
	return New(Config{Version: "test", APIKey: "k", Pool: pl, Upstream: up, Scheduler: sch})
}

// 上游卡住时，接口必须在截止时间内返回（slow=true），不能一直等。
func TestBalanceAllReturnsBeforeDeadline(t *testing.T) {
	p := slowPanel(t, 5*time.Second) // 上游比截止时间长
	rec := httptest.NewRecorder()
	start := time.Now()
	p.balanceAll(rec, httptest.NewRequest("POST", "/panel/api/balance_all", nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if elapsed > balanceAllDeadline+2*time.Second {
		t.Fatalf("耗时 %v 超过截止时间 %v —— 慢号又把人拖住了", elapsed, balanceAllDeadline)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if slow, _ := resp["slow"].(bool); !slow {
		t.Errorf("应标记 slow=true（后台仍在刷新），实际 %v", resp)
	}
	// 后台刷新结束后单飞标记必须复位，否则下一次点击会被永久拒绝。
	deadline := time.Now().Add(balanceAllDeadline + 6*time.Second)
	for time.Now().Before(deadline) && p.balanceBusy.Load() {
		time.Sleep(50 * time.Millisecond)
	}
	if p.balanceBusy.Load() {
		t.Error("后台刷新完成后 balanceBusy 应复位")
	}
}

// 已有一轮在跑时，重复点击立刻返回 running=true，不叠加新的上游请求。
func TestBalanceAllSingleFlight(t *testing.T) {
	p := slowPanel(t, 200*time.Millisecond)
	p.balanceBusy.Store(true) // 模拟"已有一轮在跑"

	rec := httptest.NewRecorder()
	start := time.Now()
	p.balanceAll(rec, httptest.NewRequest("POST", "/panel/api/balance_all", nil))
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("单飞命中应立即返回，实际 %v", elapsed)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if running, _ := resp["running"].(bool); !running {
		t.Errorf("应返回 running=true，实际 %v", resp)
	}
}
