package upstream

import (
	"strings"
	"testing"
)

// 渠道校验的识别与分类（B 层）。
//
// 现场（2026-10）：上游对 chat 端点加强渠道校验，被校验到的请求回 400 + 渠道校验业务码
// + 「没通过渠道校验，可能不是官方客户端发出的」文案。危险之处在于**同一个封套可能带
// 历史遗留的 msg:"request illegal"**，而那条历史语义是「账号被封 → 硬禁用」——误判会让
// 好号在面板上显示「已禁用（需重新登录）」，用户重扫也没用（凭证从来没问题）。
//
// 业务码一律拼接构造：它正是判据本身，手抄整串极易写错，写错后测试会「通过」一个不存在的
// 判据。
var (
	chCode       = "1" + "1" + "1" + "2" + "8"
	chCodeDashed = "1" + "1" + "-" + "1" + "2" + "8"
)

// channelCheckEnvelope 一次渠道校验的真实封套：业务码 + 渠道校验文案 + 历史遗留的
// request illegal msg（后者正是误禁用账号的触发点）。
//
// codeValue 是**已编码好的 JSON 值**：数字形态直接给码，字符串形态自己带引号——
// 带横线的写法裸写不是合法 JSON，必须先加引号（否则 Unmarshal 失败，判据永远不命中）。
func channelCheckEnvelope(codeValue string) string {
	return `{"code":` + codeValue + `,"msg":"request illegal","requestId":"0a1b2c",` +
		`"displayTips":{"zh":"这次调用没通过渠道校验，可能不是官方客户端发出的。",` +
		`"en":"This call failed channel verification, possibly because it was not sent from an official client."}}`
}

// TestHasChannelCheckCode 业务码识别：数字与字符串两种 JSON 写法都认；相邻码不算。
func TestHasChannelCheckCode(t *testing.T) {
	for _, body := range []string{
		`{"code":` + chCode + `}`,
		`{"code":"` + chCode + `"}`,
		`{"code":"` + chCodeDashed + `"}`, // 带横线写法只能是字符串形态（裸写不是合法 JSON）
	} {
		if !HasChannelCheckCode(body) {
			t.Errorf("应识别为渠道校验码: %s", body)
		}
	}
	for _, body := range []string{
		``,
		`{"code":11140,"msg":"request illegal"}`, // 账号封禁码，不是渠道校验
		`{"code":11102,"msg":"service info not found"}`,
	} {
		if HasChannelCheckCode(body) {
			t.Errorf("不应识别为渠道校验码: %s", body)
		}
	}
}

// TestIsChannelCheck 双条件：业务码 + 渠道校验文案。只有码、或只有 request illegal 都不算。
func TestIsChannelCheck(t *testing.T) {
	if !IsChannelCheck(channelCheckEnvelope(chCode)) {
		t.Fatal("中文「渠道校验」文案必须被识别")
	}
	if !IsChannelCheck(`{"code":` + chCode + `,"displayTips":{"en":"not sent from an official client"}}`) {
		t.Error("英文 official client 文案必须被识别（中文可能被上游换掉）")
	}
	if IsChannelCheck(`{"code":` + chCode + `,"msg":"request illegal"}`) {
		t.Error("没有渠道校验文案时不该命中（否则会把内容审核路径也吞掉）")
	}
}

// TestIsChannelCheckDoesNotSwallowContentBlock 同码不同因：内容审核拦截必须保持原行为
// （不罚号 + 走降级重试）。只认码会把这条唯一的救回路径一并废掉。
func TestIsChannelCheckDoesNotSwallowContentBlock(t *testing.T) {
	for _, body := range []string{
		`{"code":` + chCode + `,"msg":"Illegal API invocation from an unapproved channel"}`,
		`{"code":` + chCode + `,"msg":"blocked by security policy"}`,
	} {
		if IsChannelCheck(body) {
			t.Fatalf("内容拦截被误判成渠道校验: %s", body)
		}
		if got := Classify(400, body); got != ErrContentBlocked {
			t.Fatalf("内容拦截分类被影响: %v", got)
		}
	}
}

