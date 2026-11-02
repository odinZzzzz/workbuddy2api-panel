package qqbot

// meihua.go 梅花易数（邵雍）起卦与断卦。
//
// 为什么用确定性计算而不是让模型编：占卜的"可信度"全在起卦规则上。
// 模型自由发挥只会说漂亮话；这里按传统规则算出本卦/互卦/变卦/动爻/体用生克，
// 再把结果交给模型用龙王腔解读——数据和说话分开，和 kkrb 那套一致。
//
// 起卦法（本项目采用）：
//   - 时间起卦：上卦=(年+月+日)%8，下卦=(年+月+日+时辰)%8，动爻=(年+月+日+时辰)%6
//     年月日取**公历**（现代通行做法；正宗用农历，但农历表易错且无法在本机校验，
//     宁可标注清楚也不用可能有误的转换）。
//   - 数字起卦：用户报两个数 a、b → 上卦=a%8，下卦=b%8，动爻=(a+b)%6。
//   - 字数起卦：问题字数分上下。
//
// 取数：0 作 8（八卦循环）。先天八卦数：乾1 兑2 离3 震4 巽5 坎6 艮7 坤8。

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// bagua 八卦（索引 1-8，对应先天数）。
type bagua struct {
	Num    int
	Name   string
	Symbol string
	Elem   string  // 五行
	Lines  [3]bool // 自下而上，true = 阳
}

// baguaList 先天八卦：乾1 兑2 离3 震4 巽5 坎6 艮7 坤8。
var baguaList = [9]bagua{
	{},
	{1, "乾", "☰", "金", [3]bool{true, true, true}},
	{2, "兑", "☱", "金", [3]bool{true, true, false}},
	{3, "离", "☲", "火", [3]bool{true, false, true}},
	{4, "震", "☳", "木", [3]bool{true, false, false}},
	{5, "巽", "☴", "木", [3]bool{false, true, true}},
	{6, "坎", "☵", "水", [3]bool{false, true, false}},
	{7, "艮", "☶", "土", [3]bool{false, false, true}},
	{8, "坤", "☷", "土", [3]bool{false, false, false}},
}

// hexNames 六十四卦名：[上卦][下卦]，下标 1-8。
//
// 顺序按上卦分组（乾/兑/离/震/巽/坎/艮/坤），下卦按 乾兑离震巽坎艮坤。
var hexNames = [9][9]string{
	{},
	{"", "乾为天", "天泽履", "天火同人", "天雷无妄", "天风姤", "天水讼", "天山遁", "天地否"},
	{"", "泽天夬", "兑为泽", "泽火革", "泽雷随", "泽风大过", "泽水困", "泽山咸", "泽地萃"},
	{"", "火天大有", "火泽睽", "离为火", "火雷噬嗑", "火风鼎", "火水未济", "火山旅", "火地晋"},
	{"", "雷天大壮", "雷泽归妹", "雷火丰", "震为雷", "雷风恒", "雷水解", "雷山小过", "雷地豫"},
	{"", "风天小畜", "风泽中孚", "风火家人", "风雷益", "巽为风", "风水涣", "风山渐", "风地观"},
	{"", "水天需", "水泽节", "水火既济", "水雷屯", "水风井", "坎为水", "水山蹇", "水地比"},
	{"", "山天大畜", "山泽损", "山火贲", "山雷颐", "山风蛊", "山水蒙", "艮为山", "山地剥"},
	{"", "地天泰", "地泽临", "地火明夷", "地雷复", "地风升", "地水师", "地山谦", "坤为地"},
}

// wuxingSheng 五行相生：金生水、水生木、木生火、火生土、土生金。
var wuxingSheng = map[string]string{"金": "水", "水": "木", "木": "火", "火": "土", "土": "金"}

// wuxingKe 五行相克：金克木、木克土、土克水、水克火、火克金。
var wuxingKe = map[string]string{"金": "木", "木": "土", "土": "水", "水": "火", "火": "金"}

