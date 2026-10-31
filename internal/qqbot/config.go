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
	// RequireAtInGroup 群里必须 @ 机器人才回（默认 true，防刷屏）。
	RequireAtInGroup bool `json:"require_at_in_group"`

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
		Enabled:           false,
		Listen:            "127.0.0.1:8090",
		OneBotAPI:         "http://127.0.0.1:8091",
		GatewayBase:       "http://127.0.0.1:7863/v1",
		Model:             "cn:deepseek-v4-flash",
		RequireAtInGroup:  true,
		MaxHistoryPairs:   3,
		SessionTTLMinutes: 30,
		RatePerMinute:     6,
		MaxReplyRunes:     1200,
		TimeoutSeconds:    120,
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
