package qqbot

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// admin.go —— 内置管理后台。
//
// 为什么塞进同一个二进制：这台机器只有 1.7G 内存，再起一个 Web 服务（Node/Python）
// 就是几十上百 MB；而这个后台只需要几个表单和一张表，用 net/http + 一页 HTML 足够，
// 常驻内存可以忽略。
//
// 安全默认：
//   - admin_listen 为空 = 不启用后台（最小暴露面）；
//   - admin_token 非空时所有 /admin* 都要 Bearer 或 ?token=；
//   - token 为空且监听在 0.0.0.0 时**拒绝启动**（避免把改人设的口子裸奔到公网）。

// RecentEvent 最近事件（后台展示用，内存里只留环形缓冲）。
type RecentEvent struct {
	Time   time.Time `json:"time"`
	Type   string    `json:"type"`
	Group  int64     `json:"group"`
	User   int64     `json:"user"`
	Nick   string    `json:"nick,omitempty"`
	At     bool      `json:"at"`
	Runes  int       `json:"runes"`
	Action string    `json:"action"` // replied / ignored_at / ignored_user / not_allowed / empty
}

const recentLimit = 60

// state 后台需要的运行时状态（与 Bridge 同锁保护）。
type adminState struct {
	mu        sync.Mutex
	recent    []RecentEvent
	events    int64
	replied   int64
	skipped   int64
	startedAt time.Time
}

func (s *adminState) note(ev RecentEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events++
	if ev.Action == "replied" {
		s.replied++
	} else {
		s.skipped++
	}
	s.recent = append(s.recent, ev)
	if len(s.recent) > recentLimit {
		s.recent = s.recent[len(s.recent)-recentLimit:]
	}
}

func (s *adminState) snapshot() ([]RecentEvent, int64, int64, int64, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecentEvent, len(s.recent))
	copy(out, s.recent)
	return out, s.events, s.replied, s.skipped, s.startedAt
}

// maskKey 打码（后台不回显完整 api_key）。
func maskKey(k string) string {
	if len(k) <= 12 {
		if k == "" {
			return ""
		}
		return "***"
	}
	return k[:8] + "…" + k[len(k)-4:]
}

// AdminHandler 组装后台路由。
func (b *Bridge) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin", b.adminPage)
	mux.HandleFunc("GET /admin/api/state", b.adminState)
	mux.HandleFunc("POST /admin/api/config", b.adminSaveConfig)
	mux.HandleFunc("POST /admin/api/memory/forget", b.adminForget)
	return b.adminAuth(mux)
}