// MeihuaResult 一次起卦的完整结果。
type MeihuaResult struct {
	Method string // 起卦法（时间/数字/字数）
	Upper  bagua
	Lower  bagua
	Moving int     // 动爻 1-6（自下而上）
	Lines  [6]bool // 本卦六爻，自下而上

	UpperName, LowerName string
	Name                 string // 本卦名
	MutualName           string // 互卦名
	ChangedName          string // 变卦名

	BodyName, UseName string // 体卦/用卦（八卦名）
	BodyElem, UseElem string // 五行
	Relation          string // 体用生克关系
	Verdict           string // 吉凶倾向
	Detail            string // 断语（传统口径）
}

// 时辰序数：子1 丑2 … 亥12（子时含 23:00-00:59）。
func shichen(hour int) int {
	s := (hour+1)/2%12 + 1
	if s < 1 {
		s += 12
	}
	return s
}

// cyc 把任意整数映射到 1..n（0 视为 n）。
func cyc(v, n int) int {
	r := v % n
	if r <= 0 {
		r += n
	}
	return r
}

// MeihuaByTime 时间起卦（公历年月日 + 时辰）。
func MeihuaByTime(t time.Time) MeihuaResult {
	year, month, day := t.Year(), int(t.Month()), t.Day()
	shi := shichen(t.Hour())
	sum := year + month + day
	upper := cyc(sum, 8)
	lower := cyc(sum+shi, 8)
	moving := cyc(sum+shi, 6)
	r := buildMeihua(upper, lower, moving)
	r.Method = fmt.Sprintf("时间起卦（公历 %04d-%02d-%02d %s时）",
		year, month, day, shichenName(shi))
	return r
}

// MeihuaByNumber 数字起卦（报数）。
func MeihuaByNumber(a, b int) MeihuaResult {
	upper := cyc(a, 8)
	lower := cyc(b, 8)
	moving := cyc(a+b, 6)
	r := buildMeihua(upper, lower, moving)
	r.Method = fmt.Sprintf("数字起卦（%d / %d）", a, b)
	return r
}

// MeihuaByText 字数起卦：问题字数上卦、时辰下卦。
func MeihuaByText(question string, t time.Time) MeihuaResult {
	n := len([]rune(strings.TrimSpace(question)))
	if n <= 0 {
		return MeihuaByTime(t)
	}
	shi := shichen(t.Hour())
	upper := cyc(n, 8)
	lower := cyc(n+shi, 8)
	moving := cyc(n+shi, 6)
	r := buildMeihua(upper, lower, moving)
	r.Method = fmt.Sprintf("字数起卦（%d 字 + %s时）", n, shichenName(shi))
	return r
}

func shichenName(s int) string {
	names := [13]string{"", "子", "丑", "寅", "卯", "辰", "巳", "午", "未", "申", "酉", "戌", "亥"}
	if s < 1 || s > 12 {
		return "子"
	}
	return names[s]
}

// buildMeihua 由上下卦与动爻推出本卦/互卦/变卦/体用。
func buildMeihua(upperNum, lowerNum, moving int) MeihuaResult {
	up := baguaList[upperNum]
	low := baguaList[lowerNum]
	r := MeihuaResult{
		Upper: up, Lower: low, Moving: moving,
		UpperName: up.Name, LowerName: low.Name,
		Name: hexNames[upperNum][lowerNum],
	}
	// 六爻自下而上：下卦三爻 + 上卦三爻
	for i := 0; i < 3; i++ {
		r.Lines[i] = low.Lines[i]
		r.Lines[i+3] = up.Lines[i]
	}

	// 互卦：2-4 爻为下卦，3-5 爻为上卦
	mutualLower := linesToBagua([3]bool{r.Lines[1], r.Lines[2], r.Lines[3]})
	mutualUpper := linesToBagua([3]bool{r.Lines[2], r.Lines[3], r.Lines[4]})
	r.MutualName = hexNames[mutualUpper.Num][mutualLower.Num]

	// 变卦：动爻阴阳互换
	changed := r.Lines
	changed[moving-1] = !changed[moving-1]
	chLower := linesToBagua([3]bool{changed[0], changed[1], changed[2]})
	chUpper := linesToBagua([3]bool{changed[3], changed[4], changed[5]})
	r.ChangedName = hexNames[chUpper.Num][chLower.Num]

	// 体用：动爻所在卦为「用」，另一卦为「体」
	if moving <= 3 {
		r.UseName, r.UseElem = low.Name, low.Elem
		r.BodyName, r.BodyElem = up.Name, up.Elem
	} else {
		r.UseName, r.UseElem = up.Name, up.Elem
		r.BodyName, r.BodyElem = low.Name, low.Elem
	}
	r.Relation, r.Verdict, r.Detail = judgeBodyUse(r.BodyElem, r.UseElem)
	return r
}

