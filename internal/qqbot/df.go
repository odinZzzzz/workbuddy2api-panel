package qqbot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// df.go —— 三角洲行动官网（df.qq.com）资讯接入。
//
// 数据来源：官网首页的资讯列表由腾讯「gicp」组件异步填充（HTML 里 <ul id="newsList">
// 是空的），真实接口是：
//
//	https://apps.game.qq.com/cmc/cross?serviceId=423&source=ingame&typeids=2
//	    &filter=tag&sortby=sCreated&limit=N&start=0
//
// 实测服务器可直连（200 + JSON），字段：sTitle / sCreated / sTagInfoList / iDocID。
// 详情页链接：https://df.qq.com/cp/a20240906main/newsdetail.html?id=<iDocID>
//
// 缓存 10 分钟：官网更新频率是"天"级，没必要每次提问都打。

// 实测：这个接口**必须带 tagids**，否则回 {"status":-1,"msg":"no data"}。
// tagids=143148,143150 是官网「媒体中心/新官网视频」分类，返回的是视频/赛事/干员档案流；
// 首页那个"公告/新闻"列表走的是 fillnewsgicp 组件（channel 6914/6895/6910），
// 实测直接调 zmMcnTargetContentList 与 cmc/cross 都返回空（需要页面侧额外参数），
// 所以公告类以 kkrb 活动数据 + 官网链接兜底，这里只提供"最新内容流"。
const dfNewsAPI = "https://apps.game.qq.com/cmc/cross?serviceId=423&source=ingame&typeids=2&filter=tag&sortby=sCreated&limit=12&start=0&tagids=143148%2C143150"

const dfNewsDetail = "https://df.qq.com/cp/a20240906main/newsdetail.html?id="

// DFNews 一条官网资讯。
type DFNews struct {
	Title string `json:"title"`
	Type  string `json:"type,omitempty"`
	Date  string `json:"date,omitempty"`
	URL   string `json:"url,omitempty"`
}

// dfClient 官网资讯客户端（带缓存）。
type dfClient struct {
	mu      sync.Mutex
	http    *http.Client
	cached  []DFNews
	fetched time.Time
}

func newDFClient() *dfClient {
	return &dfClient{http: &http.Client{Timeout: 20 * time.Second}}
}

// News 取官网最新资讯（TTL 缓存，失败回退旧数据）。
func (d *dfClient) News(ttl time.Duration) ([]DFNews, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ttl > 0 && len(d.cached) > 0 && time.Since(d.fetched) < ttl {
		return d.cached, nil
	}
	list, err := d.fetch()
	if err != nil {
		if len(d.cached) > 0 {
			return d.cached, nil
		}
		return nil, err
	}
	d.cached = list
	d.fetched = time.Now()
	return list, nil
}

func (d *dfClient) fetch() ([]DFNews, error) {
	req, err := http.NewRequest(http.MethodGet, dfNewsAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", kkrbUA)
	req.Header.Set("Referer", "https://df.qq.com/")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求官网资讯失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return parseDFNews(raw)
}

// parseDFNews 解析官网资讯 JSON（独立函数便于固定样本测试）。
func parseDFNews(raw []byte) ([]DFNews, error) {
	var out struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
		Data   struct {
			Items []struct {
				Title   string `json:"sTitle"`
				Created string `json:"sCreated"`
				IdxTime string `json:"sIdxTime"`
				DocID   string `json:"iDocID"`
				TagInfo []struct {
					Name  string `json:"name"`
					Level int    `json:"level"`
				} `json:"sTagInfoList"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析官网资讯失败: %w", err)
	}
	if out.Status != 0 {
		return nil, fmt.Errorf("官网接口返回错误: status=%d msg=%s", out.Status, out.Msg)
	}
	list := make([]DFNews, 0, len(out.Data.Items))
	for _, it := range out.Data.Items {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			continue
		}
		// 类型取最深的叶子标签（如「公告」「新闻」「赛事」「视频」）
		typ := ""
		for _, t := range it.TagInfo {
			if t.Name != "" {
				typ = t.Name
			}
		}
		date := it.Created
		if len(date) >= 10 {
			date = date[:10]
		}
		n := DFNews{Title: title, Type: typ, Date: date}
		if it.DocID != "" {
			n.URL = dfNewsDetail + it.DocID
		}
		list = append(list, n)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("官网资讯为空")
	}
	return list, nil
}

// dfSingleton 进程内单例。
var dfSingleton = newDFClient()
