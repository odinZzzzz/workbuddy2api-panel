package upstream

import (
	"strings"
	"testing"
)

// 探测层（probeMask）的对拍测试。
//
// 探测层是纯优化：把「要不要清洗」从 (?i) 正则换成字面量扫描。它必须满足两条不变量，
// 否则优化就变成功能回归：
//  1. **超集**：正则能匹配的文本，探测必须也命中（假阴性 = 指纹原样出站）。
//     反过来允许（假阳性），代价只是多跑一次正则。
//  2. **等价**：净化结果与优化前的实现逐字节相同（唯一例外是 TrimSpace 收窄，单列用例）。

// legacyHasFingerprint 是优化前的预检实现（逐条正则）。保留副本专供对拍。
func legacyHasFingerprint(text string) bool {
	for _, f := range sanitizeFeatures {
		if strings.Contains(text, f) {
			return true
		}
	}
	if sanitizeDshIdentityRe.MatchString(text) ||
		sanitizeDshGuiRe.MatchString(text) ||
		sanitizeBrandRe.MatchString(text) {
		return true
	}
	return sanitizeBareHdrRe.MatchString(text)
}

// legacySanitize 是优化前的净化实现（(?i) 正则全量跑 + 无条件 TrimSpace）。
func legacySanitize(text string) string {
	if !legacyHasFingerprint(text) {
		return text
	}
	for _, rw := range sanitizeRewrites {
		text = strings.ReplaceAll(text, rw[0], rw[1])
	}
	text = sanitizeDshIdentityRe.ReplaceAllString(text, sanitizeDshIdentityRepl)
	text = sanitizeDshGuiRe.ReplaceAllString(text, sanitizeDshGuiRepl)
	text = sanitizeBrandRe.ReplaceAllString(text, sanitizeBrandRepl)
	if sanitizeHdrRe.MatchString(text) {
		text = sanitizeHdrRe.ReplaceAllString(text, "")
	}
	if strings.Contains(text, "cc_") {
		prev := ""
		for prev != text {
			prev = text
			text = sanitizeKvRe.ReplaceAllString(text, "")
		}
	}
	text = sanitizeBareHdrRe.ReplaceAllString(text, sanitizeHdrHint)
	return strings.TrimSpace(text)
}

// probeCorpus 覆盖各类形态；不含首尾空白（TrimSpace 的差异由专门用例覆盖）。
func probeCorpus() []string {
	hdrName := sanitizeFeatures[0] // 键名从实现派生，避免手抄（第一次就抄错过）
	hdrMixed := strings.ToUpper(hdrName[:1]) + hdrName[1:]
	gui := "You are interacting with the user through the DeepSeek Harness Web GUI whose URL the runtime context names."
	return []string{
		"",
		"ordinary user message",
		"please use main branch for this repo",
		dshIdentity,
		dshCoding,
		strings.ToUpper(dshIdentity),
		"You are an AI assistant built on DeepSeek.",
		"You are an assistant built on the deepseek-v4.1-flash model.",
		"You are an AI agent powered by DeepSeek Harness. Then answer briefly.",
		"You are a helpful AI assistant.",
		"You are free to use tools built by third parties.",
		"The agent powered by the harness failed.",
		"powered by OpenAI",
		gui,
		gui + ` When the user refers to "this page", they mean this GUI.`,
		"DeepSeek Harness",
		"DeepSeek  Harness",
		"deepseek harness",
		"DeepSeek-Harness",
		"/opt/deepseek-harness/app",
		"deepseek_harness",
		"upstream returned code=11-128 for this request",
		ccIdentity,
		codexInstructions,
		ccBranch,
		hdrName,
		hdrMixed + ": ",
		"tool output mentions " + hdrName + " verbatim",
		"...; ",
		"工具说明：读取文件、写入文件、运行命令。",
		"工具说明 " + dshIdentity + " 结尾",
		"混合 DeepSeek Harness 与 " + dshIdentity + " 以及 " + ccIdentity,
	}
}