// linesToBagua 三爻（自下而上）→ 八卦。
func linesToBagua(l [3]bool) bagua {
	for _, b := range baguaList[1:] {
		if b.Lines == l {
			return b
		}
	}
	return baguaList[8] // 兜底坤
}

// judgeBodyUse 体用五行生克 → 关系/吉凶/断语（传统口径）。
func judgeBodyUse(body, use string) (relation, verdict, detail string) {
	switch {
	case body == use:
		return "体用比和", "吉", "体用同属" + body + "，比和顺遂，所问之事易成，且有同类相助。"
	case wuxingSheng[use] == body:
		return "用生体", "吉", "用卦" + use + "生体卦" + body + "，外来之力助我，主有进益之喜。"
	case wuxingSheng[body] == use:
		return "体生用", "耗", "体卦" + body + "生用卦" + use + "，我耗力于外，主劳而无功、有泄气之象。"
	case wuxingKe[body] == use:
		return "体克用", "小吉", "体卦" + body + "克用卦" + use + "，我能制事，但须费力，事可成而迟。"
	case wuxingKe[use] == body:
		return "用克体", "凶", "用卦" + use + "克体卦" + body + "，外力制我，主受阻滞，宜退守待时。"
	}
	return "体用不明", "平", "体用关系不明显，宜再观察。"
}

// Text 把结果渲染成给模型/用户看的文本。
func (r MeihuaResult) Text() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "起卦：%s\n", r.Method)
	fmt.Fprintf(&sb, "上卦：%s%s（%s）　下卦：%s%s（%s）\n",
		r.Upper.Symbol, r.UpperName, r.Upper.Elem, r.Lower.Symbol, r.LowerName, r.Lower.Elem)
	fmt.Fprintf(&sb, "本卦：%s　互卦：%s　变卦：%s\n", r.Name, r.MutualName, r.ChangedName)
	fmt.Fprintf(&sb, "动爻：第 %d 爻\n", r.Moving)
	fmt.Fprintf(&sb, "体卦：%s（%s）　用卦：%s（%s）　%s → %s\n",
		r.BodyName, r.BodyElem, r.UseName, r.UseElem, r.Relation, r.Verdict)
	sb.WriteString("断语：" + r.Detail)
	return sb.String()
}

// meihuaReply 处理「占卜 <事>」/「起卦 3,8」这类指令，返回可直接外发的卦象文本。
//
// 支持三种起卦：
//   - 「占卜 明天面试能过吗」→ 字数起卦（问题字数 + 当前时辰）
//   - 「起卦 3,8」→ 报数起卦
//   - 「占卜」→ 纯时间起卦
func meihuaReply(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return MeihuaByTime(time.Now()).Text()
	}
	// 报数：全角/半角逗号、空格、斜杠分隔的两个数
	nums := strings.FieldsFunc(arg, func(c rune) bool {
		return c == ',' || c == '，' || c == ' ' || c == '/' || c == '、'
	})
	if len(nums) >= 2 {
		a, err1 := strconv.Atoi(strings.TrimSpace(nums[0]))
		b, err2 := strconv.Atoi(strings.TrimSpace(nums[1]))
		if err1 == nil && err2 == nil && a > 0 && b > 0 {
			return MeihuaByNumber(a, b).Text()
		}
	}
	// 单个纯数字：也按报数（第二个数用当前时辰补）
	if len(nums) == 1 {
		if a, err := strconv.Atoi(strings.TrimSpace(nums[0])); err == nil && a > 0 {
			return MeihuaByNumber(a, shichen(time.Now().Hour())).Text()
		}
	}
	return MeihuaByText(arg, time.Now()).Text()
}
