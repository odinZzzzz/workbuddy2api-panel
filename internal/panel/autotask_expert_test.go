// autotask_expert_test.go 「体验「X」专家」通用动作的解析与事件组装（纯函数，零网络）。
//
// 覆盖三条口径：
//   - jump_url 抽 expertId：任务中心下发的百分号编码 deeplink 与裸协议两种形态；
//   - 动作解析优先级：静态表优先（WorkBuddy_Valorant 兜底）、jump_url 兜底次之；
//   - 事件组装：chat 链的 agent_task_created 必须带 has_expert/expert_id，
//     使用事件 LOCAL 形态 type 空 + cost 0，craft 形态 mode=craft。
package panel

import (
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestExpertIDFromJumpURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "任务中心下发的 deeplink（百分号编码）",
			url:  "https://www.workbuddy.cn/work/launch/?deeplink=workbuddy%3A%2F%2Fexperts%3FexpertId%3Dex_GDl31WahyIH0&buddy_type=workbuddy",
			want: "ex_GDl31WahyIH0",
		},
		{
			name: "裸协议形态",
			url:  "workbuddy://experts?expertId=ex_WZY136wV5aMt",
			want: "ex_WZY136wV5aMt",
		},
		{
			name: "裸 expert 协议（无 expertId，Valorant 形态）",
			url:  "workbuddy://expert",
			want: "",
		},
		{
			name: "空串",
			url:  "",
			want: "",
		},
		{
			name: "带 id 但不是专家链接",
			url:  "https://www.workbuddy.cn/work/launch/?deeplink=workbuddy%3A%2F%2Ftask%3Fid%3D123",
			want: "",
		},
	}
	for _, c := range cases {
		if got := expertIDFromJumpURL(c.url); got != c.want {
			t.Errorf("%s: expertIDFromJumpURL(%q) = %q, want %q", c.name, c.url, got, c.want)
		}
	}
}

// TestAutoActionForTaskRealTasks 用 2026-10-28 线上真实任务列表的 jump_url 做回归：
// 4 个 expert_use_* 走动态识别，WorkBuddy_Valorant 走静态兜底。
func TestAutoActionForTaskRealTasks(t *testing.T) {
	cases := []struct {
		taskCode string
		title    string
		jumpURL  string
		wantID   string // 期望的专家 id（空 = 静态表兜底，不校验 id 来源）
		static   bool
	}{
		{"expert_use_maidehao", "体验「中国电商运营专家」", "https://www.workbuddy.cn/work/launch/?deeplink=workbuddy%3A%2F%2Fexperts%3FexpertId%3Dex_GDl31WahyIH0&buddy_type=workbuddy", "ex_GDl31WahyIH0", false},
		{"expert_use_douyinyin", "体验「抖音策略师」", "https://www.workbuddy.cn/work/launch/?deeplink=workbuddy%3A%2F%2Fexperts%3FexpertId%3Dex_FbY5nlWTqDod&buddy_type=workbuddy", "ex_FbY5nlWTqDod", false},
		{"expert_use_shangjia_xuanchuan", "体验「商家宣传页与海报设计师」", "https://www.workbuddy.cn/work/launch/?deeplink=workbuddy%3A%2F%2Fexperts%3FexpertId%3Dex_Weo3x2EZuSDm&buddy_type=workbuddy", "ex_Weo3x2EZuSDm", false},
		{"expert_use_bowangwang", "体验「直播电商教练」", "https://www.workbuddy.cn/work/launch/?deeplink=workbuddy%3A%2F%2Fexperts%3FexpertId%3Dex_WZY136wV5aMt&buddy_type=workbuddy", "ex_WZY136wV5aMt", false},
		{"WorkBuddy_Valorant", "体验「无畏契约拼豆专家」", "workbuddy://expert", "", true},
	}
	for _, c := range cases {
		task := &upstream.Task{TaskCode: c.taskCode, Title: c.title, JumpURL: c.jumpURL}
		act := autoActionForTask(task)
		if act == nil {
			t.Fatalf("%s: 期望解析出动作，得到 nil", c.taskCode)
		}
		if act.TaskCode != c.taskCode {
			t.Errorf("%s: TaskCode = %q", c.taskCode, act.TaskCode)
		}
		if act.run == nil {
			t.Errorf("%s: run 为空", c.taskCode)
		}
		isStatic := autoActionFor(c.taskCode) != nil
		if isStatic != c.static {
			t.Errorf("%s: 静态命中 = %v，期望 %v", c.taskCode, isStatic, c.static)
		}
		if c.wantID != "" && !strings.Contains(act.Desc, c.wantID) {
			t.Errorf("%s: Desc 未含专家 id %s，得到 %q", c.taskCode, c.wantID, act.Desc)
		}
	}
}

