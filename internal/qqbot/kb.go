package qqbot

import (
	"strings"
)

// kb.go —— 三角洲行动知识库（预设，可扩展）。
//
// 定位：群友问游戏相关问题时，给模型一份**短小的事实底稿**，避免它凭印象编
// （地图名、密码规则、工具在哪查）。条目刻意短：KB 是"事实卡"，不是攻略书。
//
// 结构：每条有 关键词（命中就注入） + 正文。命中方式是最朴素的子串匹配——够用、
// 可预测、零依赖；比向量检索更适合这种几十条的小库。
//
// 数据来源：kkrb.net（三角洲行动一图流）实测抓取 + 公开资料；每日密码是实时接口
// （见 kkrb.go），不写死在 KB 里。

// KBEntry 一条知识。
type KBEntry struct {
	Keys []string `json:"keys"`
	Text string   `json:"text"`
}

// kbEntries 内置知识库。
var kbEntries = []KBEntry{
	{
		Keys: []string{"每日密码", "密码门", "今天密码", "密码是多少", "地图密码"},
		Text: "每日地图密码：每张图有一处密码门/保险箱，密码每天 0 点刷新，六张图各不相同。\n" +
			"数据源是 kkrb.net 的实时接口，直接问「今日密码」我就去查（有 10 分钟缓存）。\n" +
			"地图代号：零号大坝 / 长弓溪谷 / 巴克什 / 航天基地 / 潮汐监狱 / AZ3。",
	},
	{
		Keys: []string{"烽火地带", "撤离模式", "撤离", "摸金"},
		Text: "烽火地带（撤离玩法）：带装备进图搜刮，撤出才算收益，死了全丢。\n" +
			"核心是路线规划与风险控制：撤离点位置、卡战备（装备价值门槛）、钥匙房收益、容器刷新点。\n" +
			"新手建议先跑熟一张图（零号大坝最好上手），别一上来堆贵装备。",
	},
	{
		Keys: []string{"全面战场", "大战场", "攻防", "64人"},
		Text: "全面战场（大战场）：大规模攻防对抗，含载具与固定点位争夺。\n" +
			"和烽火地带是两套玩法：大战场拼团队与兵种配合，烽火地带拼搜刮与撤离。",
	},
	{
		Keys: []string{"卡战备", "战备值", "装备门槛"},
		Text: "卡战备：按对局要求的装备价值下限凑够装备分（不是越贵越好，够线即可）。\n" +
			"省钱的常见做法是低价枪械+高价值配件/弹药凑线；kkrb 有「自动搭配卡战备」工具可以直接抄。",
	},
	{
		Keys: []string{"钥匙房", "钥匙", "门卡"},
		Text: "钥匙房：用钥匙/门卡开的高价值房间，出货看脸但期望收益通常高于普通容器。\n" +
			"注意钥匙有耐久/次数，别在低价值局里浪费。",
	},
	{
		Keys: []string{"特勤处", "制造", "工作台", "制药台", "防具台"},
		Text: "特勤处：用材料制造物品再卖，赚差价。四类台子（技术中心/工作台/制药台/防具台）产物利润差异大。\n" +
			"kkrb 首页每天给「制造产物推荐」：当前利润、理想售价、建议卖出时段（价格随时段波动）。",
	},
	{
		Keys: []string{"交易行", "物价", "利润", "倒卖"},
		Text: "交易行：玩家间买卖，价格随时段波动（凌晨/上午/中午差异明显）。\n" +
			"kkrb 提供「高价格浮动制造材料」「兑换子弹利润」等榜单，适合抄作业。",
	},
	{
		Keys: []string{"工具", "在哪查", "网站", "一图流", "kkrb", "kkrb.net"},
		Text: "三角洲常用工具站：kkrb.net（三角洲行动一图流，作者 @Key725）。\n" +
			"首页 https://www.kkrb.net/?viewpage=overview 汇总每日密码、制造利润、子弹热度、材料浮动价。\n" +
			"常用子工具：密码门密码、制造产物利润、热门查询子弹前十、自动搭配卡战备、综合点位地图工具、\n" +
			"钥匙房信息、武器/防具基础数据一览、瞄具开镜样式、NPC血量护甲数据、胸挂背包内部样式、\n" +
			"特勤处升级花销、扩容箱兑换价格、模拟容器搜索、活动日历、新手攻略、三角洲小知识。\n" +
			"官方 Q 群 1035748373（站长的群，不是我们的）。",
	},
	{
		Keys: []string{"地图", "零号大坝", "长弓溪谷", "巴克什", "航天基地", "潮汐监狱", "az3"},
		Text: "当前六张图：零号大坝（新手友好、结构简单）、长弓溪谷（开阔、狙击多）、巴克什（城区巷战）、\n" +
			"航天基地（室内多、CQB）、潮汐监狱（多层、风险高）、AZ3（较小、节奏快）。\n" +
			"具体点位/撤离点/钥匙房位置建议查 kkrb 的「综合点位地图工具」，我只给方向不给坐标。",
	},
}

// matchKB 返回与本条消息相关的知识（最多 limit 条）。
//
// 命中即注入；多条命中按命中关键词数量排序（越相关越靠前）。
func matchKB(text string, limit int) []KBEntry {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lower := strings.ToLower(text)
	type scored struct {
		e     KBEntry
		score int
	}
	var hits []scored
	for _, e := range kbEntries {
		score := 0
		for _, k := range e.Keys {
			if k != "" && strings.Contains(lower, strings.ToLower(k)) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{e, score})
		}
	}
	// 简单插入排序（条数很少）
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].score > hits[j-1].score; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]KBEntry, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.e)
	}
	return out
}

// kbContext 把命中的知识拼成给模型的一段文字（无命中返回空串）。
func kbContext(text string) string {
	hits := matchKB(text, 2)
	if len(hits) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("【参考资料（来自预设知识库，优先于你的记忆；不确定就说不确定）】\n")
	for _, e := range hits {
		sb.WriteString("- ")
		sb.WriteString(e.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}
