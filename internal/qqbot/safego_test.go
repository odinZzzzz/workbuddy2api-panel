package qqbot

import (
	"sync"
	"testing"
	"time"
)

// safeGo 必须吞掉 panic：回复链路里任何一处崩，裸 go 会带走整个桥进程。
func TestSafeGoRecovers(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	safeGo("test-panic", func() {
		defer wg.Done()
		panic("boom")
	})
	safeGo("test-ok", func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
	})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		// 进程还活着、两个 goroutine 都退出了 → recover 生效
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo 卡住了")
	}
}

// 令牌桶表不能无限增长：超过阈值时清掉早已回满的旧桶。
func TestLimitsEvicted(t *testing.T) {
	c := Default()
	c.RatePerMinute = 8
	c.MemoryFile = ""
	b := New(c)

	// 塞 600 个「10 分钟没动过」的桶
	b.mu.Lock()
	for i := int64(0); i < 600; i++ {
		b.limits[i] = &bucket{tokens: 1, last: time.Now().Add(-30 * time.Minute)}
	}
	b.mu.Unlock()

	b.allow(999999) // 触发淘汰

	b.mu.Lock()
	n := len(b.limits)
	b.mu.Unlock()
	if n > 512 {
		t.Fatalf("旧桶没被清理，仍有 %d 个", n)
	}
	// 当前用户的桶必须还在（否则限速失效）
	b.mu.Lock()
	_, ok := b.limits[999999]
	b.mu.Unlock()
	if !ok {
		t.Error("当前用户的桶不该被清掉")
	}
}
