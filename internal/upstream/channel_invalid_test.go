package upstream

import "testing"

// 渠道校验分类钉子：上游对内容审核与渠道校验**回同一个 code 11128**，
// 只能靠 msg 区分。两者处置截然不同：
//   - 内容审核（blocked by security policy）→ ErrContentBlocked，走降级重试
//   - 渠道校验（conversation id missing）  → ErrChannelInvalid，fail-fast 透传
//
// 若前者吞掉后者，渠道校验失败会被伪装成"内容被拦"并进入降级重试链——
// 而头是同一个头，重试永远失败，排查被彻底误导。
func TestClassifyDistinguishesChannelInvalidFromContentBlocked(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ErrKind
	}{
		{
			name: "渠道校验：conversation id missing",
			body: `{"code":11128,"msg":"conversation id missing","requestId":"abc","displayMsg":{"zh":"请求被拦截，请重新发送"}}`,
			want: ErrChannelInvalid,
		},
		{
			name: "渠道校验：displayTips 形态",
			body: `{"code":11128,"msg":"conversation id missing","displayTips":{"zh":"这次调用没通过渠道校验，可能不是官方客户端发出的。请用官方版本重新发送。"}}`,
			want: ErrChannelInvalid,
		},
		{
			name: "内容审核：blocked by security policy（同码不同因，语义不变）",
			body: `{"code":11128,"msg":"blocked by security policy"}`,
			want: ErrContentBlocked,
		},
		{
			name: "大小写不敏感",
			body: `{"code":11128,"msg":"Conversation ID Missing"}`,
			want: ErrChannelInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(400, tc.body); got != tc.want {
				t.Errorf("Classify(400, %s) = %v，期望 %v", tc.body, got, tc.want)
			}
		})
	}
}

// 钉 String() 契约：分类名会进 reqlog / hint / 运维排查，改名属破坏性变更。
func TestErrChannelInvalidString(t *testing.T) {
	if got := ErrChannelInvalid.String(); got != "channel_invalid" {
		t.Errorf("ErrChannelInvalid.String() = %q，期望 \"channel_invalid\"", got)
	}
}
