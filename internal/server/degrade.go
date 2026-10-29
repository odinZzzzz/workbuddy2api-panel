package server

import (
	"sync"
	"time"
)

// degradeGate 降级状态机：passthrough/append 模式下请求被上游内容策略拦截时，
// 切换到 Degraded 中性提示词，并在窗口内让后续请求直达中性提示词。
//
// 定位：误报处理（与 internal/upstream/sanitize.go 同哲学）——内容拦截多为
// system 来源的指纹误报，换最小中性提示词即可绕开；不是对抗框架。
//
// 窗口长度由 ttl 决定：
//   - ttl > 0（默认 30m，见 prompt.degrade_ttl）：自愈窗口。到期自动回到
//     passthrough 探一次，再被拦才重新降级。一次误报不该让全天都跑中性提示词
//     ——客户端那边只会表现为「模型今天变笨了」，且没有任何错误可循。
//   - ttl = 0：旧语义，到次日 00:00 CST 复位。
//
// 状态机：进程内存、重启清零（可接受：重启极少触发，且 custom 模式根本不进降级路径）。
type degradeGate struct {
	mu    sync.Mutex
	until time.Time
	ttl   time.Duration
}

// Active 当前是否处于降级期（now < until）。
func (g *degradeGate) Active() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.until)
}

// Trigger 触发降级。窗口长度由 ttl 决定：ttl>0 → now+ttl（自愈）；ttl=0 → 次日 00:00 CST。
// 已在降级期内则不续期（保持最早触发点的复位时刻，避免每次命中都把窗口往后推）。
func (g *degradeGate) Trigger() {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if now.Before(g.until) {
		return
	}
	if g.ttl > 0 {
		g.until = now.Add(g.ttl)
		return
	}
	g.until = nextMidnightCST(now)
}

// SetTTL 设置降级窗口时长（0 = 到次日 00:00 CST）。只影响下一次触发：
// 进行中的窗口不变，避免运行期改配置把当前窗口无限延长。
func (g *degradeGate) SetTTL(d time.Duration) {
	if d < 0 {
		d = 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ttl = d
}

// Snapshot 一次取回「是否降级 + 结束时刻」，供 /status 与面板展示。
//
// 为什么必须能读出来：降级期是**静默改变出站提示词**——一次 400 就换来「今天所有请求
// 都在用另一套 system prompt」，而客户端那边只表现为「模型变笨了」。不暴露状态的话，
// 这类故障只能靠人猜。未触发/已过期时返回 (false, 零值)。
func (g *degradeGate) Snapshot() (bool, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !time.Now().Before(g.until) {
		return false, time.Time{}
	}
	return true, g.until
}

// nextMidnightCST 返回 now 之后最近的 Asia/Shanghai 00:00 时刻（纯函数，方便单测）。
//
// 边界语义：
//   - 23:59 → 次日 00:00（几秒后）
//   - 00:00 → 次日 00:00（刚过 00:00，下个零点是次日）
//
// 用固定 +08:00 偏移计算，避免依赖系统时区配置（容器/宿主机时区不确定）。
func nextMidnightCST(now time.Time) time.Time {
	cst := time.FixedZone("CST", 8*60*60)
	// 把 now 转到 CST 视角取当天 00:00，再加一天；若该 00:00 <= now 则再加一天。
	y, m, d := now.In(cst).Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, cst)
	for !midnight.After(now) {
		midnight = midnight.Add(24 * time.Hour)
	}
	return midnight
}
