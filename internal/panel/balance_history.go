package panel

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/balancehist"
)

// balance_history.go —— 账号积分余额的时序视图（面板「仪表盘」趋势图数据源）。
//
// 采样由调度器在余额刷新后写入（见 scheduler.RunBalanceRefreshNow），本文件只做读。
// 返回结构刻意扁平：points 里每点一个 uid→credits 映射，账号元信息（昵称/域）单独
// 给一份，前端据此渲染堆叠面积图，不用在每点里重复昵称。

type balanceSeriesResp struct {
	OK        bool                 `json:"ok"`
	Enabled   bool                 `json:"enabled"`
	Hours     float64              `json:"hours"`
	From      string               `json:"from,omitempty"`
	To        string               `json:"to,omitempty"`
	Points    []balancePoint       `json:"points"`
	Accounts  []balanceAccountMeta `json:"accounts"`
	FileBytes int64                `json:"file_bytes,omitempty"`
}

type balancePoint struct {
	T        string           `json:"t"`
	Accounts map[string]int64 `json:"a"`
	Total    int64            `json:"total"`
}

type balanceAccountMeta struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Realm    string `json:"realm"`
}

// balanceHistory GET /panel/api/balance_history?hours=N
//
// hours 默认 24、上限 168（采样器只保留 7 天）。非法值按默认处理而不是报错——
// 这是只读观测接口，客户端传错参数时给默认视图比给 400 更有用。
func (p *Panel) balanceHistory(w http.ResponseWriter, r *http.Request) {
	if p.cfg.BalanceHistory == nil {
		writeJSON(w, http.StatusOK, balanceSeriesResp{OK: true, Enabled: false, Points: []balancePoint{}, Accounts: []balanceAccountMeta{}})
		return
	}
	hours := 24.0
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			hours = v
		}
	}
	if hours > 168 {
		hours = 168
	}
	since := time.Now().Add(-time.Duration(hours * float64(time.Hour)))
	pts, err := p.cfg.BalanceHistory.Series(since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "balance history: "+err.Error())
		return
	}

	// 账号元信息：以当前池为准（历史里已删除的账号不展示，避免图例出现幽灵账号）
	metas := make([]balanceAccountMeta, 0, 8)
	seen := map[string]bool{}
	for _, st := range p.cfg.Pool.List() {
		metas = append(metas, balanceAccountMeta{UID: st.UID, Nickname: st.Nickname, Realm: st.Realm})
		seen[st.UID] = true
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Nickname < metas[j].Nickname })

	out := make([]balancePoint, 0, len(pts))
	var total int64
	for _, pt := range pts {
		acc := make(map[string]int64, len(pt.Accounts))
		var sum int64
		for uid, c := range pt.Accounts {
			if !seen[uid] {
				continue
			}
			acc[uid] = c
			sum += c
		}
		if len(acc) == 0 {
			continue
		}
		out = append(out, balancePoint{T: pt.T.Format(time.RFC3339), Accounts: acc, Total: sum})
		total = sum
	}

	resp := balanceSeriesResp{
		OK:       true,
		Enabled:  true,
		Hours:    hours,
		Points:   out,
		Accounts: metas,
	}
	if len(out) > 0 {
		resp.From = out[0].T
		resp.To = out[len(out)-1].T
	}
	_ = total
	writeJSON(w, http.StatusOK, resp)
}

var _ = balancehist.Point{}