// TestClassifyRoutesChannelCheckToChannelInvalid 渠道校验归请求级：换号不改变任何头，
// 轮转必然重复失败，且会掩盖真实病因。
func TestClassifyRoutesChannelCheckToChannelInvalid(t *testing.T) {
	for _, codeValue := range []string{chCode, `"` + chCodeDashed + `"`} {
		body := channelCheckEnvelope(codeValue)
		if got := Classify(400, body); got != ErrChannelInvalid {
			t.Errorf("code=%s Classify=%v want ErrChannelInvalid", codeValue, got)
		}
	}
}

// TestClassifyChannelCheckBeatsRequestIllegal 带 request illegal 的渠道校验封套**不得**
// 归账号级 —— 那会走到 applyErrorPolicy 的硬禁用分支（本仓最贵的误判）。
func TestClassifyChannelCheckBeatsRequestIllegal(t *testing.T) {
	body := channelCheckEnvelope(chCode)
	if !strings.Contains(body, "request illegal") {
		t.Fatal("fixture 应包含 request illegal（复现真实封套）")
	}
	if got := Classify(400, body); got == ErrAccountFault {
		t.Fatal("渠道校验被归为账号级故障 → 会被硬禁用")
	}
}

// TestClassifyRealBanStillAccountFault 真正的账号封禁（11140 + request illegal）行为不变：
// 护栏不能把真封禁也放过去。
func TestClassifyRealBanStillAccountFault(t *testing.T) {
	ban := `{"code":11140,"msg":"request illegal","displayMsg":{"en":"Your account has been restricted due to unusual activity"}}`
	if got := Classify(400, ban); got != ErrAccountFault {
		t.Fatalf("真封禁仍须归账号级，got %v", got)
	}
}

// realChannelRejectionBody 2026-10-27 真机抓到的 11-128 响应（cn + DSH 身份句触发）。
// 关键点：同一个封套**同时**给出内容/指纹措辞（msg 里的 unapproved channel）与渠道措辞
// （displayTips 里的 channel verification / official client）。两套措辞并存正是误判的来源。
func realChannelRejectionBody(code string) string {
	return `{"code":` + code + `,"msg":"Illegal API invocation from an unapproved channel","requestId":"417f88002c00b26d9ab2ad8635c8c15f",` +
		`"displayMsg":{"en":"Request blocked. Please send it again","zh":"请求被拦截，请重新发送"},` +
		`"displayTips":{"en":"This call failed channel verification, possibly because it was not sent from an official client. Please resend from the official client.",` +
		`"zh":"这次调用没通过渠道校验，可能不是官方客户端发出的。请用官方版本重新发送。"},` +
		`"actions":["EDIT_INPUT","SUBMIT_FEEDBACK"]}`
}

// TestRealEnvelopeStaysContentBlocked 真机封套必须仍归内容拦截：它要能走降级重试救回请求。
// 回归背景：第一版 IsChannelCheck 只看「渠道措辞」，于是把这条判成渠道校验 → 硬 400 给客户端，
// 把「400 → 换中性提示词 → 200」这条唯一救回路径关掉了（本机实测发现）。
func TestRealEnvelopeStaysContentBlocked(t *testing.T) {
	body := realChannelRejectionBody(chCode)
	if IsChannelCheck(body) {
		t.Fatal("真机封套含内容拦截措辞，不得判成渠道校验（会关掉降级重试）")
	}
	if got := Classify(400, body); got != ErrContentBlocked {
		t.Fatalf("真机封套应归 ErrContentBlocked，got %v", got)
	}
}

// TestChannelCheckWithoutContentMarkers 不含内容措辞的渠道校验仍归请求级（manager 0006 现场：
// 封套带历史遗留的 request illegal，但文案明确是渠道校验 → 只软冷却/透传，绝不禁用）。
func TestChannelCheckWithoutContentMarkers(t *testing.T) {
	body := channelCheckEnvelope(chCode)
	if !IsChannelCheck(body) {
		t.Fatal("无内容措辞的渠道校验应被识别")
	}
	if got := Classify(400, body); got != ErrChannelInvalid {
		t.Fatalf("应归 ErrChannelInvalid，got %v", got)
	}
}
