// Package balancehist 账号积分余额的轻量时序采样。
//
// 为什么需要：面板只能看到「当前余额」，看不出「谁在掉、掉多快」。中转站式的运营
// 需要一条趋势线——尤其在「快过期积分优先消耗」的策略下，得能看出哪些号的积分在
// 被主动烧掉、哪些是躺着等过期。
//
// 形态刻意最简：JSONL 追加（每行一次采样），不引数据库、不做聚合——采样点由调用方
// 决定（复用既有的余额刷新节奏，不额外打上游）。文件按天保留，超过 maxAge 的行在
// 追加时顺带裁掉（重写文件），避免无限增长。
package balancehist

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Point 一次采样：某时刻各账号的剩余积分（uid → credits）。
type Point struct {
	T        time.Time        `json:"t"`
	Accounts map[string]int64 `json:"a"`
}

// Recorder 采样记录器。path 为空表示纯内存（测试用）。
type Recorder struct {
	mu     sync.Mutex
	path   string
	maxAge time.Duration
	// mem 仅 path=="" 时使用（测试与禁用持久化的场景）
	mem []Point
}

// Open 打开（或初始化）记录器。maxAge<=0 时默认 7 天。
func Open(path string, maxAge time.Duration) (*Recorder, error) {
	if maxAge <= 0 {
		maxAge = 7 * 24 * time.Hour
	}
	r := &Recorder{path: path, maxAge: maxAge}
	if path == "" {
		return r, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("balancehist: 创建目录: %w", err)
	}
	return r, nil
}

// Append 追加一次采样。accounts 为空时跳过（避免写入无意义的空点）。
//
// 失败只返回错误、不 panic：采样是观测行为，不该因为它让余额刷新失败。
func (r *Recorder) Append(accounts map[string]int64) error {
	if len(accounts) == 0 {
		return nil
	}
	now := time.Now()
	cp := make(map[string]int64, len(accounts))
	for k, v := range accounts {
		cp[k] = v
	}
	p := Point{T: now, Accounts: cp}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" {
		r.mem = append(r.mem, p)
		r.trimMem(now)
		return nil
	}
	line, err := json.Marshal(p)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return r.pruneLocked(now)
}

// Series 返回 since 之后的采样点（按时间升序）。
func (r *Recorder) Series(since time.Time) ([]Point, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" {
		out := make([]Point, 0, len(r.mem))
		for _, p := range r.mem {
			if !p.T.Before(since) {
				out = append(out, p)
			}
		}
		return out, nil
	}
	f, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Point
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var p Point
		if err := json.Unmarshal(line, &p); err != nil {
			continue // 坏行跳过，不影响整体趋势
		}
		if p.T.Before(since) {
			continue
		}
		out = append(out, p)
	}
	return out, sc.Err()
}

// pruneLocked 裁掉超过 maxAge 的行（重写文件）。只在追加时顺带做，代价 O(文件行数)，
// 而文件本身按天保留，行数量级很小（每 5 分钟一行 ≈ 288 行/天）。
func (r *Recorder) pruneLocked(now time.Time) error {
	cutoff := now.Add(-r.maxAge)
	pts, err := r.seriesAllLocked()
	if err != nil {
		return err
	}
	kept := pts[:0]
	for _, p := range pts {
		if !p.T.Before(cutoff) {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(pts) {
		return nil // 没有过期行，不动文件
	}
	tmp := r.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, p := range kept {
		b, err := json.Marshal(p)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func (r *Recorder) seriesAllLocked() ([]Point, error) {
	f, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Point
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var p Point
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, sc.Err()
}

func (r *Recorder) trimMem(now time.Time) {
	cutoff := now.Add(-r.maxAge)
	kept := r.mem[:0]
	for _, p := range r.mem {
		if !p.T.Before(cutoff) {
			kept = append(kept, p)
		}
	}
	r.mem = kept
}
