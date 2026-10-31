package qqbot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// kkrb.go —— 接入 kkrb.net（三角洲行动一图流）的实时数据。
//
// 为什么这么绕：对方接口 `POST /getOVData` 需要 **先访问页面拿 cookie**（csrf_token +
// PHPSESSID），否则回 `{"code":-101,"msg":"系统繁忙"}`（实测：裸 POST 被挡，带 cookie 才
// 返回 200 + 222KB JSON）。所以用 cookiejar 走两步：GET 页面 → POST 接口。
//
// 一次请求里同时包含：每日密码(bdData)、制造推荐(spData)、材料浮动(smpData)、
// 热门子弹(hapData)、子弹兑换利润(dtaData)。所以只解析一次、缓存一份，各工具共用。

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

// kkrbCraftOrder 制造台展示顺序。
var kkrbCraftOrder = []string{"tech", "workbench", "pharmacy", "armory"}

// KkrbPassword 每日密码。
type KkrbPassword struct {
	Map     string `json:"map"`
	Code    string `json:"code"`
	Updated string `json:"updated,omitempty"`
}

// KkrbCraft 制造推荐（每台一条）。
type KkrbCraft struct {
	Place    string  `json:"place"`
	Pic      string  `json:"pic,omitempty"`
	Item     string  `json:"item"`
	Profit   float64 `json:"profit"`
	Material float64 `json:"material_value"`
	Price    float64 `json:"single_price"`
	SellHint string  `json:"sell_hint,omitempty"`
	Level    int     `json:"level,omitempty"`
}

// KkrbMaterial 材料价格浮动。
type KkrbMaterial struct {
	Item     string `json:"item"`
	Pic      string `json:"pic,omitempty"`
	Current  int    `json:"current"`
	High     int    `json:"high"`
	HighTime string `json:"high_time,omitempty"`
	Low      int    `json:"low"`
	LowTime  string `json:"low_time,omitempty"`
}

// KkrbAmmo 热门子弹。
type KkrbAmmo struct {
	Item    string `json:"item"`
	Current int    `json:"current"`
	Level   int    `json:"level,omitempty"`
}

// KkrbAmmoProfit 子弹兑换利润。
type KkrbAmmoProfit struct {
	Item   string  `json:"item"`
	Pic    string  `json:"pic,omitempty"`
	Profit float64 `json:"profit"`
}

// KkrbActivity 活动（研发部门集市等）。
type KkrbActivity struct {
	Name string `json:"name"`
	Time string `json:"time,omitempty"`
	Item string `json:"item,omitempty"`
}

// KkrbOverview 一次拉取的全部概览数据。
type KkrbOverview struct {
	FetchedAt  time.Time        `json:"fetched_at"`
	Passwords  []KkrbPassword   `json:"passwords"`
	Crafts     []KkrbCraft      `json:"crafts"`
	Materials  []KkrbMaterial   `json:"materials"`
	AmmoHot    []KkrbAmmo       `json:"ammo_hot"`
	AmmoProfit []KkrbAmmoProfit `json:"ammo_profit"`
	Activities []KkrbActivity   `json:"activities"`
}

// kkrbClient 带 cookie 与缓存的客户端。
type kkrbClient struct {
	mu      sync.Mutex
	http    *http.Client
	cached  *KkrbOverview
	fetched time.Time
}

