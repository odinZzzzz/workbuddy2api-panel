package tokenstore

import (
	"fmt"
	"sync"
	"time"
)

// limiter.go —— 令牌级限流（每分钟请求数 + 并发上限）。
//
// 为什么需要：账号池是所有令牌共享的。一个客户端把并发打满，其他客户端的请求就会
// 因为「所有账号在途占满」被 503——看起来像网关挂了，实际是邻居把池子占满了。
// 中转站的做法是给每把 key 独立的 rpm 与并发闸门，超额回 429 + Retry-After。
//
// 实现取向：进程内滑动窗口 + 计数，不做分布式（本网关是单实例部署；多实例需要
// 共享限流器，届时换成 Redis 计数即可，接口形状不变）。
//
// 语义细节：
//   - RPM=0 / MaxConcurrency=0 表示不限（与配额字段同风格：0 即关闭）。
//   - 只有**被受理**的请求才计入 rpm 窗口；被拒的不计（否则重试风暴会把自己锁死）。
//   - 并发计数在请求结束时释放（调用方必须 defer release）。

// 限流拒绝原因（调用方据此回 429 + Retry-After）。
var (
	ErrRPMExceeded         = fmt.Errorf("token rpm exceeded")
	ErrConcurrencyExceeded = fmt.Errorf("token concurrency exceeded")
)

// limiter 单个令牌的限流状态。
type limiter struct {
	mu       sync.Mutex
	hits     []time.Time // 最近 60s 内被受理的请求时刻（滑动窗口）
	inFlight int
}

// Acquire 尝试为令牌放行一次请求。
//
// 成功时返回 release（调用方必须 defer 调用，用于释放并发计数），失败时返回
// ErrRPMExceeded / ErrConcurrencyExceeded。
func (s *Store) Acquire(id string) (func(), error) {
	s.mu.Lock()
	t := s.byID[id]
	if t == nil {
		s.mu.Unlock()
		return func() {}, nil // 令牌已删除：交给上层鉴权处理，这里不额外拦
	}
	rpm := t.RPM
	maxConc := t.MaxConcurrency
	lim := s.limiters[id]
	if lim == nil {
		lim = &limiter{}
		if s.limiters == nil {
			s.limiters = map[string]*limiter{}
		}
		s.limiters[id] = lim
	}
	s.mu.Unlock()

	if rpm <= 0 && maxConc <= 0 {
		return func() {}, nil // 未配置限流：零开销路径
	}

	lim.mu.Lock()
	defer lim.mu.Unlock()
	now := time.Now()
	if rpm > 0 {
		cutoff := now.Add(-time.Minute)
		kept := lim.hits[:0]
		for _, h := range lim.hits {
			if h.After(cutoff) {
				kept = append(kept, h)
			}
		}
		lim.hits = kept
		if len(lim.hits) >= rpm {
			return func() {}, ErrRPMExceeded
		}
	}
	if maxConc > 0 && lim.inFlight >= maxConc {
		return func() {}, ErrConcurrencyExceeded
	}
	if rpm > 0 {
		lim.hits = append(lim.hits, now)
	}
	lim.inFlight++
	var once sync.Once
	return func() {
		once.Do(func() {
			lim.mu.Lock()
			if lim.inFlight > 0 {
				lim.inFlight--
			}
			lim.mu.Unlock()
		})
	}, nil
}

// InFlight 返回令牌当前在途请求数（面板展示用）。
func (s *Store) InFlight(id string) int {
	s.mu.Lock()
	lim := s.limiters[id]
	s.mu.Unlock()
	if lim == nil {
		return 0
	}
	lim.mu.Lock()
	defer lim.mu.Unlock()
	return lim.inFlight
}

// SetLimits 更新令牌限流配置（rpm / 并发上限；0 = 不限）。
func (s *Store) SetLimits(id string, rpm, maxConcurrency int) (Token, bool) {
	if rpm < 0 {
		rpm = 0
	}
	if maxConcurrency < 0 {
		maxConcurrency = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return Token{}, false
	}
	t.RPM = rpm
	t.MaxConcurrency = maxConcurrency
	s.dirty = true
	_ = s.saveLocked()
	return *t, true
}
