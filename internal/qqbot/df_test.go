package qqbot

import (
	"strings"
	"testing"
)

// 官网资讯解析：标题/类型/日期/详情链接都要能落地。
func TestParseDFNews(t *testing.T) {
	fixture := `{"status":0,"msg":"success","data":{"items":[
		{"sTitle":"11月5日全新「微光」赛季开启","sCreated":"2026-10-31 10:00:00","iDocID":"2449827827177381909",
		 "sTagInfoList":[{"name":"媒体中心","level":1},{"name":"新闻","level":2}]},
		{"sTitle":"10月29日更新公告","sCreated":"2026-10-29 09:30:00","iDocID":"3953992625363444999",
		 "sTagInfoList":[{"name":"公告","level":2}]},
		{"sTitle":"","sCreated":"2026-10-28 00:00:00","iDocID":"x"}
	]}}`
	list, err := parseDFNews([]byte(fixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("空标题应被过滤，得到 %d 条", len(list))
	}
	if list[0].Type != "新闻" || list[0].Date != "2026-10-31" {
		t.Errorf("首条类型/日期不对: %+v", list[0])
	}
	if !strings.Contains(list[0].URL, "2449827827177381909") {
		t.Errorf("详情链接不对: %s", list[0].URL)
	}
	if list[1].Type != "公告" {
		t.Errorf("类型应取最深标签: %+v", list[1])
	}
}

// 接口报错要变成 error。
func TestParseDFNewsError(t *testing.T) {
	if _, err := parseDFNews([]byte(`{"status":1,"msg":"fail"}`)); err == nil {
		t.Fatal("status!=0 应报错")
	}
	if _, err := parseDFNews([]byte(`not json`)); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}
