package qqbot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// kkrb.go —— 接入 kkrb.net（三角洲行动一图流）的实时数据。
//
// 为什么这么绕：对方接口 `POST /getOVData` 需要 **先访问页面拿 cookie**（csrf_token +
// PHPSESSID），否则回 `{"code":-101,"msg":"系统繁忙"}`（实测：裸 POST 被挡，带 cookie 就
// 返回 200 + 222KB JSON）。所以这里用 cookiejar 走两步：GET 页面 → POST 接口。
//
// 数据每天更新（每日地图密码），所以按 TTL 缓存（默认 10 分钟），避免每次提问都打对方。

const (
	kkrbHome   = "https://www.kkrb.net/?viewpage=overview"
	kkrbOVData = "https://www.kkrb.net/getOVData"
	kkrbUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36"
)

// kkrbMapNames 接口里的地图代号 → 中文名（顺序即展示顺序）。
var kkrbMapNames = []struct{ code, name string }{
	{"db", "零号大坝"},
	{"cgxg", "长弓溪谷"},
	{"bks", "巴克什"},
	{"htjd", "航天基地"},
	{"cxjy", "潮汐监狱"},
	{"az3", "AZ3"},
}

// KkrbPassword 一条每日密码。
type KkrbPassword struct {
	Map     string `json:"map"`
	Code    string `json:"code"`
	Updated string `json:"updated,omitempty"`
}

// kkrbClient 带 cookie 与缓存的客户端。
type kkrbClient struct {
	mu      sync.Mutex
	http    *http.Client
	cached  []KkrbPassword
	fetched time.Time
	lastErr error
}

func newKkrbClient() *kkrbClient {
	jar, _ := cookiejar.New(nil)
	return &kkrbClient{
		http: &http.Client{Timeout: 20 * time.Second, Jar: jar},
	}
}

// warm 访问页面拿 cookie（对方要求有会话）。
func (k *kkrbClient) warm() error {
	req, err := http.NewRequest(http.MethodGet, kkrbHome, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", kkrbUA)
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// DailyPasswords 取每日地图密码（带缓存）。
func (k *kkrbClient) DailyPasswords(ttl time.Duration) ([]KkrbPassword, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if ttl > 0 && len(k.cached) > 0 && time.Since(k.fetched) < ttl {
		return k.cached, nil
	}
	list, err := k.fetch()
	if err != nil {
		k.lastErr = err
		// 拿不到但有旧数据：返回旧的（标 stale 由调用方决定），总比"没有"强
		if len(k.cached) > 0 {
			return k.cached, nil
		}
		return nil, err
	}
	k.cached = list
	k.fetched = time.Now()
	k.lastErr = nil
	return list, nil
}

func (k *kkrbClient) fetch() ([]KkrbPassword, error) {
	if err := k.warm(); err != nil {
		return nil, fmt.Errorf("访问 kkrb 页面失败: %w", err)
	}
	form := url.Values{"globalData": {"false"}}
	req, err := http.NewRequest(http.MethodPost, kkrbOVData, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", kkrbUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", kkrbHome)
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 kkrb 数据失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			BdData map[string]struct {
				Password string `json:"password"`
				Updated  string `json:"updated"`
			} `json:"bdData"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 kkrb 响应失败: %w", err)
	}
	if out.Code != 1 {
		return nil, fmt.Errorf("kkrb 返回错误: code=%d msg=%s", out.Code, out.Msg)
	}
	list := make([]KkrbPassword, 0, len(kkrbMapNames))
	for _, m := range kkrbMapNames {
		if v, ok := out.Data.BdData[m.code]; ok && strings.TrimSpace(v.Password) != "" {
			list = append(list, KkrbPassword{Map: m.name, Code: v.Password, Updated: formatKkrbUpdated(v.Updated)})
		}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("kkrb 响应里没有密码数据")
	}
	return list, nil
}

// formatKkrbUpdated "20261031000001" → "2026-10-31"。
func formatKkrbUpdated(s string) string {
	if len(s) < 8 {
		return ""
	}
	return s[:4] + "-" + s[4:6] + "-" + s[6:8]
}

// kkrbSingleton 进程内单例（cookie 复用）。
var kkrbSingleton = newKkrbClient()