// TestProbeHintsDerivedFromPatterns 防漂移：每条探针都必须是其规则匹配文本的子串。
// 手抄探针第一次就出过事（26 字符的键名抄成 22 字符的缩写 → 混合大小写 header 漏探测），
// 所以 header 那条改成从正则派生，其余在这里钉住。
func TestProbeHintsDerivedFromPatterns(t *testing.T) {
	want := strings.ToLower(strings.TrimPrefix(sanitizeBareHdrRe.String(), "(?i)"))
	if sanitizeHdrHint != want {
		t.Fatalf("hdr hint=%q want %q（必须与 sanitizeBareHdrRe 同源）", sanitizeHdrHint, want)
	}
	if !strings.Contains(strings.ToLower(sanitizeDshIdentityPat), sanitizeYouAreHint) {
		t.Errorf("身份句探针 %q 不在模式里", sanitizeYouAreHint)
	}
	for _, v := range []string{verbPoweredBy, verbBuiltOn, verbBuiltBy, verbCreatedBy, verbDevelopedBy, verbMadeBy, verbTrainedBy} {
		if !strings.Contains(sanitizeDshIdentityPat, v) {
			t.Errorf("动词探针 %q 不在模式里", v)
		}
	}
	if !strings.Contains(strings.ToLower(sanitizeDshGuiPat), sanitizeGuiHint) {
		t.Errorf("GUI 探针 %q 不在模式里", sanitizeGuiHint)
	}
	if !strings.Contains(strings.ToLower(sanitizeBrandPat), sanitizeBrandHint) {
		t.Errorf("品牌探针 %q 不在模式里", sanitizeBrandHint)
	}
}

// TestProbeNoFalseNegative 预检不允许假阴性：旧实现命中 → 新探测必须命中。
// （反向允许：探测可以更宽，代价只是多跑一次正则。）
func TestProbeNoFalseNegative(t *testing.T) {
	for _, in := range probeCorpus() {
		if legacyHasFingerprint(in) && probeMask(in) == 0 {
			t.Fatalf("漏探测（假阴性）in=%q", in)
		}
	}
}

// TestProbeNoFalseNegativeRandomized 随机拼装语料，覆盖多指纹共存。
func TestProbeNoFalseNegativeRandomized(t *testing.T) {
	parts := probeCorpus()
	seps := []string{" ", "\n", "、", " | "}
	seed := uint32(20261027)
	next := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>16) % n
	}
	for i := 0; i < 400; i++ {
		var sb strings.Builder
		for k := 0; k < 1+next(4); k++ {
			sb.WriteString(parts[next(len(parts))])
			sb.WriteString(seps[next(len(seps))])
		}
		in := strings.TrimSuffix(sb.String(), " ")
		if legacyHasFingerprint(in) && probeMask(in) == 0 {
			t.Fatalf("随机语料漏探测 in=%q", in)
		}
	}
}

// TestSanitizeTextMatchesLegacy 净化结果逐字节等价（字面量指纹在场时 TrimSpace 仍生效，
// 覆盖绝大多数路径）。
func TestSanitizeTextMatchesLegacy(t *testing.T) {
	for _, in := range probeCorpus() {
		mask := probeMask(in)
		if mask&maskLiteral == 0 && mask != 0 {
			continue // 身份句单独命中：TrimSpace 收窄是刻意的，见下一条用例
		}
		if got, want := sanitizeText(in), legacySanitize(in); got != want {
			t.Fatalf("结果不一致\n in=%q\n got=%q\nwant=%q", in, got, want)
		}
	}
}

