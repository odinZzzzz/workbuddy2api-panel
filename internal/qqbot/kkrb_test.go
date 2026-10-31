package qqbot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 解析 kkrb 接口样本：密码/制造/材料/子弹四类都要能落地。
func TestParseKkrbOverview(t *testing.T) {
	fixture := `{"code":1,"msg":"获取成功","data":{
		"bdData":{
			"db":{"password":"7353","updated":"20261031000001"},
			"cgxg":{"password":"2030","updated":"20261031000001"},
			"az3":{"password":"1482","updated":"20261031000001"}
		},
		"spData":{
			"tech":{"itemName":"先进多口径消音器","placeName":"技术中心","requiredLevel":3,
				"totalMaterialValue":41941,"singlePrice":88736,"profit":37034.04,"yesterdayHighestTime":"凌晨2点"},
			"armory":{"itemName":"重型突击背心","placeName":"防具台","requiredLevel":3,
				"totalMaterialValue":230000,"singlePrice":463446,"profit":230056,"yesterdayHighestTime":"凌晨0点"}
		},
		"smpData":[
			{"itemName":"移动电缆","currectPrice":199791,"yesterdayHighestPrice":348291,
			 "yesterdayHighestTime":"上午8点","yesterdayLowestPrice":210591,"yesterdayLowestTime":"晚上10点"}
		],
		"hapData":[{"objectName":"4.6 x 30mm AP ST","currectPrice":5636,"itemGrade":5}],
		"dtaData":[{"itemName":"5.45x39mm BT","profit":43894.2},{"itemName":"9x19mm RIP","profit":17786}]
	}}`
	ov, err := parseKkrbOverview([]byte(fixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ov.Passwords) != 3 {
		t.Fatalf("密码条数=%d want 3", len(ov.Passwords))
	}
	if ov.Passwords[0].Map != "零号大坝" || ov.Passwords[0].Code != "7353" || ov.Passwords[0].Updated != "2026-10-31" {
		t.Errorf("首条密码不对: %+v", ov.Passwords[0])
	}
	if len(ov.Crafts) != 2 || ov.Crafts[0].Place != "技术中心" || ov.Crafts[0].SellHint != "凌晨2点" {
		t.Errorf("制造数据不对: %+v", ov.Crafts)
	}
	if len(ov.Materials) != 1 || ov.Materials[0].High != 348291 || ov.Materials[0].HighTime != "上午8点" {
		t.Errorf("材料数据不对: %+v", ov.Materials)
	}
	if len(ov.AmmoHot) != 1 || ov.AmmoHot[0].Item != "4.6 x 30mm AP ST" {
		t.Errorf("热门子弹不对: %+v", ov.AmmoHot)
	}
	// 兑换利润要按利润降序
	if len(ov.AmmoProfit) != 2 || ov.AmmoProfit[0].Item != "5.45x39mm BT" {
		t.Errorf("子弹利润排序不对: %+v", ov.AmmoProfit)
	}
}

// 接口报错（code!=1）要变成 error，而不是空数据。
func TestParseKkrbOverviewError(t *testing.T) {
	if _, err := parseKkrbOverview([]byte(`{"code":-101,"msg":"系统繁忙"}`)); err == nil {
		t.Fatal("code=-101 应报错")
	}
	if _, err := parseKkrbOverview([]byte(`not json`)); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}

// 外部知识库目录：首行 keys: 声明关键词，正文为内容；mtime 变了会重读。
func TestLoadKBDir(t *testing.T) {
	dir := t.TempDir()
	card := "keys: 撤离点, 撤离路线\n---\n零号大坝的撤离点有三个，分别在西北、东南、地下通道。\n"
	if err := os.WriteFile(filepath.Join(dir, "extract.md"), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	// 没有 keys 声明的文件应被忽略
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte("随便写点什么"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := loadKBDir(dir)
	if len(entries) != 1 {
		t.Fatalf("应加载 1 条，得到 %d", len(entries))
	}
	if len(entries[0].Keys) != 2 || entries[0].Keys[0] != "撤离点" {
		t.Errorf("关键词解析不对: %+v", entries[0].Keys)
	}
	if !strings.Contains(entries[0].Text, "西北") {
		t.Errorf("正文解析不对: %q", entries[0].Text)
	}

	// 命中：外部知识卡能被 kbContext 注入
	c := Default()
	c.KBDir = dir
	c.MemoryFile = ""
	b := New(c)
	got := b.kbContext("零号大坝撤离点在哪")
	if !strings.Contains(got, "西北") {
		t.Errorf("外部知识卡未被注入: %s", got)
	}
	// 空目录路径不 panic
	if e := loadKBDir(""); e != nil {
		t.Errorf("空目录应返回 nil，得到 %d 条", len(e))
	}
	if e := loadKBDir(filepath.Join(dir, "不存在")); e != nil {
		t.Errorf("不存在的目录应返回 nil，得到 %d 条", len(e))
	}
}

// 改枪方案解析：枪名/码/价格/推荐语，码取最后一段。
func TestParseGunsmithData(t *testing.T) {
	fixture := `{"code":1,"msg":"success","data":{"2":{"1":[
		{"weaponName":"K416突击步枪","gunsmithCode":"K416突击步枪-烽火地带-6JOTK7G00B39LBELGJ9Q9",
		 "modifyPrice":400000,"recommendation":"好用无需多言","provider":"Rxx若晞"},
		{"weaponName":"M249轻机枪","gunsmithCode":"M249轻机枪-烽火地带-6JO362C0FF6C07RDN2TH6",
		 "modifyPrice":520000,"recommendation":"","provider":""}
	]}}}`
	list, err := parseGunsmithData([]byte(fixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("条数=%d want 2", len(list))
	}
	if list[0].Weapon != "K416突击步枪" || list[0].Code != "6JOTK7G00B39LBELGJ9Q9" {
		t.Errorf("解析不对: %+v", list[0])
	}
	if list[0].Price != 400000 || list[0].Note != "好用无需多言" || list[0].Author != "Rxx若晞" {
		t.Errorf("字段不全: %+v", list[0])
	}
	if list[1].Code != "6JO362C0FF6C07RDN2TH6" {
		t.Errorf("第二条码不对: %+v", list[1])
	}
}

// 接口报错要变成 error。
func TestParseGunsmithDataError(t *testing.T) {
	if _, err := parseGunsmithData([]byte(`{"code":-101,"msg":"系统繁忙"}`)); err == nil {
		t.Fatal("code!=1 应报错")
	}
	if _, err := parseGunsmithData([]byte(`not json`)); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}
