package server

import "strings"

// resolveModel 解析模型名协议（PLAN D6）：
//
//	分布式前缀： "[realm:]model"
//
// 取第一个 ":"，前段恰为 "cn"/"global" 才剥离；否则视为裸名，realm=cn、bare=原串。
// 大小写敏感（前缀必须是精确的小写枚举）。bare 即出站/选号/账本使用的裸模型名。
//
// 导出为 ResolveModel（cmd/server/main.go 粘性闭包需要），包内简写 resolveModel。
func resolveModel(model string) (realm, bare string) {
	idx := strings.IndexByte(model, ':')
	if idx < 0 {
		return "cn", model
	}
	prefix := model[:idx]
	if prefix != "cn" && prefix != "global" {
		return "cn", model
	}
	return prefix, model[idx+1:]
}

// ResolveModel 是 resolveModel 的导出面（跨包调用）。
func ResolveModel(model string) (realm, bare string) { return resolveModel(model) }

// autoRealmPrefix 跨域路由前缀：`auto:<model>` 表示"按优先级依次尝试多个域"。
//
// 为什么需要：同一模型可能两个域都有（如 deepseek-v4.1-flash），但某一域会整域
// 不可用（账号全冷却 / 全熔断 / 无额度）。写死 `global:` 的客户端会在 global 全挂时
// 直接 503，而 cn 明明有号可用；写死 `cn:` 又享受不到 global 更便宜的额度。
// `auto:` 把"优先谁、失败退谁"收敛到网关侧一处配置（pool.realm_priority），
// 客户端只认一个模型名。
const autoRealmPrefix = "auto"

// DefaultRealmPriority 默认跨域优先级：global 优先，其次 cn。
// 与 config 缺省一致（pool.realm_priority 未配置时用这个）。
var DefaultRealmPriority = []string{"global", "cn"}

// normalizeRealmPriority 清洗优先级列表：只保留 cn/global、去重、保序；
// 结果为空时回落 DefaultRealmPriority（配置写错不至于让 auto: 完全不可用）。
func normalizeRealmPriority(in []string) []string {
	out := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	for _, r := range in {
		r = strings.ToLower(strings.TrimSpace(r))
		if r != "cn" && r != "global" {
			continue
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultRealmPriority...)
	}
	return out
}

// resolveModelPlan 解析模型名 → (候选域序列, 裸模型名)。
//
//	"cn:x"      → ["cn"],        "x"
//	"global:x"  → ["global"],    "x"
//	"auto:x"    → priority(默认 ["global","cn"]), "x"
//	"x" / 未知前缀 → ["cn"],     原串（裸名历史语义零回归）
//
// 返回值保证 realms 非空，调用方可以放心取 realms[0]。
func resolveModelPlan(model string, priority []string) (realms []string, bare string) {
	idx := strings.IndexByte(model, ':')
	if idx < 0 {
		return []string{"cn"}, model
	}
	prefix := model[:idx]
	switch prefix {
	case "cn":
		return []string{"cn"}, model[idx+1:]
	case "global":
		return []string{"global"}, model[idx+1:]
	case autoRealmPrefix:
		return normalizeRealmPriority(priority), model[idx+1:]
	default:
		return []string{"cn"}, model
	}
}

// isAutoModel 判断模型名是否走跨域路由（供 /v1/models 与日志标注使用）。
func isAutoModel(model string) bool {
	idx := strings.IndexByte(model, ':')
	return idx > 0 && model[:idx] == autoRealmPrefix
}
