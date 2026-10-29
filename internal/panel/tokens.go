package panel

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/tokenstore"
)

// tokens.go —— 多令牌（API Key）管理的面板接口。
//
// 形态对齐 one-api / new-api 那类中转站：一个网关签发多把 key，每把独立启停、
// 过期时间、积分配额与用量统计。全局 api_key 始终有效，令牌是叠加通道。

// tokenView 对外视图：key 一律打码，只有创建响应里带完整值（仅此一次）。
type tokenView struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Key           string   `json:"key"` // 打码值
	Enabled       bool     `json:"enabled"`
	CreatedAt     string   `json:"created_at"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	QuotaCredits  float64  `json:"quota_credits"`
	UsedCredits   float64  `json:"used_credits"`
	UsedRequests  int64    `json:"used_requests"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	Note          string   `json:"note,omitempty"`
	Expired       bool     `json:"expired"`
	QuotaExceeded bool     `json:"quota_exceeded"`
}

func toTokenView(t tokenstore.Token, now time.Time) tokenView {
	v := tokenView{
		ID:            t.ID,
		Name:          t.Name,
		Key:           tokenstore.MaskedKey(t.Key),
		Enabled:       t.Enabled,
		CreatedAt:     t.CreatedAt.Format(time.RFC3339),
		QuotaCredits:  t.QuotaCredits,
		UsedCredits:   t.UsedCredits,
		UsedRequests:  t.UsedRequests,
		AllowedModels: t.AllowedModels,
		Note:          t.Note,
		Expired:       t.Expired(now),
		QuotaExceeded: t.QuotaExceeded(),
	}
	if !t.ExpiresAt.IsZero() {
		v.ExpiresAt = t.ExpiresAt.Format(time.RFC3339)
	}
	return v
}

// tokensList GET /panel/api/tokens
func (p *Panel) tokensList(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Tokens == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens": []tokenView{}, "disabled": true})
		return
	}
	now := time.Now()
	list := p.cfg.Tokens.List()
	out := make([]tokenView, 0, len(list))
	for _, t := range list {
		out = append(out, toTokenView(t, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens": out})
}

// tokensCreate POST /panel/api/tokens
//
// body: {name, quota_credits, expires_at(RFC3339 或空), allowed_models([]string), note}
// 响应里 key 是**完整值**，前端必须提示用户立刻复制（之后列表只给打码值）。
func (p *Panel) tokensCreate(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Tokens == nil {
		writeErr(w, http.StatusNotImplemented, "token management is not enabled")
		return
	}
	var req struct {
		Name          string   `json:"name"`
		QuotaCredits  float64  `json:"quota_credits"`
		ExpiresAt     string   `json:"expires_at"`
		AllowedModels []string `json:"allowed_models"`
		Note          string   `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	var expires time.Time
	if s := strings.TrimSpace(req.ExpiresAt); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			// 也接受 "2026-11-01" 这种纯日期（前端 date 控件常见形态）
			t, err = time.Parse("2006-01-02", s)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "expires_at 需要 RFC3339 或 YYYY-MM-DD")
				return
			}
			t = t.Add(24*time.Hour - time.Second) // 纯日期 → 当天 23:59:59
		}
		expires = t
	}
	tok, err := p.cfg.Tokens.Create(req.Name, req.QuotaCredits, expires, req.AllowedModels, req.Note)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v := toTokenView(tok, time.Now())
	v.Key = tok.Key // 完整值，仅此一次
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": v})
}

// tokensToggle POST /panel/api/tokens/{id}/toggle
func (p *Panel) tokensToggle(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Tokens == nil {
		writeErr(w, http.StatusNotImplemented, "token management is not enabled")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	cur := p.findToken(id)
	if cur == nil {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	next := !cur.Enabled
	if req.Enabled != nil {
		next = *req.Enabled
	}
	tok, ok := p.cfg.Tokens.SetEnabled(id, next)
	if !ok {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": toTokenView(tok, time.Now())})
}

// tokensReset POST /panel/api/tokens/{id}/reset
func (p *Panel) tokensReset(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Tokens == nil {
		writeErr(w, http.StatusNotImplemented, "token management is not enabled")
		return
	}
	tok, ok := p.cfg.Tokens.ResetUsage(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": toTokenView(tok, time.Now())})
}

// tokensDelete DELETE /panel/api/tokens/{id}
func (p *Panel) tokensDelete(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Tokens == nil {
		writeErr(w, http.StatusNotImplemented, "token management is not enabled")
		return
	}
	if !p.cfg.Tokens.Delete(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// findToken 按 id 取令牌副本（toggle 需要读当前状态再取反）。
func (p *Panel) findToken(id string) *tokenstore.Token {
	if p.cfg.Tokens == nil {
		return nil
	}
	for _, t := range p.cfg.Tokens.List() {
		if t.ID == id {
			cp := t
			return &cp
		}
	}
	return nil
}
