package qqbot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// memory.go —— 按 QQ 号持久化的「人设记忆」。
//
// 为什么需要：群聊里所有人共享一份上下文时，龙王记不住"谁"说过什么——A 的梗会
// 串到 B 身上，人设也没法对同一个人保持连贯（上次骂过他，这次却像初次见面）。
// 这里给每个 QQ 号存一份轻量记忆：昵称、见他的次数、最近几句原话。
//
// 设计取舍：
//   - 只存"最近 N 句原话"而不做摘要：摘要要额外调模型（花钱、变慢），而群聊里
//     "他上次说了啥"用原话最省也最像人。
//   - 落盘用 JSON + 定期合并写（跟 tokenstore 同思路），崩溃最多丢几秒的观察。
//   - 记忆是**只增不减的观察**，不做"遗忘"策略：量级很小（每用户几行文本）。

// UserMemory 单个 QQ 号的记忆。
type UserMemory struct {
	UID       int64     `json:"uid"`
	Nick      string    `json:"nick,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Count     int       `json:"count"`
	Notes     []string  `json:"notes,omitempty"`
	// Verdict 上一次对这个人下的点评（每轮收尾那句"〔点评：…〕"），下次说话会带上。
	Verdict string `json:"verdict,omitempty"`
	// Affinity 好感度 -100~100：低=更刻薄，高=勉强认可。由确定性规则增减（不调模型）。
	Affinity int `json:"affinity,omitempty"`

	// 以下来自 OneBot get_stranger_info 的静态画像（一天拉一次，见 profile.go）
	QQLevel   int       `json:"qq_level,omitempty"`
	Sign      string    `json:"sign,omitempty"`
	Sex       string    `json:"sex,omitempty"`
	Age       int       `json:"age,omitempty"`
	City      string    `json:"city,omitempty"`
	Digits    int       `json:"digits,omitempty"`
	Avatar    string    `json:"avatar,omitempty"`
	ProfileAt time.Time `json:"profile_at,omitempty"`
}

// Memory 记忆库。
type Memory struct {
	mu    sync.Mutex
	path  string
	notes int // 每用户保留几句
	users map[int64]*UserMemory
	dirty bool
}

// OpenMemory 打开（或新建）记忆库。path 为空 = 纯内存（测试用）。
func OpenMemory(path string, notesPerUser int) (*Memory, error) {
	if notesPerUser <= 0 {
		notesPerUser = 5
	}
	m := &Memory{path: path, notes: notesPerUser, users: map[int64]*UserMemory{}}
	if path == "" {
		return m, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("qqbot memory: 创建目录: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return m, nil
	}
	var file struct {
		Users []*UserMemory `json:"users"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("qqbot memory: 解析 %s: %w", path, err)
	}
	for _, u := range file.Users {
		if u == nil || u.UID == 0 {
			continue
		}
		m.users[u.UID] = u
	}
	return m, nil
}

// Observe 记录一次发言（昵称会被最新值覆盖）。
func (m *Memory) Observe(uid int64, nick, text string) {
	if uid == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	u := m.users[uid]
	if u == nil {
		u = &UserMemory{UID: uid, FirstSeen: now}
		m.users[uid] = u
	}
	if nick != "" {
		u.Nick = nick
	}
	u.LastSeen = now
	u.Count++
	if t := strings.TrimSpace(text); t != "" {
		// 单条截断：记忆是给人设用的提示，不是聊天归档
		if r := []rune(t); len(r) > 60 {
			t = string(r[:60]) + "…"
		}
		u.Notes = append(u.Notes, t)
		if len(u.Notes) > m.notes {
			u.Notes = u.Notes[len(u.Notes)-m.notes:]
		}
	}
	m.dirty = true
}

// Describe 生成给 prompt 用的记忆片段（无记忆返回空串）。
func (m *Memory) Describe(uid int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[uid]
	if u == nil || (u.Count == 0 && len(u.Notes) == 0) {
		return ""
	}
	var b strings.Builder
	name := u.Nick
	if name == "" {
		name = "（无名）"
	}
	fmt.Fprintf(&b, "你记得这个凡人：%s，已见过 %d 次", name, u.Count)
	if !u.FirstSeen.IsZero() {
		fmt.Fprintf(&b, "，第一次是 %s", u.FirstSeen.Format("2006-01-02"))
	}
	b.WriteString("。")
	if len(u.Notes) > 0 {
		b.WriteString("他之前说过：")
		b.WriteString(strings.Join(u.Notes, "；"))
		b.WriteString("。")
	}
	if line := u.ProfileLine(); line != "" {
		b.WriteString("你对他的印象：")
		b.WriteString(line)
		b.WriteString("。")
	}
	fmt.Fprintf(&b, "你对他的好感度 %d/100（%s）。", u.Affinity, AffinityTier(u.Affinity))
	if u.Verdict != "" {
		b.WriteString("你上次对他的点评是：")
		b.WriteString(u.Verdict)
		b.WriteString("。")
	}
	b.WriteString("你要按龙王的性子接着对他说话（记得他，别当第一次见）。")
	return b.String()
}

