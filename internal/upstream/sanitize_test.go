package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

const (
	ccIdentity = "You are Claude Code, Anthropic's official CLI for Claude."
	ccBranch   = "Main branch (you will usually use this for PRs)"
	ccHeader   = "x-anthropic-billing-header: cc_version=1.0; cc_entrypoint=cli;"
	// Codex instructions 首段（上游逐字精确指纹，三句缺一不可）。
	codexInstructions = "You are a coding agent running in the Codex CLI, a terminal-based coding assistant. Codex CLI is an open source project led by OpenAI. You are expected to be precise, safe, and helpful."
)

func TestIdentityRewritten(t *testing.T) {
	out := sanitizeText(ccIdentity)
	if !strings.Contains(out, "official CLI tool for Claude.") {
		t.Errorf("identity not rewritten: %q", out)
	}
	if strings.Contains(out, ccIdentity) {
		t.Errorf("original identity still present: %q", out)
	}
}

// 桌面版（claude-desktop-3p / Agent SDK）的身份句以逗号接后继内容，结尾不是句号。
// 回归用例：匹配串曾带结尾句号，导致该形态漏网、指纹原样发上游 → 400 code=11128。
func TestIdentityDesktopVariantRewritten(t *testing.T) {
	in := "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."
	out := sanitizeText(in)
	if strings.Contains(out, "official CLI for Claude") {
		t.Errorf("desktop identity not rewritten: %q", out)
	}
	if !strings.Contains(out, "official CLI tool for Claude, running within the Claude Agent SDK.") {
		t.Errorf("desktop identity suffix not preserved: %q", out)
	}
}

func TestBranchRewritten(t *testing.T) {
	out := sanitizeText(ccBranch)
	if !strings.Contains(out, "Default branch (you will usually use this for PRs)") {
		t.Errorf("branch not rewritten: %q", out)
	}
	if strings.Contains(out, "Main branch") {
		t.Errorf("original branch still present: %q", out)
	}
}

// 反馈句带 Anthropic 仓库链接，上游按整句拦截（实测只留链接或只留半边均不拦）。
// 回归用例：give→provide 一词之差即可绕过。
func TestFeedbackSentenceRewritten(t *testing.T) {
	in := "To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues"
	out := sanitizeText(in)
	if strings.Contains(out, "To give feedback") {
		t.Errorf("feedback sentence not rewritten: %q", out)
	}
	if !strings.Contains(out, "To provide feedback, users should report the issue at https://github.com/anthropics/claude-code/issues") {
		t.Errorf("feedback sentence not rewritten as expected: %q", out)
	}
}

// 上游反探测：请求体里出现裸数字 11128 即整单拦截（与上下文无关）。
// 回归用例：该串会被改写为 11-128 以打断精确匹配。
func TestUpstreamErrorCodeRewritten(t *testing.T) {
	in := "upstream returned code=11128 for this request"
	out := sanitizeText(in)
	if strings.Contains(out, "11128") {
		t.Errorf("error code not rewritten: %q", out)
	}
	if !strings.Contains(out, "11-128") {
		t.Errorf("error code not rewritten as expected: %q", out)
	}
}

// 回归：工具调用消息的 content 常为 null，而旧版 sanitizeMessages 在 content 缺失时
// 直接 continue，整条消息连 tool_calls 一起被跳过 → arguments 里的被拦字符串原样漏出。
func TestToolCallArgumentsSanitized(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "run"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{
				"name":      "Bash",
				"arguments": `{"command":"echo 11128"}`,
			}},
		}},
	}
	if !sanitizeMessages(msgs) {
		t.Fatal("sanitizeMessages 未报告任何改动，tool_calls 被跳过")
	}
	fn := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	got := fn["arguments"].(string)
	if strings.Contains(got, "11128") {
		t.Errorf("tool_call arguments 未被净化: %q", got)
	}
}

func TestBillingHeaderStrippedValueIrrelevant(t *testing.T) {
	out := sanitizeText(ccHeader)
	if strings.Contains(out, "x-anthropic-billing-header") {
		t.Errorf("header not stripped: %q", out)
	}
}

