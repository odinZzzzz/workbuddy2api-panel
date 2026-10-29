package balancehist

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndSeries(t *testing.T) {
	r, err := Open("", 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Append(map[string]int64{"u1": 100, "u2": 50}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := r.Append(map[string]int64{"u1": 90, "u2": 50}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// 空快照应被跳过
	if err := r.Append(nil); err != nil {
		t.Fatalf("Append(nil): %v", err)
	}

	pts, err := r.Series(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("采样点=%d want 2（空快照不该落盘）", len(pts))
	}
	if pts[0].Accounts["u1"] != 100 || pts[1].Accounts["u1"] != 90 {
		t.Fatalf("采样值不对: %+v", pts)
	}
}

func TestPersistenceAndSinceFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "balance-history.jsonl")
	r, err := Open(path, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Append(map[string]int64{"u1": 42}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// 重新打开应读到同一份数据（验证真的落盘了）
	r2, err := Open(path, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	pts, err := r2.Series(time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 1 || pts[0].Accounts["u1"] != 42 {
		t.Fatalf("重开后数据不对: %+v", pts)
	}

	// 未来时间点过滤应返回空
	future, err := r2.Series(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Series(future): %v", err)
	}
	if len(future) != 0 {
		t.Fatalf("未来起点应无数据，得到 %d", len(future))
	}
}

func TestPruneDropsExpiredPoints(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "balance-history.jsonl")
	// maxAge 设很小，先写一条「旧」记录，再写一条新记录触发裁剪
	r, err := Open(path, time.Millisecond)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Append(map[string]int64{"old": 1}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := r.Append(map[string]int64{"new": 2}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	pts, err := r.Series(time.Time{})
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("裁剪后应只剩 1 个点，得到 %d: %+v", len(pts), pts)
	}
	if _, ok := pts[0].Accounts["new"]; !ok {
		t.Fatalf("保留的应是新点: %+v", pts[0])
	}
}

func TestSeriesOnMissingFile(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "nope.jsonl"), 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pts, err := r.Series(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Series 不应因文件缺失报错: %v", err)
	}
	if len(pts) != 0 {
		t.Fatalf("缺文件应返回空，得到 %d", len(pts))
	}
}
