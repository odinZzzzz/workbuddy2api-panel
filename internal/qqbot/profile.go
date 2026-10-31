package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// profile.go —— 群友画像：把 OneBot 能拿到的静态资料沉淀成人设印象。
//
// 为什么要做：光看聊天内容，龙王对每个人的"印象"都是扁平的。昵称/QQ 号位数/
// QQ 等级/个性签名/注册时间这些**元信息**恰恰是真人社交里最先形成印象的东西
// （"这号 5 位数，老东西"／"签名写这个，八成中二"）。取一次缓存一天，成本可忽略。

// Profile OneBot get_stranger_info 里本服务用到的子集。
type Profile struct {
	UID       int64     `json:"uid"`
	Nick      string    `json:"nick,omitempty"`
	Remark    string    `json:"remark,omitempty"`
	Sex       string    `json:"sex,omitempty"`
	Age       int       `json:"age,omitempty"`
	QQLevel   int       `json:"qq_level,omitempty"`
	QID       string    `json:"qid,omitempty"`
	Sign      string    `json:"sign,omitempty"`     // 个性签名（longNick）
	RegTime   int64     `json:"reg_time,omitempty"` // 注册时间（unix）
	Province  string    `json:"province,omitempty"`
	City      string    `json:"city,omitempty"`
	LoginDays int       `json:"login_days,omitempty"`
	Digits    int       `json:"digits,omitempty"` // QQ 号位数
	Avatar    string    `json:"avatar,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
}

// DigitsOf 返回 QQ 号位数（"老号/新号"判断用）。
func DigitsOf(uid int64) int {
	if uid <= 0 {
		return 0
	}
	return len(strconv.FormatInt(uid, 10))
}

// AccountAgeTier 用位数粗略判断"网龄"：位数越少号越老。
//
// 这是启发式，不是事实判断——写进 prompt 时用的是"看起来像"，不是断言。
func AccountAgeTier(digits int) string {
	switch {
	case digits == 0:
		return "未知"
	case digits <= 6:
		return "老号（资深网龄）"
	case digits <= 8:
		return "中等号"
	default:
		return "较新号"
	}
}

// AvatarURL QQ 头像直链（qlogo 稳定服务，不需要额外接口）。
func AvatarURL(uid int64) string {
	if uid <= 0 {
		return ""
	}
	return fmt.Sprintf("https://q1.qlogo.cn/g?b=qq&nk=%d&s=640", uid)
}

// fetchProfile 调 OneBot get_stranger_info 拉资料。
func (b *Bridge) fetchProfile(uid int64) (Profile, error) {
	p := Profile{UID: uid, Digits: DigitsOf(uid), Avatar: AvatarURL(uid)}
	body, _ := json.Marshal(map[string]any{"user_id": uid})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := b.cfg().OneBotAPI + "/get_stranger_info"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return p, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := b.cfg().OneBotToken; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		RetCode int `json:"retcode"`
		Data    struct {
			Nickname  string `json:"nickname"`
			Nick      string `json:"nick"`
			Remark    string `json:"remark"`
			Sex       string `json:"sex"`
			Age       int    `json:"age"`
			QQLevel   int    `json:"qqLevel"`
			QID       string `json:"qid"`
			LongNick  string `json:"longNick"`
			LongNick2 string `json:"long_nick"`
			RegTime   int64  `json:"regTime"`
			RegTime2  int64  `json:"reg_time"`
			Province  string `json:"province"`
			City      string `json:"city"`
			LoginDays int    `json:"login_days"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return p, fmt.Errorf("解析 get_stranger_info: %w", err)
	}
	if out.RetCode != 0 {
		return p, fmt.Errorf("get_stranger_info retcode=%d", out.RetCode)
	}
	d := out.Data
	p.Nick = firstNonEmpty(d.Nickname, d.Nick)
	p.Remark = d.Remark
	p.Sex = d.Sex
	p.Age = d.Age
	p.QQLevel = d.QQLevel
	p.QID = d.QID
	p.Sign = firstNonEmpty(d.LongNick, d.LongNick2)
	p.RegTime = firstNonZero(d.RegTime, d.RegTime2)
	p.Province = d.Province
	p.City = d.City
	p.LoginDays = d.LoginDays
	p.FetchedAt = time.Now()
	return p, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

// refreshProfileAsync 异步刷新画像（不阻塞回复；失败静默——资料是锦上添花）。
func (b *Bridge) refreshProfileAsync(uid int64) {
	if uid <= 0 || b.mem == nil || !b.cfg().ProfileEnabled {
		return
	}
	if u, ok := b.mem.Lookup(uid); ok && time.Since(u.ProfileAt) < 24*time.Hour && u.QQLevel > 0 {
		return // 一天内拉过，够新鲜
	}
	safeGo("refreshProfile", func() {
		p, err := b.fetchProfile(uid)
		if err != nil {
			return
		}
		b.mem.SetProfile(p)
	})
}

// ProfileLine 把画像压成一行人话（给 prompt 与后台）。
func (p Profile) Line() string {
	if p.UID == 0 {
		return ""
	}
	var parts []string
	if p.Nick != "" {
		parts = append(parts, "昵称「"+p.Nick+"」")
	}
	if p.Remark != "" && p.Remark != p.Nick {
		parts = append(parts, "群名片「"+p.Remark+"」")
	}
	parts = append(parts, fmt.Sprintf("QQ 号 %d 位（%s）", p.Digits, AccountAgeTier(p.Digits)))
	if p.QQLevel > 0 {
		parts = append(parts, fmt.Sprintf("QQ 等级 %d", p.QQLevel))
	}
	if p.Sign != "" {
		parts = append(parts, "签名「"+p.Sign+"」")
	}
	sex := map[string]string{"male": "男", "female": "女"}[p.Sex]
	if sex != "" && p.Age > 0 {
		parts = append(parts, fmt.Sprintf("%s·%d 岁", sex, p.Age))
	}
	if loc := strings.TrimSpace(p.Province + p.City); loc != "" {
		parts = append(parts, loc)
	}
	if p.RegTime > 0 {
		parts = append(parts, "注册于 "+time.Unix(p.RegTime, 0).Format("2006-01"))
	}
	return strings.Join(parts, " · ")
}
