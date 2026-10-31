package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// qqbot.go —— 把独立的 QQ bot 桥后台代理进面板。
//
// 为什么代理而不是让浏览器直连：qqbot 的后台只监听 127.0.0.1:8095（最小暴露面，
// 那台机器 1.7G 内存，不想再开公网端口）。面板和后端在同一台机器上，代理一层最省事，
// 而且能复用面板已有的 api_key 鉴权——不用给 qqbot 再发一把 token。
//
// 配置：panel.qqbot_admin_url（默认 http://127.0.0.1:8095）。

// qqbotProxy 把请求转发给 qqbot 后台，原样回传状态码与 body。
func (p *Panel) qqbotProxy(w http.ResponseWriter, r *http.Request, path string) {
	base := strings.TrimRight(p.cfg.QQBotAdminURL, "/")
	if base == "" {
		writeErr(w, http.StatusNotImplemented, "qqbot admin not configured (panel.qqbot_admin_url)")
		return
	}
	var body []byte
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		body = b
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base+path, bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// qqbot 后台在 admin_token 非空时需要 Bearer；为空则回环放行，不带头也行。
	if tok := p.cfg.QQBotAdminToken; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("qqbot admin 不可达（%s）：%v", base, err))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}

// qqbotState GET /panel/api/qqbot/state
func (p *Panel) qqbotState(w http.ResponseWriter, r *http.Request) {
	p.qqbotProxy(w, r, "/admin/api/state")
}

// qqbotConfig POST /panel/api/qqbot/config —— 保存人设/白名单等（qqbot 侧热生效）
func (p *Panel) qqbotConfig(w http.ResponseWriter, r *http.Request) {
	p.qqbotProxy(w, r, "/admin/api/config")
}

// qqbotForget POST /panel/api/qqbot/forget —— 清除某个 QQ 号的记忆
func (p *Panel) qqbotForget(w http.ResponseWriter, r *http.Request) {
	p.qqbotProxy(w, r, "/admin/api/memory/forget")
}

var _ = json.Marshal