// Flush 落盘（脏了才写）。
func (m *Memory) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dirty || m.path == "" {
		m.dirty = false
		return nil
	}
	list := make([]*UserMemory, 0, len(m.users))
	for _, u := range m.users {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].UID < list[j].UID })
	raw, err := json.MarshalIndent(map[string]any{"version": 1, "users": list}, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	m.dirty = false
	return nil
}

// Count 已记录的用户数（观测/测试用）。
func (m *Memory) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users)
}

// SetVerdict 记录对某个人的点评（覆盖上一次）。
func (m *Memory) SetVerdict(uid int64, verdict string) {
	verdict = strings.TrimSpace(verdict)
	if uid == 0 || verdict == "" {
		return
	}
	if r := []rune(verdict); len(r) > 40 {
		verdict = string(r[:40])
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[uid]
	if u == nil {
		u = &UserMemory{UID: uid, FirstSeen: time.Now()}
		m.users[uid] = u
	}
	u.Verdict = verdict
	m.dirty = true
}

// Lookup 取某个 QQ 号的记忆副本（不存在返回 false）。
func (m *Memory) Lookup(uid int64) (UserMemory, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[uid]
	if u == nil {
		return UserMemory{}, false
	}
	cp := *u
	cp.Notes = append([]string(nil), u.Notes...)
	return cp, true
}

// 好感度增减的事件权重（确定性规则，不调模型——省钱且可解释）。
const (
	AffinityPerChat      = 1  // 正常聊一轮
	AffinityPerAt        = 2  // 主动 @ 机器人
	AffinityPerInterrupt = -5 // 被"闭嘴/退下"打断
	AffinityPerCalm      = -8 // 被"@+冷静"强制冷静
)

// AffinityTier 把分数映射成档位标签（给 prompt 与后台看）。
func AffinityTier(v int) string {
	switch {
	case v <= -30:
		return "厌恶"
	case v < 0:
		return "看不顺眼"
	case v < 20:
		return "蝼蚁而已"
	case v < 50:
		return "勉强顺眼"
	case v < 80:
		return "还算识相"
	default:
		return "本王认了"
	}
}

// AddAffinity 调整好感度（-100~100 夹紧），返回调整后的值。
func (m *Memory) AddAffinity(uid int64, delta int) int {
	if uid == 0 || delta == 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[uid]
	if u == nil {
		u = &UserMemory{UID: uid, FirstSeen: time.Now()}
		m.users[uid] = u
	}
	u.Affinity += delta
	if u.Affinity > 100 {
		u.Affinity = 100
	}
	if u.Affinity < -100 {
		u.Affinity = -100
	}
	m.dirty = true
	return u.Affinity
}

// ProfileLine 把记忆里存的画像压成一行人话。
func (u *UserMemory) ProfileLine() string {
	p := Profile{
		UID: u.UID, Nick: u.Nick, Sex: u.Sex, Age: u.Age, QQLevel: u.QQLevel,
		Sign: u.Sign, City: u.City, Digits: u.Digits, Avatar: u.Avatar,
	}
	if p.Digits == 0 {
		p.Digits = DigitsOf(u.UID)
	}
	return p.Line()
}

// SetProfile 把拉到的画像写进记忆（只覆盖非空字段，避免把已有信息抹掉）。
func (m *Memory) SetProfile(p Profile) {
	if p.UID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[p.UID]
	if u == nil {
		u = &UserMemory{UID: p.UID, FirstSeen: time.Now()}
		m.users[p.UID] = u
	}
	if p.Nick != "" {
		u.Nick = p.Nick
	}
	if p.Remark != "" {
		u.Nick = p.Remark
	}
	if p.QQLevel > 0 {
		u.QQLevel = p.QQLevel
	}
	if p.Sign != "" {
		u.Sign = p.Sign
	}
	if p.Sex != "" {
		u.Sex = p.Sex
	}
	if p.Age > 0 {
		u.Age = p.Age
	}
	if p.City != "" || p.Province != "" {
		u.City = strings.TrimSpace(p.Province + p.City)
	}
	u.Digits = p.Digits
	if p.Avatar != "" {
		u.Avatar = p.Avatar
	}
	u.ProfileAt = time.Now()
	m.dirty = true
}
