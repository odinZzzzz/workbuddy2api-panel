// Package qqbot 把 QQ（OneBot v11）接到 workbuddy2api 网关。
//
// 定位：一个**极轻**的桥——只做「收消息 → 调网关 → 回消息」，不引 WebSocket 库、
// 不引框架，纯 net/http。目标是 20MB 级 RSS，跑在 1.7G 的小机器上不抢内存。
//
// 数据流（全程 HTTP，避免 WS 依赖）：
//
//	NapCat/Lagrange --HttpPost事件--> 本服务 /onebot
//	本服务 --POST /v1/chat/completions--> 网关
//	本服务 --POST /send_group_msg--> NapCat/Lagrange 的 HTTP API
//
// 安全默认：白名单为空 = **谁都不回**。宁可上线后没反应，也不要一上线就在群里刷屏。
package qqbot

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config 桥的配置（qqbot.json）。
type Config struct {
	// Enabled 总开关；false 时 /onebot 直接 503（便于灰度）。
	Enabled bool `json:"enabled"`
	// Listen 本服务监听地址，如 127.0.0.1:8090（只监听回环，不暴露公网）。
	Listen string `json:"listen"`
	// OneBotAPI 上游 OneBot 实现的 HTTP API 根地址（NapCat httpServers / Lagrange Http）。
	OneBotAPI string `json:"onebot_api"`
	// OneBotToken OneBot API 的鉴权 token（为空则不带头）。
	OneBotToken string `json:"onebot_token"`

	// GatewayBase 网关地址，含 /v1（如 http://127.0.0.1:7863/v1）。
	GatewayBase string `json:"gateway_base"`
	// GatewayKey 网关 api_key。
	GatewayKey string `json:"gateway_key"`
	// Model 模型名，支持网关的域前缀：cn:/global:/auto:。
	Model string `json:"model"`
	// SystemPrompt 给模型的人设（空则不注入 system）。
	SystemPrompt string `json:"system_prompt"`

	// AllowedGroups 允许的群号白名单（空 = 不回任何群）。
	AllowedGroups []int64 `json:"allowed_groups"`
	// AllowedUsers 允许的私聊 QQ 白名单（空 = 不回任何私聊）。
	AllowedUsers []int64 `json:"allowed_users"`
	// RequireAtInGroup 群里默认必须 @ 机器人才回（默认 true，防刷屏）。
	RequireAtInGroup bool `json:"require_at_in_group"`
	// NoAtGroups 例外群：这些群里**不需要 @** 也回（"监听所有消息"）。
	NoAtGroups []int64 `json:"no_at_groups"`
	// IgnoredUsers 忽略的 QQ 号（别的 bot、旧 bot 等）：不回复它们，避免 bot 互相回。
	IgnoredUsers []int64 `json:"ignored_users"`

	// BotPatterns 识别 bot 的昵称关键词（不区分大小写）。命中即按 bot 对话风控处理。
	BotPatterns []string `json:"bot_patterns"`
	// BotRoundLimit 与同一个 bot 在同一会话里最多接几轮（默认 3），超过则高冷退场。
	BotRoundLimit int `json:"bot_round_limit"`
	// BotCooldownSeconds 高冷退场的冷却时长（默认 600 秒）。
	BotCooldownSeconds int `json:"bot_cooldown_seconds"`

	// ActivationKeywords 激活词：出现任一（或被 @）才应声。
	ActivationKeywords []string `json:"activation_keywords"`
	// ActivationWindowSeconds 激活期长度（秒），期间每说一句自动续期。
	ActivationWindowSeconds int `json:"activation_window_seconds"`
	// KkrbEnabled 是否接入 kkrb.net 实时数据（每日密码等）；默认 true。
	KkrbEnabled bool `json:"kkrb_enabled"`
	// KkrbCacheMinutes 实时数据缓存时长（分钟，默认 10）。
	KkrbCacheMinutes int `json:"kkrb_cache_minutes"`
	// ChartFont 画图表文字用的字体（.ttf；.ttc 集合字体 opentype 解析不了）。
	ChartFont string `json:"chart_font"`

	// KBDir 外部知识库目录（*.md，首行 `keys: a, b`，其余为正文）；空 = 只用内置。
	KBDir string `json:"kb_dir"`
	// KBEnabled 是否注入预设知识库（三角洲相关事实卡）；默认 true。
	KBEnabled bool `json:"kb_enabled"`

	// PersonaDefaultPrompt 默认人格（善良热心好青年）的 system prompt。
	PersonaDefaultPrompt string `json:"persona_default_prompt"`
	// PersonaAltPrompt 已下线（单人格：病娇龙王；字段保留仅兼容旧配置）。
	PersonaAltPrompt string `json:"persona_alt_prompt"`
	// PersonaAltSeconds 已下线。
	PersonaAltSeconds int `json:"persona_alt_seconds"`
	// AngerThreshold 已下线。
	AngerThreshold int `json:"anger_threshold"`
	// AngerMildGain 轻度冒犯的怒气增量（默认 25）。
	AngerMildGain int `json:"anger_mild_gain"`
	// AngerDecay 正常/友善对话的怒气衰减（默认 12）。
	AngerDecay int `json:"anger_decay"`
	// MildKeywords 轻度冒犯词（阴阳怪气、轻微吐槽），攒怒气但不直接翻脸。
	MildKeywords []string `json:"mild_keywords"`

	// AttackKeywords 触发切龙王的关键词（被骂/挑衅）。
	AttackKeywords []string `json:"attack_keywords"`

	// ToolsEnabled 是否给模型工具（自绘表情/发表情/戳一戳/查记忆/记笔记）；默认 true。
	ToolsEnabled bool `json:"tools_enabled"`
	// MaxToolRounds 单次对话内最多几轮工具调用（防死循环）。
	MaxToolRounds int `json:"max_tool_rounds"`

	// ProfileEnabled 是否拉群友画像（昵称/等级/签名/号位数）；默认 true。
	ProfileEnabled bool `json:"profile_enabled"`

	// StickerTemplateDir 自定义 SVG 模板目录（同名 .svg 覆盖内置名画模板）。
	StickerTemplateDir string `json:"sticker_template_dir"`

	// DisableStickerCollect 关掉"收藏群聊表情"（默认 false = 开启）。
	// 用反向布尔：正向 bool 在旧配置文件里缺键时零值是 false，会把功能默认关掉（踩过）。
	DisableStickerCollect bool `json:"disable_sticker_collect"`
	// StickerCollectTotalMB 收藏目录总容量上限（MB，默认 50）：超了按最旧淘汰。
	// 只限单张不够——群里斗图一天就能堆几十 MB。
	StickerCollectTotalMB int `json:"sticker_collect_total_mb"`
	// StickerCollectMaxKB 收藏单张图片的大小上限（KB，默认 1024）。
	StickerCollectMaxKB int `json:"sticker_collect_max_kb"`

	// StickerDir 表情包目录（本地图片，随机挑一张发）；空 = 不支持发表情。
	StickerDir string `json:"sticker_dir"`
	// StickerCommand 发表情包的关键词（默认「表情」「来个表情」）。
	StickerCommand []string `json:"sticker_command"`
	// PokeBack 被戳一戳时是否回戳（默认 true）。
	PokeBack bool `json:"poke_back"`
	// PokeReply 被戳时的回话（空 = 不说话，只回戳）。
	PokeReply string `json:"poke_reply"`

	// CalmKeywords 冷静词：**@机器人 + 命中冷静词** → 直接进入冷静期（默认 5 分钟）。
	// 与 interrupt_keywords 的区别：中断词=短静默（默认 60s，谁都能触发）；
	// 冷静词=长冷静（需 @，用于"别刷屏了"的强指令），且能把已静默期升级加长。
	CalmKeywords []string `json:"calm_keywords"`
	// CalmSeconds 冷静期时长（默认 300 秒）。
	CalmSeconds int `json:"calm_seconds"`

	// InterruptKeywords 中断词：命中后立即闭嘴。
	InterruptKeywords []string `json:"interrupt_keywords"`
	// SilenceSeconds 静默期长度（秒）：期间任何消息都不处理（连激活词也不认）。
	SilenceSeconds int `json:"silence_seconds"`

	// AdminListen 管理后台监听地址（空 = 不启用）；admin_token 为空时只允许回环访问。
	AdminListen string `json:"admin_listen"`
	// AdminToken 后台访问 token（Bearer 或 ?token=）。
	AdminToken string `json:"admin_token"`

	// MemoryScope 会话上下文的作用域：
	//   "per_user"（默认）—— 群里按「群+人」隔离，每个人各自一段上下文；
	//   "per_chat"        —— 整个群共享一段（旧行为）。
	MemoryScope string `json:"memory_scope"`
	// MemoryFile 跨会话的用户记忆文件（昵称/见面次数/最近原话），空 = 不落盘。
	MemoryFile string `json:"memory_file"`
	// MemoryNotesPerUser 每用户保留几句原话（默认 5）。
	MemoryNotesPerUser int `json:"memory_notes_per_user"`

	// MaxHistoryPairs 每个会话保留多少轮上下文（0 = 无记忆）。
	MaxHistoryPairs int `json:"max_history_pairs"`
	// SessionTTLMinutes 会话闲置多久清空（0 = 不过期）。
	SessionTTLMinutes int `json:"session_ttl_minutes"`
	// RatePerMinute 单个 QQ 号每分钟最多几次（0 = 不限）。
	RatePerMinute int `json:"rate_per_minute"`
	// MaxReplyRunes 回复截断长度（QQ 单条有长度上限）。
	MaxReplyRunes int `json:"max_reply_runes"`
	// TimeoutSeconds 单次网关调用超时。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// Default 返回安全的默认值：白名单空、群里必须 @、无记忆。
func Default() Config {
	return Config{
		Enabled:            false,
		Listen:             "127.0.0.1:8090",
		OneBotAPI:          "http://127.0.0.1:8091",
		GatewayBase:        "http://127.0.0.1:7863/v1",
		Model:              "cn:deepseek-v4-flash",
		RequireAtInGroup:   true,
		MemoryScope:        "per_user",
		MemoryFile:         "data/users.json",
		MemoryNotesPerUser: 5,
		MaxHistoryPairs:    8,
		SessionTTLMinutes:  30,
		RatePerMinute:      6,
		MaxReplyRunes:      1200,
		TimeoutSeconds:     120,
		ChartFont:          "/usr/share/fonts/chinese/simhei.ttf",
		KkrbEnabled:        true,
		KkrbCacheMinutes:   10,
		KBEnabled:          true,
		PersonaAltSeconds:  600,
		AngerThreshold:     100,
		AngerMildGain:      25,
		AngerDecay:         12,
		MildKeywords:       []string{"蠢", "垃圾", "sb", "笨蛋", "白痴", "没用", "烦人", "菜"},
		// 只留明确的直接辱骂：蠢/垃圾/sb/笨蛋/白痴 这类容易误伤（"垃圾回收器"、
		// "这设计真蠢"），一律不算攻击——宁可漏判，也不要动不动切龙王。
		AttackKeywords: []string{"傻逼", "煞笔", "智障", "弱智", "你妈", "去死", "滚蛋", "狗东西", "人工智障", "蠢货"},
		ToolsEnabled:   true,
		MaxToolRounds:  3,
		ProfileEnabled: true,
		StickerCommand: []string{"表情", "来个表情", "发表情"},
		PokeBack:       true,
		PokeReply:      "戳什么戳？手痒就跪下。",
	}
}

// Load 读配置文件；文件不存在时返回默认配置（Enabled=false），不报错——
// 让服务能先起来、由运维补配置，而不是因为少一个文件就 CrashLoop。
func Load(path string) (Config, error) {
	c := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("qqbot: 读取 %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("qqbot: 解析 %s: %w", path, err)
	}
	c.normalize()
	return c, nil
}

func (c *Config) normalize() {
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = "127.0.0.1:8090"
	}
	if strings.TrimSpace(c.OneBotAPI) == "" {
		c.OneBotAPI = "http://127.0.0.1:8091"
	}
	if strings.TrimSpace(c.GatewayBase) == "" {
		c.GatewayBase = "http://127.0.0.1:7863/v1"
	}
	c.GatewayBase = strings.TrimRight(c.GatewayBase, "/")
	c.OneBotAPI = strings.TrimRight(c.OneBotAPI, "/")
	if strings.TrimSpace(c.Model) == "" {
		c.Model = "cn:deepseek-v4-flash"
	}
	if c.KkrbCacheMinutes <= 0 {
		c.KkrbCacheMinutes = 10
	}
	if c.AngerThreshold <= 0 {
		c.AngerThreshold = 100
	}
	if c.AngerMildGain <= 0 {
		c.AngerMildGain = 25
	}
	if c.AngerDecay <= 0 {
		c.AngerDecay = 12
	}
	if c.MildKeywords == nil {
		c.MildKeywords = []string{"蠢", "垃圾", "sb", "笨蛋", "白痴", "没用", "烦人", "菜"}
	}
	if c.PersonaAltSeconds <= 0 {
		c.PersonaAltSeconds = 600
	}
	if c.AttackKeywords == nil {
		c.AttackKeywords = []string{"傻逼", "煞笔", "智障", "弱智", "你妈", "去死", "滚蛋", "狗东西", "人工智障", "蠢货"}
	}
	if c.MaxToolRounds <= 0 {
		c.MaxToolRounds = 3
	}
	if c.StickerCommand == nil {
		c.StickerCommand = []string{"表情", "来个表情", "发表情"}
	}
	// 收藏配额：0 会被 enforceStickerQuota 当成「不限」直接跳过，必须在这里兜底，
	// 否则旧配置文件（没有这两个键）等于永久无上限。
	if c.StickerCollectMaxKB <= 0 {
		c.StickerCollectMaxKB = 1024
	}
	if c.StickerCollectTotalMB <= 0 {
		c.StickerCollectTotalMB = 50
	}
	// admin_token 支持环境变量覆盖：配置文件里存明文 token 不合适（会跟着备份走）。
	if v := strings.TrimSpace(os.Getenv("QQBOT_ADMIN_TOKEN")); v != "" {
		c.AdminToken = v
	}
	if c.CalmSeconds <= 0 {
		c.CalmSeconds = 300
	}
	if c.CalmKeywords == nil {
		c.CalmKeywords = []string{"冷静", "安静点", "别吵"}
	}
	if c.BotRoundLimit <= 0 {
		c.BotRoundLimit = 3
	}
	if c.BotCooldownSeconds <= 0 {
		c.BotCooldownSeconds = 600
	}
	if c.BotPatterns == nil {
		c.BotPatterns = []string{"bot", "机器人", "助手", "gpt", "ai"}
	}
	if c.ActivationWindowSeconds <= 0 {
		c.ActivationWindowSeconds = 300
	}
	if c.SilenceSeconds <= 0 {
		c.SilenceSeconds = 60
	}
	if c.ActivationKeywords == nil {
		c.ActivationKeywords = []string{"小八嘎", "龙王", "我去"}
	}
	if c.InterruptKeywords == nil {
		c.InterruptKeywords = []string{"闭嘴", "退下", "别说了", "安静", "/stop"}
	}
	if strings.TrimSpace(c.MemoryScope) == "" {
		c.MemoryScope = "per_user"
	}
	if c.MemoryNotesPerUser <= 0 {
		c.MemoryNotesPerUser = 5
	}
	if c.MaxHistoryPairs < 0 {
		c.MaxHistoryPairs = 0
	}
	if c.SessionTTLMinutes < 0 {
		c.SessionTTLMinutes = 0
	}
	if c.RatePerMinute < 0 {
		c.RatePerMinute = 0
	}
	if c.MaxReplyRunes <= 0 {
		c.MaxReplyRunes = 1200
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 120
	}
}

// AllowsGroup 群白名单判定。
func (c Config) AllowsGroup(groupID int64) bool {
	for _, g := range c.AllowedGroups {
		if g == groupID {
			return true
		}
	}
	return false
}

// AllowsUser 私聊白名单判定。
func (c Config) AllowsUser(userID int64) bool {
	for _, u := range c.AllowedUsers {
		if u == userID {
			return true
		}
	}
	return false
}

// RequiresAt 该群是否必须 @ 机器人才回。
func (c Config) RequiresAt(groupID int64) bool {
	if !c.RequireAtInGroup {
		return false
	}
	for _, g := range c.NoAtGroups {
		if g == groupID {
			return false
		}
	}
	return true
}

// Ignores 该 QQ 号是否应被忽略（别的 bot、旧 bot 自己等）。
func (c Config) Ignores(userID int64) bool {
	for _, u := range c.IgnoredUsers {
		if u == userID {
			return true
		}
	}
	return false
}

// ScopePerUser 群聊上下文是否按人隔离。
func (c Config) ScopePerUser() bool {
	return strings.TrimSpace(c.MemoryScope) != "per_chat"
}
