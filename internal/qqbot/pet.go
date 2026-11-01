package qqbot

// pet.go 元气鲸鱼娘立绘（dsh-whale-musume，MIT）。
//
// 为什么用现成立绘而不是让模型画：用户明确"直接复用资源，用原来的图"——
// 这些是官方美术资产，质量远超模型手绘 SVG；bot 只做"选对状态 + 发出去"。
// webp 在 repo 里（go:embed），发送时转 PNG（QQ 对 webp 的兼容性不如 png 稳），
// 再走 sendImage（本地文件 → base64 内联，避开 NapCat 路径发送超时）。

import (
	"bytes"
	"embed"
	"fmt"
	"image/png"
	"strings"

	xwebp "golang.org/x/image/webp"
)

//go:embed pet/*.webp
var petAssets embed.FS

// petState 一个可用状态：canonical 名 + 中文别名 + 面向模型的一句话说明。
type petState struct {
	Canonical string
	Desc      string
}

// petStates 面向模型的状态清单（键 = 参数值）。
var petStates = []petState{
	{"greet", "打招呼问好"},
	{"angry", "生气"},
	{"meme-cry", "哭"},
	{"blush", "害羞脸红"},
	{"meme-smug", "得意"},
	{"meme-shock", "震惊"},
	{"meme-heart", "比心"},
	{"meme-yes", "点头认可"},
	{"meme-no", "摇头拒绝"},
	{"meme-music", "哼歌"},
	{"meme-broke", "破产哭穷"},
	{"cool-shades", "戴墨镜耍帅"},
	{"star", "闪闪发光"},
	{"celebrate", "庆祝"},
	{"teasing", "调侃你"},
	{"failure", "受挫蔫了"},
	{"sleep", "睡着了"},
	{"work-idea", "工作冒灵感"},
	{"work-slack", "上班摸鱼"},
	{"daily-coffee", "喝咖啡"},
	{"daily-eat", "吃东西"},
	{"curious", "好奇凑过来"},
	{"abstract", "发呆放空"},
	{"running", "跑起来"},
	{"night", "深夜困倦"},
	{"work-pat", "拍拍头安慰"},
	{"daily-pajama", "穿睡衣"},
}

// petAliases 中文/别名 → canonical。模型可能传中文，也可能传英文状态名。
var petAliases = map[string]string{
	"打招呼": "greet", "问好": "greet", "哈喽": "greet",
	"生气": "angry", "怒": "angry", "愤怒": "angry",
	"哭": "meme-cry", "难过": "meme-cry", "伤心": "meme-cry", "眼泪": "meme-cry",
	"害羞": "blush", "脸红": "blush",
	"得意": "meme-smug", "骄傲": "meme-smug", "嘚瑟": "meme-smug",
	"震惊": "meme-shock", "惊讶": "meme-shock", "吓到": "meme-shock",
	"比心": "meme-heart", "爱心": "meme-heart", "喜欢": "meme-heart",
	"点头": "meme-yes", "好": "meme-yes", "可以": "meme-yes", "认可": "meme-yes",
	"摇头": "meme-no", "不行": "meme-no", "拒绝": "meme-no",
	"唱歌": "meme-music", "音乐": "meme-music", "哼歌": "meme-music",
	"破产": "meme-broke", "穷": "meme-broke", "没钱": "meme-broke",
	"耍帅": "cool-shades", "墨镜": "cool-shades", "帅": "cool-shades",
	"闪": "star", "发光": "star", "明星": "star",
	"庆祝": "celebrate", "恭喜": "celebrate", "成功": "celebrate",
	"调侃": "teasing", "嘲笑": "teasing", "坏笑": "teasing",
	"受挫": "failure", "蔫": "failure", "失败": "failure",
	"睡": "sleep", "睡觉": "sleep", "困": "sleep", "打盹": "sleep",
	"工作": "work-idea", "干活": "work-idea", "灵感": "work-idea",
	"摸鱼": "work-slack", "偷懒": "work-slack", "划水": "work-slack",
	"咖啡": "daily-coffee", "喝茶": "daily-coffee",
	"吃": "daily-eat", "干饭": "daily-eat", "饿": "daily-eat",
	"好奇": "curious",
	"发呆": "abstract", "放空": "abstract", "无语": "abstract",
	"跑": "running", "奔跑": "running", "冲": "running",
	"深夜": "night", "熬夜": "night",
	"摸头": "work-pat", "安慰": "work-pat", "拍拍": "work-pat",
	"睡衣": "daily-pajama", " pajama": "daily-pajama",
	"idle-cute": "idle-cute", "可爱": "idle-cute",
	"greet": "greet", "angry": "angry", "cry": "meme-cry", "blush": "blush",
	"smug": "meme-smug", "shock": "meme-shock", "heart": "meme-heart",
	"yes": "meme-yes", "no": "meme-no", "music": "meme-music",
	"broke": "meme-broke", "shades": "cool-shades", "star": "star",
	"celebrate": "celebrate", "teasing": "teasing", "failure": "failure",
	"sleep": "sleep", "work": "work-idea", "slack": "work-slack",
	"coffee": "daily-coffee", "eat": "daily-eat", "curious": "curious",
	"abstract": "abstract", "running": "running", "night": "night",
	"pat": "work-pat", "pajama": "daily-pajama",
}