// 附加验证：正常对话里出现 github.com/anthropics/ 链接（但不是反馈句整句）
// 时，预检特征命中（进入净化），但改写层只动精确匹配的整句——普通链接文本
// 不该被改写。同理，既不含 11128 也不含反馈整句的文本原样返回。
func TestNormalAnthropicLinkNotRewritten(t *testing.T) {
	in := "see https://github.com/anthropics/anthropic-cookbook for examples"
	if out := sanitizeText(in); out != in {
		t.Errorf("normal anthropic link should be untouched: %q -> %q", in, out)
	}
}

// Codex instructions 首段：命中预告且整句改写，逐字指纹被破坏、语义保留。
func TestCodexInstructionsRewritten(t *testing.T) {
	out := sanitizeText(codexInstructions)
	if strings.Contains(out, codexInstructions) {
		t.Errorf("codex fingerprint still present: %q", out)
	}
	if !strings.Contains(out, "You are a coding agent running in the Codex CLI tool, a terminal-based coding assistant.") {
		t.Errorf("codex first sentence not rewritten: %q", out)
	}
	// 其余两句原样保留，语义不变。
	if !strings.Contains(out, "Codex CLI is an open source project led by OpenAI.") ||
		!strings.Contains(out, "You are expected to be precise, safe, and helpful.") {
		t.Errorf("codex remaining sentences altered: %q", out)
	}
}

// 预检必须能认出 Codex 特征（此前只含 Claude Code，导致提前放行）。
func TestCodexFingerprintDetected(t *testing.T) {
	if !hasFingerprint(codexInstructions) {
		t.Error("codex fingerprint not detected by precheck")
	}
}

// 非精确变体不应被改写：仅去掉其中一个词即视为已破坏，无需改动。
func TestCodexVariantNotTouched(t *testing.T) {
	in := "You are a coding agent running in a CLI, a terminal-based coding assistant."
	if out := sanitizeText(in); out != in {
		t.Errorf("already-broken variant should be untouched: %q -> %q", in, out)
	}
}

func TestBillingHeaderCaseInsensitive(t *testing.T) {
	alt := "X-Anthropic-Billing-Header: cc_version=1.0;"
	out := strings.ToLower(sanitizeText(alt))
	if strings.Contains(out, "billing") {
		t.Errorf("case-insensitive header not stripped: %q", out)
	}
}

func TestTrailingKVStripped(t *testing.T) {
	out := sanitizeText("...; cc_version=2.0; cc_entrypoint=cli;")
	if strings.Contains(out, "cc_version") || strings.Contains(out, "cc_entrypoint") {
		t.Errorf("trailing kv not stripped: %q", out)
	}
}

func TestExactMatchOnlyVariantNotTouched(t *testing.T) {
	in := "...official CLI for Claude!"
	if out := sanitizeText(in); out != in {
		t.Errorf("variant should be untouched: %q -> %q", in, out)
	}
}

func TestUserFreeTextNotTouched(t *testing.T) {
	in := "please use main branch for this repo"
	if out := sanitizeText(in); out != in {
		t.Errorf("free text should be untouched: %q -> %q", in, out)
	}
}

func TestNoFeatureReturnsSameString(t *testing.T) {
	in := "ordinary user message"
	if out := sanitizeText(in); out != in {
		t.Errorf("no-feature text should pass through unchanged: %q -> %q", in, out)
	}
}

func TestMultimodalTextPartOnly(t *testing.T) {
	imgPart := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "..."}}
	content := []any{
		map[string]any{"type": "text", "text": ccIdentity},
		imgPart,
	}
	out, changed := sanitizeContent(content)
	if !changed {
		t.Fatal("expected change")
	}
	parts := out.([]any)
	txt, _ := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(txt, "CLI tool") {
		t.Errorf("text part not sanitized: %q", txt)
	}
	img, _ := parts[1].(map[string]any)
	if img["type"] != "image" || img["source"].(map[string]any)["data"] != "..." {
		t.Error("image part modified")
	}
}

// 集成：完整请求体经 PrepareBodyOpt 净化后无残留指纹，且 stream/tool_choice 行为不受影响。
func TestPrepareBodyOptSanitizesSystem(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":"` + ccIdentity + ` ` + ccHeader + `"},` +
		`{"role":"user","content":"hi"}]}`)
	out := PrepareBodyOpt(body, true)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["stream"] != true {
		t.Error("stream not forced")
	}
	msgs := obj["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(sys, "x-anthropic-billing-header") || strings.Contains(sys, ccIdentity) || strings.Contains(sys, ccBranch) {
		t.Errorf("fingerprints remain: %q", sys)
	}
	if !strings.Contains(sys, "CLI tool") {
		t.Errorf("rewrite missing: %q", sys)
	}
}

