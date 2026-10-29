// Package tokenstore 多令牌（API Key）管理：像 one-api / new-api 那类中转站一样，
// 一个网关可以签发多把 key，每把独立启停、过期时间、积分配额与用量统计。
//
// 与全局 api_key 的关系：全局 api_key（config.json 顶层）**始终有效**，令牌是叠加的
// 额外通道——老部署不建令牌时行为完全不变。这样升级不需要迁移任何配置。
//
// 持久化：data/tokens.json，原子写（tmp + rename，0600），与 pool 的 state.json 同风格。
package tokenstore

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 鉴权失败的细分原因：调用方据此给出可读错误（401 / 429 / 403），
// 而不是笼统的 invalid_api_key——排障时"哪把 key 为什么被拒"必须一眼看清。
var (
	ErrNotFound      = errors.New("token not found")
	ErrDisabled      = errors.New("token disabled")
	ErrExpired       = errors.New("token expired")
	ErrQuotaExceeded = errors.New("token quota exceeded")
	ErrModelDenied   = errors.New("model not allowed for this token")
)

// Token 一把令牌。Key 存明文（本机私有部署、面板需要展示与复制）；
// 对外列表接口会打码，只有新建时返回完整值。
type Token struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Key           string    `json:"key"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"` // 零值 = 永不过期
	QuotaCredits  float64   `json:"quota_credits"`        // 0 = 不限
	UsedCredits   float64   `json:"used_credits"`
	UsedRequests  int64     `json:"used_requests"`
	AllowedModels []string  `json:"allowed_models,omitempty"` // 空 = 全部模型
	Note          string    `json:"note,omitempty"`
}

// Expired 报告令牌是否已过期（零值 ExpiresAt 表示永不过期）。
func (t *Token) Expired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt)
}

// QuotaExceeded 报告是否已用满配额（QuotaCredits=0 表示不限）。
func (t *Token) QuotaExceeded() bool {
	return t.QuotaCredits > 0 && t.UsedCredits >= t.QuotaCredits
}

// AllowsModel 判断该令牌是否允许访问指定模型（空名单 = 全部允许）。
func (t *Token) AllowsModel(model string) bool {
	if len(t.AllowedModels) == 0 {
		return true
	}
	bare := model
	if i := strings.LastIndex(model, ":"); i >= 0 {
		bare = model[i+1:] // 允许名单按裸名写，容忍 cn:/global: 前缀
	}
	for _, m := range t.AllowedModels {
		if m == model || m == bare {
			return true
		}
	}
	return false
}

// MaskedKey 返回打码后的密钥（sk-abcd…wxyz），用于列表展示。
func MaskedKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:8] + "…" + key[len(key)-4:]
}

// Store 令牌集合。所有读写都在 mu 下进行；写操作标记 dirty 并由 Save 落盘。
type Store struct {
	mu    sync.Mutex
	path  string
	byID  map[string]*Token
	byKey map[string]*Token
	dirty bool
}

type fileFormat struct {
	Version int      `json:"version"`
	Tokens  []*Token `json:"tokens"`
}

