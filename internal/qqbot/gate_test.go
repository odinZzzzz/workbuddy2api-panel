package qqbot

import (
	"testing"
	"time"
)

func gateCfg() Config {
	c := Default()
	c.ActivationKeywords = []string{"小八嘎", "龙王"}
	c.InterruptKeywords = []string{"闭嘴", "退下"}
	c.ActivationWindowSeconds = 300
	c.SilenceSeconds = 60
	c.CalmKeywords = []string{"冷静", "安静点"}
	c.CalmSeconds = 300
	return c
}

// 空闲态：没有激活词、也没 @ → 不理。
func TestGateIdleIgnoresPlainMessage(t *testing.T) {
	g := NewGate()
	c := gateCfg()
	d := g.Decide(c, "group:1", "今天吃什么", false)
	if d.Allow || d.Reason != "idle" {
		t.Fatalf("空闲态不该放行: %+v", d)
	}
}

// 激活词 / @ 都能激活。
func TestGateActivation(t *testing.T) {
	c := gateCfg()

	g1 := NewGate()
	if d := g1.Decide(c, "group:1", "小八嘎你在吗", false); !d.Allow || d.Reason != "activated" {
		t.Fatalf("激活词应激活: %+v", d)
	}

	g2 := NewGate()
	if d := g2.Decide(c, "group:1", "在吗", true); !d.Allow || d.Reason != "activated" {
		t.Fatalf("@ 应激活: %+v", d)
	}
}

// 激活期内正常回话，且说话会续期（滑动窗口）。
func TestGateActiveWindowExtends(t *testing.T) {
	g := NewGate()
	c := gateCfg()
	c.ActivationWindowSeconds = 2

	d1 := g.Decide(c, "group:1", "小八嘎", false)
	if !d1.Allow {
		t.Fatalf("应激活: %+v", d1)
	}
	firstUntil := d1.Until

	time.Sleep(1100 * time.Millisecond)
	d2 := g.Decide(c, "group:1", "随便聊聊", false)
	if !d2.Allow || d2.Reason != "in_window" {
		t.Fatalf("激活期内应放行: %+v", d2)
	}
	if !d2.Until.After(firstUntil) {
		t.Errorf("续期应把到期时间往后推：first=%v now=%v", firstUntil, d2.Until)
	}

	// 不再说话，等窗口过去 → 回到 idle
	time.Sleep(2200 * time.Millisecond)
	if d3 := g.Decide(c, "group:1", "还在吗", false); d3.Allow || d3.Reason != "idle" {
		t.Fatalf("窗口过期应回 idle: %+v", d3)
	}
}

// 中断词 → 静默；静默期内任何消息（含激活词）都不处理。
func TestGateInterruptThenSilent(t *testing.T) {
	g := NewGate()
	c := gateCfg()

	if d := g.Decide(c, "group:1", "小八嘎", false); !d.Allow {
		t.Fatalf("先激活: %+v", d)
	}
	d := g.Decide(c, "group:1", "闭嘴", false)
	if d.Allow || d.Reason != "interrupted" || d.Mode != "silent" {
		t.Fatalf("中断词应转静默: %+v", d)
	}
	// 静默期内：激活词、@、指令全都不认
	for _, tc := range []struct {
		text string
		at   bool
	}{{"小八嘎出来", false}, {"在吗", true}, {"/help", false}} {
		if got := g.Decide(c, "group:1", tc.text, tc.at); got.Allow {
			t.Errorf("静默期内不应放行 %q: %+v", tc.text, got)
		}
	}
}

// 静默期到期后自动回 idle，需要重新激活。
func TestGateSilenceExpires(t *testing.T) {
	g := NewGate()
	c := gateCfg()
	c.SilenceSeconds = 1

	g.Decide(c, "group:1", "闭嘴", false) // idle 下也能被叫停
	if d := g.Decide(c, "group:1", "小八嘎", false); d.Allow {
		t.Fatalf("静默期内不该放行: %+v", d)
	}
	time.Sleep(1200 * time.Millisecond)
	if d := g.Decide(c, "group:1", "小八嘎", false); !d.Allow || d.Reason != "activated" {
		t.Fatalf("静默到期后应可重新激活: %+v", d)
	}
}

// 状态按会话隔离：A 群静默不影响 B 群。
func TestGatePerChatIsolation(t *testing.T) {
	g := NewGate()
	c := gateCfg()
	g.Decide(c, "group:1", "闭嘴", false)
	if d := g.Decide(c, "group:2", "小八嘎", false); !d.Allow {
		t.Fatalf("另一个群不该受静默影响: %+v", d)
	}
}

// Snapshot 能反映当前模式（后台展示用）。
func TestGateSnapshot(t *testing.T) {
	g := NewGate()
	c := gateCfg()
	g.Decide(c, "group:1", "小八嘎", false)
	g.Decide(c, "group:2", "闭嘴", false)
	views := g.Snapshot()
	if len(views) != 2 {
		t.Fatalf("应有 2 条状态，得到 %d", len(views))
	}
	modes := map[string]string{}
	for _, v := range views {
		modes[v.Key] = v.Mode
	}
	if modes["group:1"] != "active" || modes["group:2"] != "silent" {
		t.Fatalf("模式不对: %+v", modes)
	}
}

// @ + 冷静词 → 直接进 5 分钟冷静期（且能把已静默期升级加长）。
func TestGateCalmCommand(t *testing.T) {
	g := NewGate()
	c := gateCfg()
	c.CalmKeywords = []string{"冷静"}
	c.CalmSeconds = 300

	// 激活态下冷静
	if d := g.Decide(c, "group:1", "小八嘎", false); !d.Allow {
		t.Fatalf("先激活: %+v", d)
	}
	d := g.Decide(c, "group:1", "冷静", true)
	if d.Allow || d.Reason != "calm" || d.Mode != "silent" {
		t.Fatalf("@+冷静 应转冷静期: %+v", d)
	}
	if time.Until(d.Until) < 4*time.Minute {
		t.Errorf("冷静期应约 5 分钟，实际剩余 %v", time.Until(d.Until))
	}
	// 冷静期内一切照旧不理
	if got := g.Decide(c, "group:1", "小八嘎", false); got.Allow {
		t.Fatalf("冷静期内不该放行: %+v", got)
	}

	// 已静默期被升级：先 60s 中断，再 @+冷静 → 到期时间应更晚
	g2 := NewGate()
	c2 := gateCfg()
	c2.SilenceSeconds = 60
	c2.CalmSeconds = 300
	c2.CalmKeywords = []string{"冷静"}
	d1 := g2.Decide(c2, "group:2", "闭嘴", false)
	d2 := g2.Decide(c2, "group:2", "冷静", true)
	if !d2.Until.After(d1.Until) {
		t.Errorf("冷静指令应延长静默：before=%v after=%v", d1.Until, d2.Until)
	}
}

// 不带 @ 的"冷静"不算冷静指令（避免闲聊误触）。
func TestGateCalmRequiresAt(t *testing.T) {
	g := NewGate()
	c := gateCfg()
	c.CalmKeywords = []string{"冷静"}
	g.Decide(c, "group:1", "小八嘎", false) // 激活
	d := g.Decide(c, "group:1", "我要冷静一下", false)
	if !d.Allow {
		t.Fatalf("不带 @ 的冷静不该触发冷静期: %+v", d)
	}
}