func TestPrepareBodyOptDisabledPreservesFingerprints(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	out := PrepareBodyOpt(body, false)
	if !strings.Contains(string(out), ccIdentity) {
		t.Error("sanitize=false should preserve fingerprints")
	}
	// 但 stream 仍强制
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	if obj["stream"] != true {
		t.Error("stream should still be forced")
	}
}

// PrepareBody 默认行为 = 开启脱敏（保持向后兼容）。
func TestPrepareBodyDefaultSanitizes(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	out := PrepareBodyOpt(body, true)
	if strings.Contains(string(out), ccIdentity) {
		t.Error("PrepareBody default should sanitize")
	}
}

// 出站边界集成：ChatStream 发往上游的 wire body 必须无残留指纹。
func TestChatStreamWireBodySanitized(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints.Store(true)
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":"` + ccIdentity + ` ` + ccHeader + `"},` +
		`{"role":"user","content":"hi"}]}`)
	rc, status, respBody, err := c.ChatStream(acct, body, "", ChatMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d: %s", status, respBody)
	}
	// 上游收到的 body：stream 强制 + 指纹已净化
	var obj map[string]any
	if err := json.Unmarshal(gotBody, &obj); err != nil {
		t.Fatalf("wire body not json: %v", err)
	}
	if obj["stream"] != true {
		t.Error("wire body stream not forced")
	}
	sys, _ := obj["messages"].([]any)[0].(map[string]any)["content"].(string)
	for _, fp := range []string{"x-anthropic-billing-header", ccIdentity, ccBranch} {
		if strings.Contains(sys, fp) {
			t.Errorf("wire body contains fingerprint %q: %q", fp, sys)
		}
	}
}

