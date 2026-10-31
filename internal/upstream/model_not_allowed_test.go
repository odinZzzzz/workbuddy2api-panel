package upstream

import (
	"net/http"
	"testing"
)

// 真机报文（用户实测）：gemini-3.5-flash 在 personal_free_user 上被拒。
const modelNotAllowedBody = `{"code":11158,"msg":"model [gemini-3.5-flash] is not allowed for user type [personal_free_user]","requestId":"517c9a98","displayMsg":{"en":"Your account type does not support this model.","zh":"当前账号类型暂不支持该模型。","zh-hant":"目前帳號類型暫不支援該模型。"},"actions":["SUBMIT_FEEDBACK","COPY_ERROR","SWITCH_MODEL"]}`

// IsModelNotAllowed 认这条报文，且不误伤普通 400。
func TestIsModelNotAllowed(t *testing.T) {
	if !IsModelNotAllowed(400, modelNotAllowedBody) {
		t.Fatal("应识别 11158 报文")
	}
	// 只有业务码、没有文案（上游精简版）也要认
	if !IsModelNotAllowed(400, `{"code":11158,"msg":"forbidden"}`) {
		t.Error("仅有 code 11158 时应识别")
	}
	// 只有文案、没有 code（别的包装层）也要认
	if !IsModelNotAllowed(400, `{"error":{"message":"Model is not allowed for user type"}}`) {
		t.Error("仅有英文文案时应识别")
	}
	// 繁体中文文案
	if !IsModelNotAllowed(400, `{"displayMsg":{"zh-hant":"目前帳號類型暫不支援該模型。"}}`) {
		t.Error("繁体文案应识别")
	}
	// 不该误伤：内容拦截 / 普通 400 / 空体
	for _, body := range []string{
		`{"code":400,"msg":"bad request"}`,
		`{"code":11-128,"msg":"content blocked by policy"}`,
		"",
	} {
		if IsModelNotAllowed(400, body) {
			t.Errorf("不应识别: %s", body)
		}
	}
}

// Classify 必须把它判成 ErrModelNotAllowed，而不是 ErrAccountFault。
// 这一条是问题的核心：判成账号故障会冷却好号、跑空池子、最终 503。
func TestClassifyModelNotAllowed(t *testing.T) {
	got := Classify(http.StatusBadRequest, modelNotAllowedBody)
	if got != ErrModelNotAllowed {
		t.Fatalf("Classify=%v want ErrModelNotAllowed（判成 ErrAccountFault 会把好号打死）", got)
	}
	// 真机 11102 的「权限」变体：msg 是 only available for authorized users
	authzBody := `{"code":11102,"msg":"model [gemini-3.5-flash] is only available for authorized users","requestId":"4c496eda"}`
	if k := Classify(http.StatusBadRequest, authzBody); k != ErrModelNotAllowed {
		t.Fatalf("11102 权限变体应判 ErrModelNotAllowed，实际 %v（判成 ErrModelBlocked 就不会全局拉黑）", k)
	}
	if !IsModelNotAllowed(http.StatusBadRequest, authzBody) {
		t.Error("应识别 only available for authorized users")
	}

	// 顺序：真正的「模型不存在」11102 仍走 ErrModelBlocked
	blocked := `{"code":11102,"msg":"service info not found"}`
	if k := Classify(http.StatusBadRequest, blocked); k != ErrModelBlocked {
		t.Errorf("11102 应优先判为 ErrModelBlocked，实际 %v", k)
	}
}
