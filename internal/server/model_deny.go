package server

// model_deny.go 进程内「模型级黑名单」。
//
// 背景：上游会对某些模型返回 11158「model [x] is not allowed for user type [y]」。
// 这不是账号故障，也不是限流——同一类型的**所有**账号都没有这个模型的权限。
// 旧行为把它归进账号故障 → 冷却换号 → 池子被跑空 → 客户端收到
// 503 no_healthy_account（把"模型不可用"读成"号全挂了"），而且下一次还会再来一遍。
//
// 现在的处理：
//   1. 第一次遇到就把模型拉黑（带 TTL，账号类型升级后可自动恢复）；
//   2. /v1/models 不再列出它 —— 客户端（DSH / Codex 等）根本不会去选；
//   3. 仍然显式请求它时直接 400 + 明确原因，不打上游、不动账号状态。
//
// TTL 取 1 小时：账号类型变更（比如试用转正）通常不会即时发生，但也不该永久拉黑。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// modelDenyTTL 黑名单条目有效期。
const modelDenyTTL = time.Hour

type modelDenyList struct {
	mu    sync.Mutex
	until map[string]time.Time
	ttl   time.Duration
	// path 落盘位置（空 = 纯内存，测试用）。
	//
	// 为什么必须落盘：黑名单是"上游明确说过这类账号没有该模型权限"的事实，
	// 内存版一重启就清空 → /v1/models 又把不可用模型列出来，客户端再撞一次、
	// 池子再被跑空一遍。落盘后重启仍生效，过期时间一并保存。
	path string
}

func newModelDenyList(ttl time.Duration, path string) *modelDenyList {
	if ttl <= 0 {
		ttl = modelDenyTTL
	}
	d := &modelDenyList{until: map[string]time.Time{}, ttl: ttl, path: path}
	d.load()
	return d
}

// load 从磁盘恢复黑名单（忽略已过期项与损坏文件——损坏就当空表，不阻塞启动）。
func (d *modelDenyList) load() {
	if d.path == "" {
		return
	}
	raw, err := os.ReadFile(d.path)
	if err != nil {
		return
	}
	var saved map[string]time.Time
	if err := json.Unmarshal(raw, &saved); err != nil {
		log.Printf("WARN: [server] 模型黑名单文件损坏，按空表启动: %v", err)
		return
	}
	now := time.Now()
	for k, until := range saved {
		if now.Before(until) {
			d.until[modelDenyKey(k)] = until
		}
	}
}

// save 原子落盘（temp + rename）：进程在写一半时被杀也不会留下半截 JSON。
func (d *modelDenyList) save() {
	if d.path == "" {
		return
	}
	raw, err := json.Marshal(d.until)
	if err != nil {
		return
	}
	if dir := filepath.Dir(d.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("WARN: [server] 写模型黑名单失败: %v", err)
		return
	}
	if err := os.Rename(tmp, d.path); err != nil {
		log.Printf("WARN: [server] 落盘模型黑名单失败: %v", err)
	}
}

// key 归一化模型名：去掉 cn:/global: 前缀，统一小写。
//
// 拉黑按「裸模型名」记：同一个模型在 cn / global 两个域的权限通常一致，
// 真不一致时也只是多拒一次（下次请求会再拉到同一条错误），比漏放行安全。
func modelDenyKey(model string) string {
	m := strings.TrimSpace(strings.ToLower(model))
	m = strings.TrimPrefix(m, "cn:")
	m = strings.TrimPrefix(m, "global:")
	return m
}

// Deny 拉黑一个模型（含过期时间）。
func (d *modelDenyList) Deny(model string) {
	k := modelDenyKey(model)
	if k == "" {
		return
	}
	d.mu.Lock()
	d.until[k] = time.Now().Add(d.ttl)
	d.mu.Unlock()
	d.save()
}

// Denied 该模型是否处于拉黑期。
func (d *modelDenyList) Denied(model string) bool {
	k := modelDenyKey(model)
	if k == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	until, ok := d.until[k]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(d.until, k) // 顺手清过期项，避免表无界增长
		return false
	}
	return true
}

// List 当前生效的黑名单（按名排序，供面板/日志展示）。
func (d *modelDenyList) List() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	out := make([]string, 0, len(d.until))
	for k, until := range d.until {
		if now.After(until) {
			delete(d.until, k)
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