// TestAutoActionForTaskNoAction 无动作的任务与空任务必须返回 nil
// （调用方靠它决定「展示操作指引」而不是报错）。
func TestAutoActionForTaskNoAction(t *testing.T) {
	if act := autoActionForTask(nil); act != nil {
		t.Errorf("nil 任务应为 nil，得到 %+v", act)
	}
	// wb_wechat_oa_subscribe_task 需真实扫码关注，jump_url 也无 expertId。
	task := &upstream.Task{
		TaskCode: "wb_wechat_oa_subscribe_task",
		Title:    "关注「腾讯 WorkBuddy」官方公众号",
		JumpURL:  "workbuddy://wechat/oa",
	}
	if act := autoActionForTask(task); act != nil {
		t.Errorf("无专家 id 的任务应为 nil，得到 %+v", act)
	}
}

// TestBuildExpertUseEvents 事件组装（LOCAL 形态）：chat 链注入 has_expert，
// 使用事件 type 空、cost 0。
func TestBuildExpertUseEvents(t *testing.T) {
	ex := upstream.MarketExpert{
		ExpertID: "ex_GDl31WahyIH0", ExpertType: "agent",
		DisplayNameZH: "卖得好", ProfessionZH: "中国电商运营专家",
		Version: "1.0.1", Categories: []any{"05-Ecommerce"},
	}
	const conv, req = "wb2api-conv-1", "cmb-0123456789abcdef0123456789abcdef"

	events := buildExpertUseEvents(ex, conv, req)
	if len(events) == 0 {
		t.Fatal("事件为空")
	}
	var created, use map[string]any
	for _, ev := range events {
		switch ev["eventCode"] {
		case "agent_task_created":
			created = ev
		case "expert_actual_use":
			use = ev
		}
	}
	if created == nil {
		t.Fatal("缺 agent_task_created")
	}
	if created["has_expert"] != true {
		t.Errorf("agent_task_created.has_expert = %v", created["has_expert"])
	}
	if created["expert_id"] != ex.ExpertID {
		t.Errorf("agent_task_created.expert_id = %v", created["expert_id"])
	}
	if created["expert_name"] != ex.DisplayNameZH {
		t.Errorf("agent_task_created.expert_name = %v", created["expert_name"])
	}
	if use == nil {
		t.Fatal("缺 expert_actual_use")
	}
	if use["id"] != ex.ExpertID || use["requestId"] != req || use["conversationId"] != conv {
		t.Errorf("使用事件锚点不对: %v", use)
	}
	if use["mode"] != "LOCAL" {
		t.Errorf("mode = %v，期望 LOCAL", use["mode"])
	}
	if use["type"] != "" {
		t.Errorf("type 应为空，得到 %v", use["type"])
	}
	if use["cost"] != 0 {
		t.Errorf("cost 应为 0，得到 %v", use["cost"])
	}
}

// TestCraftFallbackEvent craft 兜底形态：只补使用事件、复用同一 conversationId/requestId
// （不重发对话），mode=craft 且 cost 非零——与 LOCAL 形态区分开，供判据未实测时兜底。
func TestCraftFallbackEvent(t *testing.T) {
	ex := upstream.MarketExpert{ExpertID: "ex_FbY5nlWTqDod", ExpertType: "agent", DisplayNameZH: "斗音音"}
	const conv, req = "wb2api-conv-2", "cmb-fedcba9876543210fedcba9876543210"

	ev := upstream.DesktopExpertActualUseEvent(ex, conv, req)
	if ev["eventCode"] != "expert_actual_use" {
		t.Errorf("eventCode = %v", ev["eventCode"])
	}
	if ev["mode"] != "craft" {
		t.Errorf("mode = %v，期望 craft", ev["mode"])
	}
	if ev["cost"] == 0 {
		t.Errorf("craft 形态 cost 不应为 0")
	}
	if ev["requestId"] != req || ev["conversationId"] != conv {
		t.Errorf("锚点不对: %v", ev)
	}
}

// TestNeedExpertAccept accept 前置的判定口径：只有「未接受」才需要，
// 已接受/已完成/已领取都必须跳过（幂等，不重复写上游）。
func TestNeedExpertAccept(t *testing.T) {
	cases := []struct {
		name string
		task *upstream.Task
		want bool
	}{
		{"nil 任务", nil, false},
		{"未接受", &upstream.Task{TaskCode: "expert_use_maidehao", AcceptStatus: "not_accepted"}, true},
		{"状态缺失", &upstream.Task{TaskCode: "expert_use_maidehao"}, true},
		{"已接受", &upstream.Task{TaskCode: "expert_use_maidehao", AcceptStatus: "accepted"}, false},
		{"已完成", &upstream.Task{TaskCode: "expert_use_maidehao", AcceptStatus: "completed"}, false},
		{"已领取", &upstream.Task{TaskCode: "expert_use_maidehao", AcceptStatus: "claimed", Claimed: true}, false},
	}
	for _, c := range cases {
		if got := needExpertAccept(c.task); got != c.want {
			t.Errorf("%s: needExpertAccept = %v，期望 %v", c.name, got, c.want)
		}
	}
}
