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
}

func newModelDenyList(ttl time.Duration) *modelDenyList {
	if ttl <= 0 {
		ttl = modelDenyTTL
	}
	return &modelDenyList{until: map[string]time.Time{}, ttl: ttl}
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
