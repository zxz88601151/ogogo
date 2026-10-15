// Package auth 实现 TP-02 教师认证与 RBAC 授权（对齐设计 08 §3.1 / ADR-018）。
//
// TP-02 决策（Phase 1 简化方案，Owner 待确认）：
//   账号 + 密码（PBKDF2-HMAC-SHA256 加盐哈希）→ 服务端签发会话令牌（HMAC-SHA256）。
//   令牌仅承载身份与角色，不含任何个人信息；不引入外部 IdP。
//
// RBAC 边界（ADR-018，不可突破）：
//   - teacher：本版本最高活跃权限，可创建/编辑/撤回内容；
//   - academic_offairs（教务处）：reserved 预留角色，本版本不激活，
//     在「登录/会话恢复/API 授权/发布入口」四关全部拒绝；
//   - admin（系统管理员）：仅终端运维，不参与内容发布。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Role 角色。
type Role string

const (
	RoleTeacherRole      Role = "teacher"            // 最高活跃权限
	RoleAcademicOffices Role = "academic_offices"  // reserved，预留不激活
	RoleAdmin            Role = "admin"              // 系统管理员，仅运维
)

// Errors 认证/授权错误（可安全返回给前端，不泄露内部细节）。
var (
	ErrInvalidCredentials = errors.New("账号或密码不正确")
	ErrTokenInvalid       = errors.New("登录状态无效，请重新登录")
	ErrTokenExpired       = errors.New("登录已过期，请重新登录")
	ErrTokenRevoked       = errors.New("登录状态已注销，请重新登录")
	ErrRoleNotActivated   = errors.New("该角色在本版本未开放")
	ErrForbidden          = errors.New("没有执行该操作的权限")
)

// User 用户记录（落盘于 users.json，不保存明文口令）。
type User struct {
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Role         Role   `json:"role"`
	PasswordHash string `json:"password_hash"` // pbkdf2-sha256$iterations$salt$hash
	Active       bool   `json:"active"`
}

// Session 会话。
type Session struct {
	// TokenID（jti）令牌唯一编号：撤回清单以它为键，与令牌签名互不依赖。
	TokenID     string    `json:"jti"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        Role      `json:"role"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// TokenTtl 会话有效期（G-4 建议值，需校方签字；此处为技术默认）。
const TokenTtl = 8 * time.Hour

// MinSecretLen 生产密钥最小长度（P0-1 fail-closed 红线）。
const MinSecretLen = 32

// ErrWeakSecret 密钥不合规（缺失或过短）。
var ErrWeakSecret = fmt.Errorf("签名密钥不合规：需 ≥%d 字节（请设置 EDUDESK_SECRET，或用 --dev 显式降级）", MinSecretLen)

// SecretOK 校验签名密钥是否达到生产强度。
//
// P0-1：默认值 `dev-only-secret-change-me` 一旦被带到生产，等同于把 Bundle 签名私钥公开
//（终端验签密钥随前端下发，见 implementation/README.md）。因此这里 fail-closed：
// 密钥缺失或长度不足时由调用方**拒绝启动**，而不是静默降级。
func SecretOK(secret string) error {
	if len(secret) < MinSecretLen {
		return ErrWeakSecret
	}
	if secret == DevSecret {
		return ErrWeakSecret
	}
	return nil
}

// DevSecret 演示密钥（仅供 --dev 本地调试，生产禁止）。
const DevSecret = "dev-only-secret-change-me"

// Store 用户存储与令牌密钥。
type Store struct {
	users    map[string]User
	secret   []byte
	tokenTTL time.Duration

	// ---- 令牌吊销清单（P1-7）----
	// 无状态 HMAC 令牌无法在不查表的情况下提前失效；登出/改密/角色停用必须能立即止损，
	// 因此维护一份 jti 名单。文件名单即可满足单机 MVP，不必上 Redis。
	mu      sync.RWMutex
	revoked map[string]time.Time // jti -> 令牌自身到期时间（到期即可安全淘汰）
	revPath string               // 空 = 仅内存（测试/演示）
}

// NewStore 从 users.json 加载用户（吊销清单仅驻内存）。
func NewStore(path, secret string) (*Store, error) {
	return NewStoreWithRevocations(path, secret, "")
}

// NewStoreWithRevocations 同 NewStore，但把吊销清单持久化到 revPath。
//
// 持久化的意义：服务重启不能让已登出的令牌"复活"。重启后清单从磁盘恢复，
// 已过期的条目顺带淘汰，避免文件无限增长。
func NewStoreWithRevocations(path, secret, revPath string) (*Store, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取用户文件失败: %w", err)
	}
	var users []User
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, fmt.Errorf("解析用户文件失败: %w", err)
	}
	m := make(map[string]User, len(users))
	for _, u := range users {
		m[u.Username] = u
	}
	if secret == "" {
		secret = DevSecret
	}
	s := &Store{
		users:    m,
		secret:   []byte(secret),
		tokenTTL: TokenTtl,
		revoked:  map[string]time.Time{},
		revPath:  revPath,
	}
	if revPath != "" {
		if err := s.loadRevocations(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// loadRevocations 从磁盘恢复吊销清单并淘汰已过期条目。
func (s *Store) loadRevocations() error {
	raw, err := os.ReadFile(s.revPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 首次运行，无清单
		}
		return fmt.Errorf("读取吊销清单失败: %w", err)
	}
	var stored map[string]time.Time
	if err := json.Unmarshal(raw, &stored); err != nil {
		// 清单损坏不能静默忽略：否则"以为吊销了其实没有"，是安全假象。
		return fmt.Errorf("吊销清单损坏（拒绝启动以免吊销失效）: %w", err)
	}
	now := time.Now()
	for jti, exp := range stored {
		if exp.After(now) {
			s.revoked[jti] = exp
		}
	}
	return nil
}

// flushRevocations 原子落盘（先写临时文件再 rename，避免半写损坏清单）。
// 调用方必须已持有写锁。
func (s *Store) flushRevocations() error {
	if s.revPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.revoked, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.revPath), 0o755); err != nil {
		return err
	}
	tmp := s.revPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.revPath)
}