// PetStateNames 面向模型的状态清单（prompt 稳定，固定顺序）。
func PetStateNames() string {
	out := make([]string, 0, len(petStates))
	for _, s := range petStates {
		out = append(out, s.Canonical+"="+s.Desc)
	}
	return strings.Join(out, "；")
}

// resolvePetState 把模型传的 state 归一化到 canonical。
func resolvePetState(state string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(state))
	if k == "" {
		return "", false
	}
	if c, ok := petAliases[k]; ok {
		return c, true
	}
	// 直接给 canonical 也行
	for _, s := range petStates {
		if s.Canonical == k {
			return k, true
		}
	}
	return "", false
}

// petPNG 取某状态的立绘并转成 PNG 字节。
func petPNG(canonical string) ([]byte, error) {
	raw, err := petAssets.ReadFile("pet/state-" + canonical + ".webp")
	if err != nil {
		return nil, fmt.Errorf("鲸鱼娘没有「%s」这张立绘", canonical)
	}
	img, err := xwebp.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("立绘解码失败: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("PNG 编码失败: %w", err)
	}
	return buf.Bytes(), nil
}

// sendPetImage 渲染好的 PNG 落盘并发送，返回给模型的回执文案。
func (b *Bridge) sendPetImage(ev *Event, canonical string) (string, error) {
	pngBytes, err := petPNG(canonical)
	if err != nil {
		return "", err
	}
	path, err := b.SaveGeneratedSticker(pngBytes)
	if err != nil {
		return "", err
	}
	if err := b.sendImage(ev, path); err != nil {
		return "", err
	}
	b.state.note(RecentEvent{Type: "tool", Group: ev.GroupID, User: ev.UserID, Action: "pet_" + canonical})
	return "已把鲸鱼娘「" + canonical + "」发出去", nil
}

// ── 意图识别：要鲸鱼娘的图 ─────────────────────────────────────
//
// 为什么要代码直连、而不是交给模型决定：模型自己画必然走样（用户已多次反馈），
// 而"参考立绘"这件事机器做不到——最忠实的做法就是**直接发官方立绘**。
// 所以这里做确定性判断：只要在要鲸鱼娘的图，就按情绪选状态发图，全程不过模型。

var whaleSubjectWords = []string{"鲸鱼娘", "鲸鱼", "鲸娘"}

var whaleDrawVerbs = []string{
	"画", "来一张", "整一张", "发一张", "生成", "看看她", "看看鲸",
	"立绘", "表情", "图片", "图", "照片",
}

// WhaleArtIntent 判断"要鲸鱼娘的图"，返回立绘状态。
// 第二个返回值为 false 表示不是这类请求（交给正常对话流程）。
func WhaleArtIntent(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "/") {
		return "", false
	}
	hasSubject := false
	for _, w := range whaleSubjectWords {
		if strings.Contains(t, w) {
			hasSubject = true
			break
		}
	}
	if !hasSubject {
		return "", false
	}
	// 有"画/图/立绘"这类动词：只要提到鲸鱼娘就是要图，情绪缺省 greet。
	for _, v := range whaleDrawVerbs {
		if strings.Contains(t, v) {
			st, _ := moodFromText(t)
			return st, true
		}
	}
	// 没动词，但带了明确情绪词（≥2 字）：也算要图，如"鲸鱼娘睡觉""鲸鱼娘比心"。
	// 单字情绪（"吃"）不算——"鲸鱼好吃吗"这种闲聊不该触发。
	if mood, found := strongMood(t); found {
		return mood, true
	}
	return "", false
}