// Open 加载（或初始化）令牌库。path 为空表示纯内存（测试用）。
func Open(path string) (*Store, error) {
	s := &Store{path: path, byID: map[string]*Token{}, byKey: map[string]*Token{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // 首次运行：空库
		}
		return nil, fmt.Errorf("tokenstore: 读取 %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, nil
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("tokenstore: 解析 %s: %w", path, err)
	}
	for _, t := range f.Tokens {
		if t == nil || t.ID == "" || t.Key == "" {
			continue // 脏数据直接跳过，不让一条坏记录拖垮整个库
		}
		s.byID[t.ID] = t
		s.byKey[t.Key] = t
	}
	return s, nil
}

// Save 原子落盘（tmp + rename，0600）。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		s.dirty = false
		return nil
	}
	list := make([]*Token, 0, len(s.byID))
	for _, t := range s.byID {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.MarshalIndent(fileFormat{Version: 1, Tokens: list}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	// 显式 chmod：os.WriteFile 的 perm 参数只在创建时生效，且 Windows 上映射有限，
	// 这里补一次，保证 Unix 部署下令牌文件确实是 0600（里面是明文密钥）。
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.dirty = false
	return nil
}

// List 返回令牌副本（按创建时间升序），调用方随便改不影响库。
func (s *Store) List() []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Token, 0, len(s.byID))
	for _, t := range s.byID {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Create 新建令牌。返回含完整 key 的副本（面板只在此时展示完整值）。
func (s *Store) Create(name string, quotaCredits float64, expiresAt time.Time, allowedModels []string, note string) (Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名令牌"
	}
	if quotaCredits < 0 {
		return Token{}, fmt.Errorf("tokenstore: quota_credits 不能为负")
	}
	key, err := newKey()
	if err != nil {
		return Token{}, err
	}
	t := &Token{
		ID:            newID(),
		Name:          name,
		Key:           key,
		Enabled:       true,
		CreatedAt:     time.Now(),
		ExpiresAt:     expiresAt,
		QuotaCredits:  quotaCredits,
		AllowedModels: normalizeModels(allowedModels),
		Note:          strings.TrimSpace(note),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[t.ID] = t
	s.byKey[t.Key] = t
	s.dirty = true
	if err := s.saveLocked(); err != nil {
		return Token{}, err
	}
	return *t, nil
}

// Delete 删除令牌。
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return false
	}
	delete(s.byID, id)
	delete(s.byKey, t.Key)
	s.dirty = true
	_ = s.saveLocked()
	return true
}

// SetEnabled 启停令牌。
func (s *Store) SetEnabled(id string, enabled bool) (Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return Token{}, false
	}
	t.Enabled = enabled
	s.dirty = true
	_ = s.saveLocked()
	return *t, true
}

// ResetUsage 重置用量（不动配额本身）。
func (s *Store) ResetUsage(id string) (Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return Token{}, false
	}
	t.UsedCredits = 0
	t.UsedRequests = 0
	s.dirty = true
	_ = s.saveLocked()
	return *t, true
}

// Authenticate 校验一把 key，返回命中的令牌。错误区分 disabled / expired / quota，
// 便于调用方给出可读原因（这类"被拒但不知道为啥"最耗排障时间）。
func (s *Store) Authenticate(key string) (*Token, error) {
	if key == "" {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.lookupLocked(key)
	if t == nil {
		return nil, ErrNotFound
	}
	if !t.Enabled {
		return nil, ErrDisabled
	}
	if t.Expired(time.Now()) {
		return nil, ErrExpired
	}
	if t.QuotaExceeded() {
		return nil, ErrQuotaExceeded
	}
	cp := *t
	return &cp, nil
}

// lookupLocked 按 key 找令牌。用摘要做常量时间比较，避免逐字节比较泄露前缀信息
// （与 httpauth 同口径；令牌数量通常很少，全表扫一遍完全可接受）。
func (s *Store) lookupLocked(key string) *Token {
	if t, ok := s.byKey[key]; ok {
		return t // 快路径：精确命中
	}
	want := sha256.Sum256([]byte(key))
	for k, t := range s.byKey {
		got := sha256.Sum256([]byte(k))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			return t
		}
	}
	return nil
}

// AddUsage 给令牌记账（积分 + 请求数）。credits<=0 时只记请求数。
// 记账失败不影响请求本身——它只是统计，不该让一次成功的对话变成错误。
func (s *Store) AddUsage(id string, credits float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return
	}
	t.UsedRequests++
	if credits > 0 {
		t.UsedCredits += credits
	}
	s.dirty = true
	_ = s.saveLocked()
}

// AddUsageByKey 便于在只有 key 的地方记账（当前主链路用 id）。
func (s *Store) AddUsageByKey(key string, credits float64) {
	s.mu.Lock()
	t := s.lookupLocked(key)
	s.mu.Unlock()
	if t != nil {
		s.AddUsage(t.ID, credits)
	}
}

func normalizeModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func newKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
