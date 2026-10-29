// Package httpauth 网关与面板共用的 Bearer 鉴权原语。
//
// 单独成包的原因：server（/v1/*、/status）与 panel（/panel/api/*）两处鉴权
// 必须完全同口径——此前各自复制了一份"字符串直接比较"的实现，既容易漂移，
// 又都带计时侧信道。统一到这里后，口径只有一份，且天然常量时间比较。
package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerPrefix 认证方案前缀（大小写敏感，与 HTTP 规范及既有实现一致）。
const bearerPrefix = "Bearer "

// VerifyBearer 校验请求头是否携带正确的 Bearer 密钥。
//
// key 为空表示"未启用鉴权"，恒返回 true（调用方据此放行）。
// 比较用 SHA-256 摘要 + subtle.ConstantTimeCompare：
//   - 常量时间，不因前缀匹配长度而泄露信息；
//   - 先摘要再比较，长度差异被吸收进摘要（不会因长度不同提前返回）；
//   - 摘要本身不可逆，即便有侧信道也拿不到密钥原文。
func VerifyBearer(r *http.Request, key string) bool {
	if key == "" {
		return true
	}
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, bearerPrefix) {
		// 缺头/方案不对：仍走一次摘要比较，保持耗时形状一致。
		subtle.ConstantTimeCompare(digest(""), digest(key))
		return false
	}
	tok := authz[len(bearerPrefix):]
	return subtle.ConstantTimeCompare(digest(tok), digest(key)) == 1
}

// digest 返回 s 的 SHA-256（定长 32 字节，供常量时间比较）。
func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// VerifyAPIKey 校验请求携带的密钥，**同时接受两种携带方式**：
//
//	Authorization: Bearer <key>   —— OpenAI 系客户端
//	x-api-key: <key>              —— Anthropic 系客户端（Claude Code 等）
//
// 存在原因：/v1/messages 走 Anthropic 协议，其官方 SDK 用 x-api-key；而本网关其余
// 端点用 Bearer。两种头都指向同一个 api_key，所以这里统一成一个判据，避免
// 「同一把 key 换个头就 401」的割裂。key 为空表示未启用鉴权，恒放行。
//
// 比较口径与 VerifyBearer 完全一致（摘要 + 常量时间），两种头的校验都会执行，
// 不因第一个头命中就提前返回，保持耗时形状一致。
func VerifyAPIKey(r *http.Request, key string) bool {
	if key == "" {
		return true
	}
	okBearer := VerifyBearer(r, key)
	okXAPIKey := subtle.ConstantTimeCompare(digest(r.Header.Get("x-api-key")), digest(key)) == 1
	return okBearer || okXAPIKey
}

// PresentedKey 取出请求里携带的原始密钥（Bearer 优先，其次 x-api-key）。
//
// 多令牌管理需要"用户到底出示了哪把 key"才能查表，而 VerifyBearer/VerifyAPIKey
// 只回答"对不对"。两者分工：先校验，再用本函数取原值做令牌查找。
func PresentedKey(r *http.Request) string {
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, bearerPrefix) {
		return authz[len(bearerPrefix):]
	}
	return r.Header.Get("x-api-key")
}