// 出站边界（Codex 场景）：CPA 把 instructions 折成 role=system 的首条消息，
// 净化后 wire body 不得残留 Codex 逐字指纹；其余消息不受影响。
func TestChatStreamWireBodyCodexInstructionsSanitized(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints.Store(true)
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"kimi-k3","messages":[` +
		`{"role":"system","content":"` + codexInstructions + `"},` +
		`{"role":"user","content":"say ok"}]}`)
	rc, status, respBody, err := c.ChatStream(acct, body, "", ChatMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d: %s", status, respBody)
	}
	var obj map[string]any
	if err := json.Unmarshal(gotBody, &obj); err != nil {
		t.Fatalf("wire body not json: %v", err)
	}
	sys, _ := obj["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(sys, codexInstructions) ||
		strings.Contains(sys, "in the Codex CLI, a terminal-based coding assistant.") {
		t.Errorf("wire body still contains codex fingerprint: %q", sys)
	}
	if !strings.Contains(sys, "running in the Codex CLI tool, a terminal-based") {
		t.Errorf("codex rewrite missing on wire: %q", sys)
	}
	user, _ := obj["messages"].([]any)[1].(map[string]any)["content"].(string)
	if user != "say ok" {
		t.Errorf("user message altered: %q", user)
	}
}

// 出站边界：关闭脱敏后 wire body 原样保留指纹（验证开关真实有效）。
func TestChatStreamWireBodySanitizeDisabled(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints.Store(false)
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	rc, status, _, err := c.ChatStream(acct, body, "", ChatMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d", status)
	}
	if !strings.Contains(string(gotBody), ccIdentity) {
		t.Error("sanitize disabled should preserve fingerprint on wire")
	}
}

// TestSanitizeMessagesScrubsReasoningField thinking.go 的回填会把客户端送来的
// reasoning_content 镜像进 reasoning 字段；此前只洗 content / reasoning_content /
// tool_calls → 镜像进 reasoning 的指纹（裸 "11128" 这类反探测串）原样出站，
// 而请求体里出现裸 11128 本身就是上游整单拦截条件。
func TestSanitizeMessagesScrubsReasoningField(t *testing.T) {
	ms := []any{map[string]any{
		"role":      "assistant",
		"content":   "hi",
		"reasoning": "upstream said 11128",
	}}
	if !sanitizeMessages(ms) {
		t.Fatal("reasoning 字段里的指纹未被净化")
	}
	got, _ := ms[0].(map[string]any)["reasoning"].(string)
	if strings.Contains(got, "11128") {
		t.Fatalf("reasoning 仍含裸指纹: %q", got)
	}
}

// newTestUpstream 起一个假上游并捕获请求。
func newTestUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h)
}

// ── 句式级指纹（DSH 自我介绍句 / GUI 描述段 / 品牌缩减）─────────────────────
//
// 这三条规则来自两家的真机实测（2026-10）：上游对「自我介绍句式 + 品牌」做句式判定，
// 不是对品牌名判定；同一句在 system / developer / assistant 里 400，在 user / tool
// 里 200。因此规则必须①认句式而非品牌、②只在身份载体上生效。
const (
	dshIdentity = "You are an AI agent powered by DeepSeek Harness."
	dshCoding   = "You are a coding agent powered by the deepseek-v4.1-flash model."
	dshNeutral  = "You are a helpful AI assistant."
)

// TestDshIdentityRewritten 两句真实原文整句替换为中性句（不留残句）。
func TestDshIdentityRewritten(t *testing.T) {
	for _, in := range []string{dshIdentity, dshCoding} {
		if out := sanitizeText(in); out != dshNeutral {
			t.Errorf("%q -> %q want %q", in, out, dshNeutral)
		}
	}
}

// TestDshIdentityBuiltOnVariant built on 变体同样命中（按句式而非品牌判定）。
func TestDshIdentityBuiltOnVariant(t *testing.T) {
	in := "You are an AI assistant built on DeepSeek."
	if out := sanitizeText(in); out != dshNeutral {
		t.Errorf("%q -> %q", in, out)
	}
}

// TestDshIdentityCaseInsensitive 全大写变体也必须命中：预检的 strings.Contains 快速
// 路径是大小写敏感的，只在特征表里补小写串会让改写层永远执行不到（两家都踩过）。
func TestDshIdentityCaseInsensitive(t *testing.T) {
	in := "YOU ARE AN AI AGENT POWERED BY DEEPSEEK HARNESS."
	if out := sanitizeText(in); out != dshNeutral {
		t.Errorf("upper-case variant missed: %q -> %q", in, out)
	}
	if !hasFingerprint("you are an ai agent powered by x.") {
		t.Error("hasFingerprint 应认得小写句式")
	}
}

// TestDshModelNameWithDotsFullyReplaced 模型名自带点（deepseek-v4.1-flash）：句尾必须
// 按「点 + 空白/行尾」收口，否则会停在 "v4."、把 "1-flash model." 留在正文里。
func TestDshModelNameWithDotsFullyReplaced(t *testing.T) {
	out := sanitizeText(dshCoding)
	if strings.Contains(out, "1-flash") || strings.Contains(out, "model.") {
		t.Errorf("句子只替换了一半: %q", out)
	}
}

// TestDshFollowingInstructionsPreserved 句号后的内容原样保留（那是客户端的行为契约），
// 且句尾空白要接回去——吃掉的话 "assistant.Then answer briefly." 会粘成一句。
func TestDshFollowingInstructionsPreserved(t *testing.T) {
	in := dshIdentity + "\nUse the read tool to inspect files.\nDo not guess."
	out := sanitizeText(in)
	if !strings.Contains(out, "Use the read tool to inspect files.\nDo not guess.") {
		t.Errorf("后文被改动: %q", out)
	}
	if strings.Contains(out, "DeepSeek Harness") {
		t.Errorf("指纹残留: %q", out)
	}
	in2 := dshIdentity + " Then answer briefly."
	if out2 := sanitizeText(in2); !strings.Contains(out2, "assistant. Then answer briefly.") {
		t.Errorf("句尾空白被吃掉: %q", out2)
	}
}

// TestDshRewriteIdempotent 幂等：同一 body 会被预处理两次（PrepareBody 与 ChatStream）。
func TestDshRewriteIdempotent(t *testing.T) {
	once := sanitizeText(dshIdentity)
	if twice := sanitizeText(once); twice != once {
		t.Errorf("非幂等: %q -> %q", once, twice)
	}
}

// TestDshPlainTextNotTouched 反例：普通自我介绍与无关句子不得改写。
func TestDshPlainTextNotTouched(t *testing.T) {
	for _, in := range []string{
		"You are a helpful assistant.",
		"You are a helpful AI assistant built for coding tasks",
		"The agent powered by the harness failed.",
		"please use main branch for this repo",
	} {
		if out := sanitizeText(in); out != in {
			t.Errorf("不应改写: %q -> %q", in, out)
		}
	}
}

// TestDshGuiParagraphRewritten GUI 描述段：上游按「A 句 + B 句连续出现」整段判定，
// 改任一句都会失配。这里只改 A 句尾的语序，品牌与句意不变。
func TestDshGuiParagraphRewritten(t *testing.T) {
	in := `You are interacting with the user through the DeepSeek Harness Web GUI whose URL the runtime context names. When the user refers to "this page", they mean this GUI.`
	out := sanitizeText(in)
	if !strings.Contains(out, "whose URL is named in the runtime context.") {
		t.Errorf("GUI 段句尾未改写: %q", out)
	}
	if strings.Contains(out, "whose URL the runtime context names") {
		t.Errorf("原句尾仍在: %q", out)
	}
	if !strings.Contains(out, `When the user refers to "this page", they mean this GUI.`) {
		t.Errorf("后一句被改动: %q", out)
	}
}

// TestBrandShrinkKeepsIdentifiers 品牌缩减只吃空格/制表符：路径与下划线标识符必须
// 原样保留（带连字符/下划线的形态在实测里是放行的）。
func TestBrandShrinkKeepsIdentifiers(t *testing.T) {
	in := "DeepSeek Harness lives at /opt/deepseek-harness/app and deepseek_harness."
	out := sanitizeText(in)
	if strings.Contains(out, "DeepSeek Harness") {
		t.Errorf("品牌未缩减: %q", out)
	}
	if !strings.Contains(out, "/opt/deepseek-harness/app") || !strings.Contains(out, "deepseek_harness") {
		t.Errorf("路径/标识符被改坏: %q", out)
	}
}

// TestSanitizeMessagesScopeByRole 作用域：只有身份载体（system / developer /
// assistant）走句式层；user / tool 是用户数据，实测上游对它们放行，改了只会让模型
// 看到与磁盘不一致的内容。
func TestSanitizeMessagesScopeByRole(t *testing.T) {
	ms := []any{
		map[string]any{"role": "system", "content": dshIdentity},
		map[string]any{"role": "developer", "content": dshIdentity},
		map[string]any{"role": "assistant", "content": dshIdentity},
		map[string]any{"role": "user", "content": dshIdentity},
		map[string]any{"role": "tool", "content": dshIdentity},
	}
	if !sanitizeMessages(ms) {
		t.Fatal("sanitizeMessages 应报告改动")
	}
	for i, want := range []string{dshNeutral, dshNeutral, dshNeutral, dshIdentity, dshIdentity} {
		got, _ := ms[i].(map[string]any)["content"].(string)
		if got != want {
			t.Errorf("role=%v content=%q want %q", ms[i].(map[string]any)["role"], got, want)
		}
	}
}

// TestToolCallArgsNotIdentityScoped tool_calls 参数是应用数据，不做句式改写
// （逐字表与 header 剥离照旧生效）。
func TestToolCallArgsNotIdentityScoped(t *testing.T) {
	ms := []any{map[string]any{
		"role": "assistant",
		"tool_calls": []any{map[string]any{
			"function": map[string]any{"name": "f", "arguments": `{"note":"` + dshIdentity + `"}`},
		}},
	}}
	if sanitizeMessages(ms) {
		t.Fatal("tool_calls 参数不应被句式层改写")
	}
}

// TestPrepareBodySanitizesDshPrompt 出站边界：PrepareBody 之后 wire body 里不得残留
// DSH 自我介绍句；developer 角色（DSH 的运行时上下文用它承载）同样要覆盖。
func TestPrepareBodySanitizesDshPrompt(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash","messages":[` +
		`{"role":"system","content":"` + dshIdentity + `"},` +
		`{"role":"developer","content":"` + dshCoding + `"}]}`)
	out := PrepareBodyOpt(body, true)
	s := string(out)
	if strings.Contains(s, "DeepSeek Harness") {
		t.Errorf("wire body 仍含 DSH 指纹: %s", s)
	}
	if strings.Count(s, dshNeutral) != 2 {
		t.Errorf("两句都该替换为中性句: %s", s)
	}
}

