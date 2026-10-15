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
	"strings"
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
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        Role      `json:"role"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// TokenTtl 会话有效期（G-4 建议值，需校方签字；此处为技术默认）。
const TokenTtl = 8 * time.Hour

// Store 用户存储与令牌密钥。
type Store struct {
	users    map[string]User
	secret   []byte
	tokenTTL time.Duration
	// revoked 记录已登出的令牌 jti 前缀（演示级吊销，生产用 Redis/DB）
	revoked map[string]bool
}

// NewStore 从 users.json 加载用户。
func NewStore(path, secret string) (*Store, error) {
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
		secret = "dev-only-secret-change-me"
	}
	return &Store{
		users:    m,
		secret:   []byte(secret),
		tokenTTL: TokenTtl,
		revoked:  map[string]bool{},
	}, nil
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
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		IssuedAt:    now,
		ExpiresAt:   now.Add(s.tokenTTL),
	}
	return s.sign(sess), sess, nil
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