func newKkrbClient() *kkrbClient {
	jar, _ := cookiejar.New(nil)
	return &kkrbClient{http: &http.Client{Timeout: 25 * time.Second, Jar: jar}}
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

// Overview 取概览数据（带 TTL 缓存；失败时回退旧缓存）。
func (k *kkrbClient) Overview(ttl time.Duration) (*KkrbOverview, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if ttl > 0 && k.cached != nil && time.Since(k.fetched) < ttl {
		return k.cached, nil
	}
	ov, err := k.fetch()
	if err != nil {
		if k.cached != nil {
			return k.cached, nil // 拉不到新的：用旧的，总比"没有"强
		}
		return nil, err
	}
	k.cached = ov
	k.fetched = time.Now()
	return ov, nil
}

func (k *kkrbClient) fetch() (*KkrbOverview, error) {
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return parseKkrbOverview(raw)
}

// parseKkrbOverview 解析接口 JSON（独立函数便于用固定样本测试）。
func parseKkrbOverview(raw []byte) (*KkrbOverview, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			BdData map[string]struct {
				Password string `json:"password"`
				Updated  string `json:"updated"`
			} `json:"bdData"`
			SpData map[string]struct {
				ItemName             string  `json:"itemName"`
				Pic                  string  `json:"pic"`
				PlaceName            string  `json:"placeName"`
				RequiredLevel        int     `json:"requiredLevel"`
				TotalMaterialValue   float64 `json:"totalMaterialValue"`
				SinglePrice          float64 `json:"singlePrice"`
				Profit               float64 `json:"profit"`
				YesterdayHighestTime string  `json:"yesterdayHighestTime"`
			} `json:"spData"`
			SmpData []struct {
				ItemName              string `json:"itemName"`
				Pic                   string `json:"pic"`
				CurrectPrice          int    `json:"currectPrice"`
				YesterdayHighestPrice int    `json:"yesterdayHighestPrice"`
				YesterdayHighestTime  string `json:"yesterdayHighestTime"`
				YesterdayLowestPrice  int    `json:"yesterdayLowestPrice"`
				YesterdayLowestTime   string `json:"yesterdayLowestTime"`
			} `json:"smpData"`
			HapData []struct {
				ObjectName   string `json:"objectName"`
				CurrectPrice int    `json:"currectPrice"`
				ItemGrade    int    `json:"itemGrade"`
			} `json:"hapData"`
			AriiData []struct {
				ActivityName string `json:"activityName"`
				ActivityTime string `json:"activityTime"`
				ItemName     string `json:"itemName"`
			} `json:"ariiData"`
			DtaData []struct {
				ItemName string  `json:"itemName"`
				Pic      string  `json:"pic"`
				Profit   float64 `json:"profit"`
			} `json:"dtaData"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 kkrb 响应失败: %w", err)
	}
	if out.Code != 1 {
		return nil, fmt.Errorf("kkrb 返回错误: code=%d msg=%s", out.Code, out.Msg)
	}
	ov := &KkrbOverview{FetchedAt: time.Now()}

	for _, m := range kkrbMapNames {
		if v, ok := out.Data.BdData[m.code]; ok && strings.TrimSpace(v.Password) != "" {
			ov.Passwords = append(ov.Passwords, KkrbPassword{
				Map: m.name, Code: v.Password, Updated: formatKkrbUpdated(v.Updated),
			})
		}
	}
	for _, place := range kkrbCraftOrder {
		v, ok := out.Data.SpData[place]
		if !ok || strings.TrimSpace(v.ItemName) == "" {
			continue
		}
		ov.Crafts = append(ov.Crafts, KkrbCraft{
			Place: v.PlaceName, Item: v.ItemName, Profit: v.Profit, Pic: v.Pic,
			Material: v.TotalMaterialValue, Price: v.SinglePrice,
			SellHint: v.YesterdayHighestTime, Level: v.RequiredLevel,
		})
	}
	for _, m := range out.Data.SmpData {
		ov.Materials = append(ov.Materials, KkrbMaterial{
			Item: m.ItemName, Pic: m.Pic, Current: m.CurrectPrice,
			High: m.YesterdayHighestPrice, HighTime: m.YesterdayHighestTime,
			Low: m.YesterdayLowestPrice, LowTime: m.YesterdayLowestTime,
		})
	}
	for _, a := range out.Data.HapData {
		ov.AmmoHot = append(ov.AmmoHot, KkrbAmmo{Item: a.ObjectName, Current: a.CurrectPrice, Level: a.ItemGrade})
	}
	for _, d := range out.Data.DtaData {
		ov.AmmoProfit = append(ov.AmmoProfit, KkrbAmmoProfit{Item: d.ItemName, Profit: d.Profit, Pic: d.Pic})
	}
	for _, a := range out.Data.AriiData {
		if strings.TrimSpace(a.ActivityName) == "" {
			continue
		}
		ov.Activities = append(ov.Activities, KkrbActivity{Name: a.ActivityName, Time: a.ActivityTime, Item: a.ItemName})
	}
	sort.Slice(ov.AmmoProfit, func(i, j int) bool { return ov.AmmoProfit[i].Profit > ov.AmmoProfit[j].Profit })
	if len(ov.Passwords) == 0 && len(ov.Crafts) == 0 {
		return nil, fmt.Errorf("kkrb 响应里没有可用数据")
	}
	return ov, nil
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