// TestTrimOnlyWhenLiteralFingerprint TrimSpace 收窄（本次刻意的行为变更）：
// 只因一句自我介绍就把 system prompt 的首尾空白裁掉，会让同一 prompt 每轮字节都不同、
// 前缀缓存全失效。现在只有真的清过 KV/header 残留时才裁。
func TestTrimOnlyWhenLiteralFingerprint(t *testing.T) {
	in := "  " + dshIdentity + "\n"
	got := sanitizeText(in)
	if !strings.HasPrefix(got, "  ") || !strings.HasSuffix(got, "\n") {
		t.Fatalf("身份句命中不应触发 TrimSpace: %q", got)
	}
	if !strings.Contains(got, dshNeutral) {
		t.Fatalf("身份句应被替换: %q", got)
	}
	// 有字面量指纹（header 键名）时仍裁：KV 清理会留下尾随空白。
	kv := "  " + sanitizeHdrHint + ": " + "  "
	if out := sanitizeText(kv); strings.TrimSpace(out) != out {
		t.Fatalf("KV 残留路径仍应 TrimSpace: %q", out)
	}
}

// TestCanonFastPathEqualsCiRegex 快路等价：规范大小写输入下，canon 与 (?i) 的输出逐字节
// 相同（快路只是加速，不是新规则）。
func TestCanonFastPathEqualsCiRegex(t *testing.T) {
	for _, in := range []string{
		dshIdentity,
		dshCoding,
		"You are an AI assistant built on DeepSeek.",
		"You are an AI agent powered by DeepSeek Harness. Then answer briefly.",
		"You are interacting with the user through the DeepSeek Harness Web GUI whose URL the runtime context names.",
	} {
		canon := sanitizeDshIdentityCanonRe.ReplaceAllString(in, sanitizeDshIdentityRepl)
		ci := sanitizeDshIdentityRe.ReplaceAllString(in, sanitizeDshIdentityRepl)
		if canon != ci {
			t.Errorf("身份句快路不一致 in=%q canon=%q ci=%q", in, canon, ci)
		}
		canonGui := sanitizeDshGuiCanonRe.ReplaceAllString(in, sanitizeDshGuiRepl)
		ciGui := sanitizeDshGuiRe.ReplaceAllString(in, sanitizeDshGuiRepl)
		if canonGui != ciGui {
			t.Errorf("GUI 快路不一致 in=%q canon=%q ci=%q", in, canonGui, ciGui)
		}
	}
}

// TestProbeRoleScoping identity=false（user/tool 数据）时句式层位整体清掉。
func TestProbeRoleScoping(t *testing.T) {
	if probeMask(dshIdentity)&maskIdentity == 0 {
		t.Fatal("system 作用域应识别身份句")
	}
	if hasFingerprintScoped(dshIdentity, false) {
		t.Fatal("user/tool 作用域不应识别身份句")
	}
	if sanitizeTextScoped(dshIdentity, false) != dshIdentity {
		t.Fatal("user/tool 作用域不应改写身份句")
	}
}

// TestProbeIdentityWindow 「You are」与动词隔得太远不算身份句（否则任何提到 built by
// 的 system prompt 都会白跑一遍 548µs 的正则）。
func TestProbeIdentityWindow(t *testing.T) {
	far := "You are a helpful assistant. " + strings.Repeat("无关内容。", 40) + " built by someone."
	if probeMask(far)&maskIdentity != 0 {
		t.Fatal("超出窗口的动词不应触发身份句探测")
	}
	near := "You are an AI agent built by Acme."
	if probeMask(near)&maskIdentity == 0 {
		t.Fatal("窗口内的动词应触发身份句探测")
	}
}

// TestFoldAtEdgeCases 折叠查找的边界：越界、部分匹配、非 ASCII 字节、大小写。
func TestFoldAtEdgeCases(t *testing.T) {
	if foldAt("abc", 0, "abcd") {
		t.Error("越界应返回 false")
	}
	if !foldAt("abcd", 0, "abcd") {
		t.Error("完全匹配应返回 true")
	}
	if !foldAt("ABCD", 0, "abcd") {
		t.Error("大写应折叠命中")
	}
	if foldAt("abc", 1, "abc") {
		t.Error("尾部不足应返回 false")
	}
	if foldAt("中文abc", 2, "abc") {
		t.Error("落在多字节字符内部不应误判")
	}
	if !foldAt("x中文abc", 7, "abc") { // x(1) + 中(3) + 文(3) = 7
		t.Error("多字节前缀之后仍应命中")
	}
}