// 11-128 渠道校验的四类触发源（依据 curo22/dsh-wb400-guard 真机 A/B）：
// 本用例钉住我们**新增**的两条 + 零宽空格品牌变体，避免以后被顺手删掉。
func TestSanitizeChannelGateTriggers(t *testing.T) {
	// 1) @ 文件引用句：真机结论「去掉任意一个 @ 就 200」
	in := "Tokens prefixed with @ are paths the user explicitly referenced. " +
		"Relative paths resolve from the workspace root; absolute paths identify files or directories on the host."
	got := sanitizeText(in)
	if strings.Contains(got, "@") {
		t.Errorf("@ 没被替换掉：%q", got)
	}
	if !strings.Contains(got, "(at)") {
		t.Errorf("@ 应换成 (at) 而不是直接删：%q", got)
	}
	if !strings.Contains(got, "prefixed with") {
		t.Errorf("句子其余部分应保留：%q", got)
	}

	// 2) GUI 段 B 句（system 尾部那条）
	gui := `Current DeepSeek Harness Web GUI URL: "http://127.0.0.1:43120".`
	got2 := sanitizeText(gui)
	if strings.Contains(got2, "Web GUI URL") || strings.Contains(got2, "127.0.0.1") {
		t.Errorf("GUI URL 段没被中性化：%q", got2)
	}
	if !strings.Contains(got2, "runtime context") {
		t.Errorf("应换成中性句：%q", got2)
	}

	// 3) 品牌词夹零宽空格（官方发布版就是 DeepSeek\u200B Harness）
	zw := "powered by DeepSeek\u200b Harness.\n"
	got3 := sanitizeText(zw)
	if strings.Contains(got3, "\u200b") {
		t.Errorf("零宽空格品牌没被缩减：%q", got3)
	}

	// 4) 作用域：user 数据（identity=false）里的 @ 不许改写
	userText := "看这个 @file.txt 和 @dir/ 里有什么"
	if got4 := sanitizeTextScoped(userText, false); got4 != userText {
		t.Errorf("user 数据不该被改写：%q", got4)
	}

	// 5) 幂等：重复净化不再变化
	if again := sanitizeText(got); again != got {
		t.Errorf("净化不幂等：%q -> %q", got, again)
	}
}

