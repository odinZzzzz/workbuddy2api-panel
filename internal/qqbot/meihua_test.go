package qqbot

// meihua_test.go 梅花易数起卦/断卦的确定性校验。
//
// 这些用例都是**手算**得出的，不是拿代码输出反填的：
//   - 时间起卦：2024-01-01 12:00（午时）
//     上卦=(2024+1+1)%8=2026%8=2 → 兑
//     下卦=(2026+7)%8=2033%8=1 → 乾
//     动爻=2033%6=5
//     本卦=泽天夬；动爻 5 在上卦 → 用=兑(金)、体=乾(金) → 比和 → 吉

import (
	"strings"
	"testing"
	"time"
)

func TestShichen(t *testing.T) {
	cases := map[int]int{
		23: 1, 0: 1, // 子时
		1: 2, 2: 2, // 丑时
		11: 7, 12: 7, // 午时
		22: 12, // 亥时
	}
	for hour, want := range cases {
		if got := shichen(hour); got != want {
			t.Errorf("shichen(%d) = %d, want %d", hour, got, want)
		}
	}
}

func TestCyc(t *testing.T) {
	if got := cyc(8, 8); got != 8 {
		t.Errorf("cyc(8,8)=%d want 8（0 视为 8）", got)
	}
	if got := cyc(9, 8); got != 1 {
		t.Errorf("cyc(9,8)=%d want 1", got)
	}
	if got := cyc(6, 6); got != 6 {
		t.Errorf("cyc(6,6)=%d want 6", got)
	}
}

// 手算用例：2024-01-01 12:00 → 泽天夬，动爻 5，体乾用兑，比和。
func TestMeihuaByTimeKnownCase(t *testing.T) {
	tm := time.Date(2024, 1, 1, 12, 0, 0, 0, time.Local)
	r := MeihuaByTime(tm)

	if r.UpperName != "兑" {
		t.Errorf("上卦 = %s, want 兑", r.UpperName)
	}
	if r.LowerName != "乾" {
		t.Errorf("下卦 = %s, want 乾", r.LowerName)
	}
	if r.Moving != 5 {
		t.Errorf("动爻 = %d, want 5", r.Moving)
	}
	if r.Name != "泽天夬" {
		t.Errorf("本卦 = %s, want 泽天夬", r.Name)
	}
	// 动爻 5 在上卦（4-6）→ 用=上卦兑，体=下卦乾
	if r.UseName != "兑" || r.BodyName != "乾" {
		t.Errorf("体用 = 体%s/用%s, want 体乾/用兑", r.BodyName, r.UseName)
	}
	if r.Relation != "体用比和" || r.Verdict != "吉" {
		t.Errorf("生克 = %s/%s, want 体用比和/吉", r.Relation, r.Verdict)
	}
}

// 数字起卦：a=3,b=4 → 上卦离(3)、下卦震(4)、动爻=(7)%6=1 → 火雷噬嗑，动爻 1 在下卦。
func TestMeihuaByNumberKnownCase(t *testing.T) {
	r := MeihuaByNumber(3, 4)
	if r.UpperName != "离" || r.LowerName != "震" {
		t.Errorf("上下卦 = %s/%s, want 离/震", r.UpperName, r.LowerName)
	}
	if r.Moving != 1 {
		t.Errorf("动爻 = %d, want 1", r.Moving)
	}
	if r.Name != "火雷噬嗑" {
		t.Errorf("本卦 = %s, want 火雷噬嗑", r.Name)
	}
	// 动爻 1 在下卦 → 用=下卦震(木)，体=上卦离(火) → 体生用（火生木？不对：木生火）
	// 体离=火、用震=木：木生火 → 用生体 → 吉
	if r.UseName != "震" || r.BodyName != "离" {
		t.Errorf("体用 = 体%s/用%s, want 体离/用震", r.BodyName, r.UseName)
	}
	if r.Relation != "用生体" || r.Verdict != "吉" {
		t.Errorf("生克 = %s/%s, want 用生体/吉", r.Relation, r.Verdict)
	}
}

// 六十四卦名表完整性：8×8 全部非空且不重复。
func TestHexNamesComplete(t *testing.T) {
	seen := map[string]bool{}
	for up := 1; up <= 8; up++ {
		for low := 1; low <= 8; low++ {
			n := hexNames[up][low]
			if n == "" {
				t.Errorf("hexNames[%d][%d] 为空", up, low)
				continue
			}
			if seen[n] {
				t.Errorf("卦名重复: %s", n)
			}
			seen[n] = true
		}
	}
	if len(seen) != 64 {
		t.Errorf("卦名数 = %d, want 64", len(seen))
	}
}

// 八卦五行映射正确。
func TestBaguaElements(t *testing.T) {
	want := map[string]string{
		"乾": "金", "兑": "金", "离": "火", "震": "木",
		"巽": "木", "坎": "水", "艮": "土", "坤": "土",
	}
	for _, b := range baguaList[1:] {
		if want[b.Name] != b.Elem {
			t.Errorf("%s 五行 = %s, want %s", b.Name, b.Elem, want[b.Name])
		}
	}
}

// 互卦/变卦有值且格式正常（不校验具体卦，因为要 64 条手算表；只保证流程完整）。
func TestMeihuaDerivedFields(t *testing.T) {
	r := MeihuaByNumber(5, 7)
	if r.MutualName == "" || r.ChangedName == "" {
		t.Fatal("互卦/变卦不应为空")
	}
	if r.Moving < 1 || r.Moving > 6 {
		t.Errorf("动爻越界: %d", r.Moving)
	}
	txt := r.Text()
	for _, want := range []string{"起卦：", "本卦：", "互卦：", "变卦：", "动爻：", "体卦：", "断语："} {
		if !strings.Contains(txt, want) {
			t.Errorf("输出缺少 %q:\n%s", want, txt)
		}
	}
}