// adminAuth token 校验：Bearer 或 ?token=。token 为空时只允许回环来源。
func (b *Bridge) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := b.cfg().AdminToken
		if want == "" {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil || (host != "127.0.0.1" && host != "::1") {
				http.Error(w, "admin token not configured; only loopback allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got != want {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminState GET /admin/api/state —— 配置（key 打码）+ 统计 + 记忆 + 最近事件。
func (b *Bridge) adminState(w http.ResponseWriter, r *http.Request) {
	recent, events, replied, skipped, started := b.state.snapshot()
	cfg := b.currentConfig()
	cfg.GatewayKey = maskKey(cfg.GatewayKey)
	cfg.OneBotToken = maskKey(cfg.OneBotToken)
	cfg.AdminToken = maskKey(cfg.AdminToken)

	type memView struct {
		UID       int64    `json:"uid"`
		Nick      string   `json:"nick"`
		Count     int      `json:"count"`
		FirstSeen string   `json:"first_seen"`
		LastSeen  string   `json:"last_seen"`
		Notes     []string `json:"notes"`
		Verdict   string   `json:"verdict,omitempty"`
		Affinity  int      `json:"affinity"`
		Tier      string   `json:"tier"`
		QQLevel   int      `json:"qq_level"`
		Sign      string   `json:"sign,omitempty"`
		Digits    int      `json:"digits"`
		Avatar    string   `json:"avatar,omitempty"`
		Profile   string   `json:"profile,omitempty"`
	}
	var mems []memView
	if b.mem != nil {
		b.mem.mu.Lock()
		for _, u := range b.mem.users {
			mems = append(mems, memView{
				UID: u.UID, Nick: u.Nick, Count: u.Count,
				FirstSeen: u.FirstSeen.Format("2006-01-02 15:04"),
				LastSeen:  u.LastSeen.Format("2006-01-02 15:04"),
				Notes:     u.Notes,
				Verdict:   u.Verdict,
				Affinity:  u.Affinity,
				Tier:      AffinityTier(u.Affinity),
				QQLevel:   u.QQLevel,
				Sign:      u.Sign,
				Digits:    u.Digits,
				Avatar:    u.Avatar,
				Profile:   u.ProfileLine(),
			})
		}
		b.mem.mu.Unlock()
		sort.Slice(mems, func(i, j int) bool { return mems[i].LastSeen > mems[j].LastSeen })
	}
	sessions, limiters := b.Stats()

	resp := map[string]any{
		"ok":        true,
		"config":    cfg,
		"stats":     map[string]any{"events": events, "replied": replied, "skipped": skipped, "sessions": sessions, "limiters": limiters, "memory_users": len(mems), "uptime_sec": int(time.Since(started).Seconds())},
		"recent":    recent,
		"memory":    mems,
		"gate":      b.gateSnapshot(),
		"botguard":  b.botGuardSnapshot(),
		"startedAt": started.Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

// adminSaveConfig POST /admin/api/config —— 保存并热生效（能热改的立刻生效）。
func (b *Bridge) adminSaveConfig(w http.ResponseWriter, r *http.Request) {
	// 通用合并：面板能改的字段远多于旧白名单（人设、激活词、知识库、图表字体…）。
	// 之前只认 13 个字段，其它一律丢弃 → 面板点保存后回读还是旧值（"保存就还原"）。
	// 现在把请求体当 patch，与当前配置做 JSON 层合并：只覆盖提交的键，其余保持不变。
	var patch map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&patch); err != nil {
		writeAdminErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(patch) == 0 {
		writeAdminErr(w, http.StatusBadRequest, "empty patch")
		return
	}
	cfg := b.currentConfig()

	// 当前配置 → map → 合并 patch → 回结构体（未知键自然被忽略，已知键全部生效）
	curRaw, err := json.Marshal(cfg)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var merged map[string]any
	if err := json.Unmarshal(curRaw, &merged); err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for k, v := range patch {
		// 只接受配置里真实存在的键，避免写入垃圾字段
		if _, ok := merged[k]; !ok {
			continue
		}
		merged[k] = v
	}
	outRaw, err := json.Marshal(merged)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var next Config
	if err := json.Unmarshal(outRaw, &next); err != nil {
		writeAdminErr(w, http.StatusBadRequest, "config decode: "+err.Error())
		return
	}
	// model 不允许被清空（清空等于停摆）
	if strings.TrimSpace(next.Model) == "" {
		next.Model = cfg.Model
	}
	cfg = next
	b.applyConfig(cfg)
	if err := b.saveConfig(); err != nil {
		writeAdminErr(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	log.Printf("[qqbot] admin: 配置已更新并热生效（model=%q groups=%v no_at=%v rate=%d）",
		cfg.Model, cfg.AllowedGroups, cfg.NoAtGroups, cfg.RatePerMinute)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// adminForget POST /admin/api/memory/forget —— 忘掉某个 QQ 号的记忆。
func (b *Bridge) adminForget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UID int64 `json:"uid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.UID == 0 {
		writeAdminErr(w, http.StatusBadRequest, "需要 uid")
		return
	}
	if b.mem != nil {
		b.mem.mu.Lock()
		delete(b.mem.users, in.UID)
		b.mem.dirty = true
		b.mem.mu.Unlock()
		if err := b.mem.Flush(); err != nil {
			writeAdminErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	log.Printf("[qqbot] admin: 已清除 QQ %d 的记忆", in.UID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func writeAdminErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
	_, _ = w.Write(b)
}

// StartAdmin 启动后台监听（admin_listen 为空则不启动）。
func (b *Bridge) StartAdmin() error {
	addr := strings.TrimSpace(b.currentConfig().AdminListen)
	if addr == "" {
		return nil
	}
	if b.currentConfig().AdminToken == "" && !strings.HasPrefix(addr, "127.0.0.1") && !strings.HasPrefix(addr, "localhost") {
		return fmt.Errorf("admin_listen=%s 但 admin_token 为空：拒绝把无鉴权的后台暴露出去", addr)
	}
	srv := &http.Server{Addr: addr, Handler: b.AdminHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("[qqbot] admin 后台: http://%s/admin （token %s）", addr, maskKey(b.currentConfig().AdminToken))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[qqbot] admin 监听失败: %v", err)
		}
	}()
	return nil
}

// adminPage 返回内置页面（单文件、无外链、无构建步骤）。
func (b *Bridge) adminPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminHTML))
}
