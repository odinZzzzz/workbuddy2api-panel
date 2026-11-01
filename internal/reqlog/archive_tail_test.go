package reqlog

// archive_tail_test.go 「最近 N 条」倒序尾读的正确性。
//
// 现场：面板请求日志 600 条要 10.6s —— 原实现把 retention 期内**所有**归档文件
// 整读 + 每条 JSON 反序列化 + 全量排序再截断。改成按日期倒序、文件内从尾往前扫、
// 凑够 limit 就停。这两个测试分别盯住两件事：
//   1. 跨块边界的行不能丢、不能重（反序分块读取最容易踩的坑）；
//   2. 按日期倒序时，够数就停，且返回的确实是最新的那批。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 强制极小块大小，制造大量跨块边界。
func TestReadArchiveSurvivesChunkBoundaries(t *testing.T) {
	old := archiveTailChunk
	archiveTailChunk = 512 // 每条事件约 200B → 每块切 2~3 行，边界密集
	defer func() { archiveTailChunk = old }()

	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 1 << 24, RetentionDays: 7})
	base := time.Now().Add(-time.Hour)
	const n = 500
	for i := 0; i < n; i++ {
		r.Record(Event{
			Time:      base.Add(time.Duration(i) * time.Millisecond),
			RequestID: fmt.Sprintf("ev-%03d", i),
			Model:     "m",
			Status:    200,
			OK:        true,
			Outcome:   OutcomeSuccess,
		})
	}
	r.Close()

	const limit = 200
	rows, err := r.ReadArchive(limit, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != limit {
		t.Fatalf("拿到 %d 条，期望 %d（跨块边界丢了记录）", len(rows), limit)
	}
	// 必须正好是最新的 limit 条，且严格倒序、无重复。
	for i, e := range rows {
		want := fmt.Sprintf("ev-%03d", n-1-i)
		if e.RequestID != want {
			t.Fatalf("第 %d 条 = %s，期望 %s（顺序错或丢/重）", i, e.RequestID, want)
		}
	}
}

// 多天归档：凑够 limit 就停，且只取最新的那天。
func TestReadArchiveStopsAtNewestDay(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Hour)
	// 三天各 100 条，文件名按日期分组（与生产一致）。
	days := []string{}
	for d := 0; d < 3; d++ {
		day := base.Add(time.Duration(d) * 24 * time.Hour)
		days = append(days, day.Format("2006-01-02"))
		var sb []byte
		for i := 0; i < 100; i++ {
			sb = append(sb, []byte(fmt.Sprintf(
				`{"time":"%s","request_id":"d%d-%03d","model":"m","status":200,"ok":true,"outcome":"success"}`+"\n",
				day.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano), d, i))...)
		}
		name := filepath.Join(dir, "requests-"+day.Format("2006-01-02")+".jsonl")
		if err := os.WriteFile(name, sb, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 1 << 24, RetentionDays: 7})
	rows, err := r.ReadArchive(50, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 50 {
		t.Fatalf("拿到 %d 条，期望 50", len(rows))
	}
	for _, e := range rows {
		if want := "d2-"; len(e.RequestID) < 3 || e.RequestID[:3] != want {
			t.Fatalf("返回了旧数据 %s（应只取最新一天，凑够就停）", e.RequestID)
		}
	}
	if rows[0].RequestID != "d2-099" {
		t.Errorf("首条应是最新一条 d2-099，实际 %s", rows[0].RequestID)
	}
}