// moodFromText 挑最匹配的情绪。
//
// 评分 = 离"鲸鱼"这个主体词的字符距离最近者优先，同距离取更长的别名。
// 为什么不是简单的"长键优先"：'给我看看摸鱼的鲸鱼娘' 里 "看看"(泛动词) 和
// "摸鱼"(真情绪) 等长，先匹配到谁全看排序——按邻近度就稳定选对（摸鱼贴着鲸鱼娘）。
func moodFromText(text string) (string, bool) {
	if c, ok := bestMood(text, 0); ok {
		return c, true
	}
	return "greet", false
}

// strongMood 只认长度 ≥2 的情绪词（中文单字多为泛词，容易误伤）。
func strongMood(text string) (string, bool) {
	return bestMood(text, 2)
}

// bestMood 按"离主体词最近"挑选情绪；minRunes 限制别名最短长度（0 = 不限）。
func bestMood(text string, minRunes int) (string, bool) {
	subjectAt := -1
	for _, w := range whaleSubjectWords {
		if i := strings.Index(text, w); i >= 0 {
			if subjectAt < 0 || i < subjectAt {
				subjectAt = i
			}
		}
	}
	bestState, bestDist, bestLen := "", 1<<30, 0
	for _, k := range sortedAliasKeys() {
		if minRunes > 0 && len([]rune(k)) < minRunes {
			continue
		}
		i := strings.Index(text, k)
		if i < 0 {
			continue
		}
		dist := 0
		if subjectAt >= 0 {
			dist = i - subjectAt
			if dist < 0 {
				dist = -dist
			}
		}
		c, ok := resolvePetState(k)
		if !ok {
			continue
		}
		// 更近优先；同距离取更长别名（"生气" 胜过 "气"）
		if dist < bestDist || (dist == bestDist && len([]rune(k)) > bestLen) {
			bestState, bestDist, bestLen = c, dist, len([]rune(k))
		}
	}
	if bestState == "" {
		return "", false
	}
	return bestState, true
}

// sortedAliasKeys 别名按长度降序。
func sortedAliasKeys() []string {
	keys := make([]string, 0, len(petAliases))
	for k := range petAliases {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if len([]rune(keys[j])) > len([]rune(keys[i])) {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// sendPetImageTweaked 立绘 + 程序化改色（位图 HSL）。
// 语义级重绘需要 img2img 模型；换配色不需要——直接改像素，画面内容零损失。
func (b *Bridge) sendPetImageTweaked(ev *Event, canonical string, tw Tweak, deco Deco) (string, error) {
	pngBytes, err := petPNG(canonical)
	if err != nil {
		return "", err
	}
	if !tw.Empty() {
		if out, rerr := RecolorPNG(pngBytes, tw.Hue, tw.Sat, tw.Light); rerr == nil {
			pngBytes = out
		}
	}
	if !deco.Empty() {
		if out, derr := ApplyDeco(pngBytes, deco, b.cfg().ChartFont); derr == nil {
			pngBytes = out
		}
	}
	path, err := b.SaveGeneratedSticker(pngBytes)
	if err != nil {
		return "", err
	}
	if err := b.sendImage(ev, path); err != nil {
		return "", err
	}
	b.state.note(RecentEvent{Type: "tool", Group: ev.GroupID, User: ev.UserID, Action: "pet_" + canonical})
	var notes []string
	if msg := DescribeTweak(tw); msg != "无调整" {
		notes = append(notes, msg)
	}
	if d := deco.Describe(); d != "" {
		notes = append(notes, d)
	}
	if len(notes) > 0 {
		return "已把鲸鱼娘「" + canonical + "」按 " + strings.Join(notes, "、") + " 处理后发出", nil
	}
	return "已把鲸鱼娘「" + canonical + "」发出去", nil
}
