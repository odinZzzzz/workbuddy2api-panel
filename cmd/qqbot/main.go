// Command qqbot 把 QQ（OneBot v11）接到 workbuddy2api 网关。
//
// 为什么单独一个进程而不是塞进网关：QQ 桥会引入自己的故障域（OneBot 掉线、
// 群里刷屏、网关超时重试），和网关的生命周期解耦后互不影响；它也很小（纯 net/http，
// 常驻 20MB 级），在 1.7G 的机器上不抢内存。
//
// 用法：
//
//	qqbot -config /opt/qqbot/qqbot.json
//
// 配置见 internal/qqbot/config.go；白名单为空 = 谁都不回（安全默认）。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/qqbot"
)

func main() {
	cfgPath := flag.String("config", "qqbot.json", "配置文件路径")
	flag.Parse()

	cfg, err := qqbot.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	br := qqbot.New(cfg)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           br.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		if err := br.Close(); err != nil {
			log.Printf("qqbot: flush memory: %v", err)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("qqbot listening on %s (enabled=%v model=%q gateway=%s onebot=%s groups=%d users=%d)",
		cfg.Listen, cfg.Enabled, cfg.Model, cfg.GatewayBase, cfg.OneBotAPI,
		len(cfg.AllowedGroups), len(cfg.AllowedUsers))
	if !cfg.Enabled {
		log.Printf("qqbot: enabled=false —— /onebot 会返回 503（先填白名单再打开）")
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http: %v", err)
	}
	log.Printf("qqbot bye")
	_ = os.Stdout.Sync()
}
