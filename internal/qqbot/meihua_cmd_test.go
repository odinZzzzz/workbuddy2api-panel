package qqbot

// meihua_cmd_test.go 「占卜」指令的解析与三种起卦路径。

import (
	"strings"
	"testing"
)

func TestCommandMeihua(t *testing.T) {
	cases := []struct {
		text string
		arg  string
	}{
		{"占卜 明天面试能过吗", "明天面试能过吗"},
		{"起卦 3,8", "3,8"},
		{"算一卦 今天运气", "今天运气"},
	}
	for _, c := range cases {
		name, arg, ok := Command(c.text)
		if !ok || name != "/meihua" {
			t.Fatalf("%q → name=%q ok=%v, want /meihua", c.text, name, ok)
		}
		if arg != c.arg {
			t.Errorf("%q 参数 = %q, want %q", c.text, arg, c.arg)
		}
	}
	// 不抢占「总结」「锐评」
	if n, _, _ := Command("总结"); n != "/summary" {
		t.Errorf("总结 → %q", n)
	}
	if n, _, _ := Command("锐评 这游戏"); n != "/rap" {
		t.Errorf("锐评 → %q", n)
	}
}

func TestMeihuaReplyForms(t *testing.T) {
	// 报数起卦
	got := meihuaReply("3,8")
	if !strings.Contains(got, "数字起卦") {
		t.Errorf("报数应走数字起卦: %s", got[:60])
	}
	// 单个数字：第二个数用时辰补
	got2 := meihuaReply("7")
	if !strings.Contains(got2, "数字起卦") {
		t.Errorf("单数字也应走数字起卦: %s", got2[:60])
	}
	// 带问题文字：字数起卦
	got3 := meihuaReply("明天面试能过吗")
	if !strings.Contains(got3, "字数起卦") {
		t.Errorf("带文字应走字数起卦: %s", got3[:60])
	}
	// 空参数：时间起卦
	got4 := meihuaReply("")
	if !strings.Contains(got4, "时间起卦") {
		t.Errorf("空参数应走时间起卦: %s", got4[:60])
	}
	// 三种路径都必须给出完整卦象
	for _, s := range []string{got, got2, got3, got4} {
		for _, want := range []string{"本卦：", "互卦：", "变卦：", "动爻：", "体卦：", "断语："} {
			if !strings.Contains(s, want) {
				t.Errorf("输出缺少 %q", want)
			}
		}
	}
}

// 同一时间起卦必须稳定（可复现），否则"占卜"每次结果都变就不可信了。
func TestMeihuaDeterministic(t *testing.T) {
	a := MeihuaByNumber(11, 23)
	b := MeihuaByNumber(11, 23)
	if a.Name != b.Name || a.Moving != b.Moving || a.Relation != b.Relation {
		t.Errorf("同参数应得同卦: %+v vs %+v", a, b)
	}
}
