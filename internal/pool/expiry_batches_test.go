package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestNormalizeBatchesKeepsFarOnlyBatch 只有 200 天后长期包的账号不能被裁成
// 「无未来到期批次」——那是假信息。裁剪口径是「最早一批必留 + 未来 30 天其余」。
func TestNormalizeBatchesKeepsFarOnlyBatch(t *testing.T) {
	now := time.Now()
	far := now.Add(200 * 24 * time.Hour)
	got := normalizeBatches([]CreditBatch{{At: far, Remain: 500}}, 500, now)
	if len(got) != 1 || !got[0].At.Equal(far) || got[0].Remain != 500 {
		t.Fatalf("far-only batch dropped: %+v", got)
	}
}

// TestNormalizeBatchesHorizonAndLimit 30 天窗口外的后续批次被裁掉、总数封顶
// creditBatchLimit，且输入里的已过期/零余额批次被剔除。
func TestNormalizeBatchesHorizonAndLimit(t *testing.T) {
	now := time.Now()
	in := []CreditBatch{{At: now.Add(-time.Hour), Remain: 9}}
	for i := 0; i < 15; i++ {
		in = append(in, CreditBatch{At: now.Add(time.Duration(i+1) * 24 * time.Hour), Remain: 10})
	}
	in = append(in, CreditBatch{At: now.Add(200 * 24 * time.Hour), Remain: 77})
	got := normalizeBatches(in, 1000, now)
	if len(got) != creditBatchLimit {
		t.Fatalf("len=%d want %d（30 天窗口 + 上限）", len(got), creditBatchLimit)
	}
	if !got[0].At.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("first batch=%v want 最早的未来批次（已过期必须剔除）", got[0].At)
	}
	for _, b := range got {
		if b.At.After(now.Add(creditBatchHorizon)) {
			t.Fatalf("批次 %v 超出 %v 窗口却仍保留", b.At, creditBatchHorizon)
		}
	}
}

// TestNormalizeBatchesClampsToCredits 累计钳到总余额：上游重复记录不能把同一笔
// 积分算成两批。
func TestNormalizeBatchesClampsToCredits(t *testing.T) {
	now := time.Now()
	in := []CreditBatch{
		{At: now.Add(time.Hour), Remain: 80},
		{At: now.Add(2 * time.Hour), Remain: 80},
	}
	got := normalizeBatches(in, 100, now)
	var sum int64
	for _, b := range got {
		sum += b.Remain
	}
	if sum != 100 {
		t.Fatalf("sum=%d want 100（钳到 credits）", sum)
	}
}

// TestStatusBatchesJSONShape 钉住 /overview 的线格式：面板按 credits_batches[].at
// （RFC3339）与 .remain 解析，键名或形态一变前端就静默退化成「无未来到期批次」。
func TestStatusBatchesJSONShape(t *testing.T) {
	now := time.Now()
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCreditsDetailed("u1", CreditsSnapshot{
		Remain:  100,
		Batches: []CreditBatch{{At: now.Add(time.Hour), Remain: 40}},
	})
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("u1 missing")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"credits_batches\":[{\"at\":\"") {
		t.Fatalf("credits_batches 线格式不符: %s", raw)
	}
	if !strings.Contains(string(raw), "\"remain\":40") {
		t.Fatalf("批次剩余量未透出: %s", raw)
	}
}

// TestLoadDropsLegacyExpirySchema 旧 schema 的到期快照必须整体丢弃：
// version<2 的 credits_earliest_* 是 CycleEndTime 口径，与展示口径混用会让卡片与
// 明细互相矛盾。空窗期显示「无未来到期批次」比显示错的好。
func TestLoadDropsLegacyExpirySchema(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	raw := `{"accounts":{"u1":{"credits":100,"credits_expiring":50,` +
		`"credits_earliest_expiry":"2030-01-01T00:00:00Z","credits_earliest_remaining":40,` +
		`"credits_batches":[{"at":"2030-01-01T00:00:00Z","remain":40}]}}}`
	if err := os.WriteFile(fp, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New(fp)
	defer p.Close()
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("u1 未加载")
	}
	if st.Credits != 100 {
		t.Fatalf("credits=%d want 100（余额不受 schema 影响）", st.Credits)
	}
	if st.CreditsExpiring != 0 || !st.CreditsEarliestExpiry.IsZero() || st.CreditsEarliestRemaining != 0 || len(st.CreditsBatches) != 0 {
		t.Fatalf("旧 schema 到期快照未丢弃: %+v", st)
	}
}

// TestLoadKeepsCurrentExpirySchema schema=2 的快照照常恢复（含逐批展示口径）。
func TestLoadKeepsCurrentExpirySchema(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "state.json")
	raw := `{"expiry_schema":2,"accounts":{"u1":{"credits":100,"credits_expiring":50,` +
		`"credits_earliest_expiry":"2030-01-01T00:00:00Z","credits_earliest_remaining":40,` +
		`"credits_batches":[{"at":"2030-01-01T00:00:00Z","remain":40}]}}}`
	if err := os.WriteFile(fp, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New(fp)
	defer p.Close()
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("u1 未加载")
	}
	if st.CreditsExpiring != 50 || st.CreditsEarliestRemaining != 40 || st.CreditsEarliestExpiry.IsZero() {
		t.Fatalf("路由口径快照未恢复: %+v", st)
	}
	if len(st.CreditsBatches) != 1 || st.CreditsBatches[0].Remain != 40 {
		t.Fatalf("展示口径批次未恢复: %+v", st.CreditsBatches)
	}
}