// GUI 段措辞漂移 + 全角 ＠：这两条来自对 0.2.0-rc.2 内核与在线 README 的码点级核对，
// 老规则（要求 "whose URL the runtime context names."）在新形态上会漏，钉住它们。
func TestSanitizeDriftedGuiForms(t *testing.T) {
	// 新形态：…Web GUI at ${webUrl}（Harness 甚至被 purge 成 DeepSeek Web GUI）
	for _, in := range []string{
		"You are interacting with the user through the DeepSeek Web GUI at http://127.0.0.1:43120.",
		"You are interacting with the user through the DeepSeek\u200b Harness Web GUI at http://127.0.0.1:43120.",
	} {
		got := sanitizeText(in)
		if strings.Contains(got, "Web GUI at") || strings.Contains(got, "127.0.0.1") {
			t.Errorf("漂移形态没被中性化：%q", got)
		}
		if !strings.Contains(got, "runtime context") {
			t.Errorf("应换成中性句：%q", got)
		}
	}

	// 全角 ＠ 也要换（网关会把全角折回半角再判定）
	full := "Tokens prefixed with \uFF20 are paths the user explicitly referenced."
	if got := sanitizeText(full); strings.Contains(got, "\uFF20") {
		t.Errorf("全角 ＠ 没被替换：%q", got)
	}

	// GUI URL 段（漂移后没有 Harness 词）
	urlSeg := `Current Web GUI URL: "http://127.0.0.1:43120".`
	// 注意：中性句本身也含 "GUI URL" 字样，所以只断言原 URL 与 "Current …" 前缀消失
	if got := sanitizeText(urlSeg); strings.Contains(got, "43120") || strings.Contains(got, "Current") {
		t.Errorf("GUI URL 段没被中性化：%q", got)
	}
}
