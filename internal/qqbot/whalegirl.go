package qqbot

// whalegirl.go 复用 whale-girl.svg 这一份美术资源，切出不同状态。
//
// 原图（dsh-whale-lap，MIT）把"闭眼 / 笔记本 / 打字手 / Zzz / 喷水 / 眼泪 / 汗 / 音符 /
// 四种嘴型"都画在同一份 SVG 里，靠外部 CSS 控制显隐。我们的渲染器不跑 CSS，
// 所以按 state **物理裁剪**对应的组——这样结构、线条、配色全部沿用原画，
// 只有状态不同，画风与参考图 100% 一致（重画必然走样）。

import (
	"strings"
)

// whaleBaseKeep 任何状态都保留的主体部件。
var whaleBaseKeep = []string{
	"wlgTwinL", "wlgTwinR", "wlgTail", "wlgFlukes", "wlgBody", "wlgStripes",
	"wlgHead", "wlgBrows", "wlgBlush", "wlgStar", "wlgArms",
	"wlgBubbles", "wlgSparkles", "wlgFinL", "wlgFinR", "wlgLockL", "wlgLockR",
}

// whaleAllParts 所有可选部件（用于反推"要删哪些"）。
var whaleAllParts = []string{
	"wlgEyes", "wlgEyesClosed",
	"wlgMouthDefault", "wlgMouthTongue", "wlgMouthHappy", "wlgMouthHappyTongue",
	"wlgMouthSad", "wlgMouthO",
	"wlgLaptop", "wlgTypeHands", "wlgZzz", "wlgSpout", "wlgBlow",
	"wlgNote", "wlgSweat", "wlgTear",
}

// whaleState 状态 → 需要保留的可选部件。
type whaleState struct {
	Keep []string
	Desc string
}

// whaleStates 支持的状态。键就是工具参数 state 的取值。
var whaleStates = map[string]whaleState{
	"idle":  {[]string{"wlgEyes", "wlgMouthDefault", "wlgMouthTongue"}, "趴着发呆（默认）"},
	"sleep": {[]string{"wlgEyesClosed", "wlgMouthDefault", "wlgZzz"}, "打盹：闭眼 + Zzz"},
	"work":  {[]string{"wlgEyes", "wlgMouthDefault", "wlgLaptop", "wlgTypeHands"}, "干活：掏出小电脑敲代码"},
	"cry":   {[]string{"wlgEyesClosed", "wlgMouthSad", "wlgTear"}, "哭：闭眼 + 掉眼泪"},
	"happy": {[]string{"wlgEyes", "wlgMouthHappy", "wlgMouthHappyTongue"}, "开心：咧嘴笑"},
	"angry": {[]string{"wlgEyes", "wlgMouthO", "wlgSweat"}, "生气：o 嘴 + 冒汗"},
	"sing":  {[]string{"wlgEyes", "wlgMouthO", "wlgNote"}, "哼歌：o 嘴 + 音符"},
	"spout": {[]string{"wlgEyes", "wlgMouthDefault", "wlgSpout", "wlgBlow"}, "头顶喷水"},
	"shy":   {[]string{"wlgEyesClosed", "wlgMouthDefault"}, "害羞：闭眼抿嘴（腮红一直在）"},
}

// WhaleStateNames 列出可用状态（给工具描述用）。
func WhaleStateNames() string {
	names := make([]string, 0, len(whaleStates))
	for k := range whaleStates {
		names = append(names, k)
	}
	// 固定顺序，避免每次输出不同（prompt 稳定性）
	order := []string{"idle", "sleep", "work", "cry", "happy", "angry", "sing", "spout", "shy"}
	out := make([]string, 0, len(order))
	for _, k := range order {
		if _, ok := whaleStates[k]; ok {
			out = append(out, k+"="+whaleStates[k].Desc)
		}
	}
	if len(out) == 0 {
		return strings.Join(names, "/")
	}
	return strings.Join(out, "；")
}

// removeElementByID 删掉指定 id 的元素。
//
// 实现刻意"笨"：先找 `id="X"`，再往回找标签起始 `<`，取标签名；
// 是 <g> 就按 <g>/</g> 配平删整组，其它元素删到它自己的 `/>`。
// 早前用正则一次性算偏移的版本把结构删坏过（<path> 被 </g> 收尾），所以不图省事。
func removeElementByID(src, id string) string {
	needle := `id="` + id + `"`
	pos := strings.Index(src, needle)
	if pos < 0 {
		return src
	}
	start := strings.LastIndex(src[:pos], "<")
	if start < 0 {
		return src
	}
	tagEnd := strings.Index(src[start:], ">")
	if tagEnd < 0 {
		return src
	}
	tagEnd += start
	// 取标签名
	nameEnd := start + 1
	for nameEnd < len(src) && (isTagChar(src[nameEnd])) {
		nameEnd++
	}
	tag := src[start+1 : nameEnd]
	selfClosing := strings.HasSuffix(strings.TrimSpace(src[start:tagEnd+1]), "/>")

	if tag == "g" && !selfClosing {
		depth, i := 1, tagEnd+1
		for i < len(src) && depth > 0 {
			o := strings.Index(src[i:], "<g")
			c := strings.Index(src[i:], "</g>")
			if c < 0 {
				return src
			}
			if o >= 0 && o < c {
				depth++
				i += o + 2
			} else {
				depth--
				i += c + 4
			}
		}
		return src[:start] + src[i:]
	}
	if selfClosing {
		return src[:start] + src[tagEnd+1:]
	}
	// 非自闭合的普通元素：找到自己的结束标签
	closeTag := "</" + tag + ">"
	end := strings.Index(src[tagEnd:], closeTag)
	if end < 0 {
		return src
	}
	return src[:start] + src[tagEnd+1+end+len(closeTag):]
}

func isTagChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// WhaleGirlState 按 state 裁剪 whale-girl.svg。未知 state 回落 idle。
func WhaleGirlState(src, state string) (string, string) {
	name := strings.ToLower(strings.TrimSpace(state))
	st, ok := whaleStates[name]
	if !ok {
		name = "idle"
		st = whaleStates["idle"]
	}
	keep := map[string]bool{}
	for _, k := range whaleBaseKeep {
		keep[k] = true
	}
	for _, k := range st.Keep {
		keep[k] = true
	}
	out := src
	for _, part := range whaleAllParts {
		if !keep[part] {
			out = removeElementByID(out, part)
		}
	}
	return out, name
}