// Revoke 把令牌加入吊销清单。令牌本身无需有效签名之外的额外信息——jti 取自签名的载荷，
// 因此这里必须先验签再吊销，防止伪造 jti 把别人踢下线。
func (s *Store) Revoke(token string) error {
	sess, err := s.Verify(token)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[sess.TokenID] = sess.ExpiresAt
	if err := s.flushRevocations(); err != nil {
		return fmt.Errorf("吊销清单落盘失败: %w", err)
	}
	return nil
}

// IsRevoked 查询 jti 是否已被吊销。
func (s *Store) IsRevoked(jti string) bool {
	if jti == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	exp, ok := s.revoked[jti]
	if !ok {
		return false
	}
	return exp.After(time.Now())
}

// RevokedCount 当前有效吊销条目数（供运维/自检）。
func (s *Store) RevokedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	n := 0
	for _, exp := range s.revoked {
		if exp.After(now) {
			n++
		}
	}
	return n
}

// pbkdf2Sha256 迭代派生（stdlib 无 pbkdf2，使用 HMAC-SHA256 手工实现 PBKDF2）。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	var out []byte
	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		mac.Write(buf)
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

const pbkdf2Iterations = 120000

// HashPassword 生成口令哈希（供初始化用户使用）：pbkdf2-sha256$iter$salt$hash
func HashPassword(password string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic("随机源不可用: " + err.Error())
	}
	saltHex := hex.EncodeToString(salt)
	dk := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, saltHex, hex.EncodeToString(dk))
}

// verifyPassword 校验口令（常量时间比较）。
func verifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	var iter int
	if _, err := fmt.Sscanf(parts[1], "%d", &iter); err != nil || iter <= 0 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// roleActive 该角色在本版本是否激活（教务处 reserved → 不激活）。
func roleActive(r Role) bool {
	switch r {
	case RoleTeacherRole, RoleAdmin:
		return true
	case RoleAcademicOffices:
		return false // ADR-018：预留但不激活
	default:
		return false
	}
}

// Login 校验账号口令并签发令牌。
func (s *Store) Login(username, password string) (string, *Session, error) {
	u, ok := s.users[username]
	if !ok {
		// 即使用户不存在也执行一次派生，避免时序侧信道泄露账号存在性
		_ = pbkdf2SHA256([]byte(password), []byte("dummy-salt"), pbkdf2Iterations, 32)
		return "", nil, ErrInvalidCredentials
	}
	if !u.Active || !verifyPassword(u.PasswordHash, password) {
		return "", nil, ErrInvalidCredentials
	}
	if !roleActive(u.Role) {
		// 第一关：登录即拒绝未激活角色（教务处 reserved）
		return "", nil, ErrRoleNotActivated
	}

	now := time.Now()
	sess := &Session{
		TokenID:     newTokenID(),
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		IssuedAt:    now,
		ExpiresAt:   now.Add(s.tokenTTL),
	}
	return s.sign(sess), sess, nil
}

// newTokenID 生成 128 位随机 jti（登出吊销的键）。
func newTokenID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("随机源不可用: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// sign 签发令牌：base64url(payload).base64url(hmac)。
func (s *Store) sign(sess *Session) string {
	payload, _ := json.Marshal(sess)
	enc := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify 校验令牌并返回会话（四关中的会话恢复关）。
func (s *Store) Verify(token string) (*Session, error) {
	if token == "" {
		return nil, ErrTokenInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrTokenInvalid
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return nil, ErrTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrTokenInvalid
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, ErrTokenInvalid
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	// 吊销关：登出/改密/角色停用后令牌必须立即失效（P1-7）。
	// 放在验签之后：jti 来自已验签的载荷，伪造者无法借吊销清单实施拒绝服务。
	if s.IsRevoked(sess.TokenID) {
		return nil, ErrTokenRevoked
	}
	// 会话恢复关：角色若已被停用，立即失效（可吊销已登录会话）
	u, ok := s.users[sess.Username]
	if !ok || !u.Active || u.Role != sess.Role || !roleActive(sess.Role) {
		return nil, ErrRoleNotActivated
	}
	return &sess, nil
}

// CanPublish 内容发布权限（发布入口关）。
// 仅 teacher 具备；admin 不参与内容发布（ADR-018）。
func CanPublish(r Role) bool { return r == RoleTeacherRole }

// CanOperateTerminal 终端运维权限（仅 admin）。
func CanOperateTerminal(r Role) bool { return r == RoleAdmin }