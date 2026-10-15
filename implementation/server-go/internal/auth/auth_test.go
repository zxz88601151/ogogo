package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	users := `[{
		"username":"teacher01","display_name":"张老师","role":"teacher",
		"password_hash":"` + HashPassword("Teach@2026") + `","active":true
	},{
		"username":"admin01","display_name":"运维","role":"admin",
		"password_hash":"` + HashPassword("Admin@2026") + `","active":true
	},{
		"username":"academic01","display_name":"教务","role":"academic_offices",
		"password_hash":"` + HashPassword("Acad@2026") + `","active":true
	}]`
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoginSuccess(t *testing.T) {
	s := newTestStore(t)
	token, sess, err := s.Login("teacher01", "Teach@2026")
	if err != nil {
		t.Fatalf("登录应成功: %v", err)
	}
	if token == "" || sess.Role != RoleTeacherRole {
		t.Error("应返回令牌与 teacher 角色")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Login("teacher01", "wrong"); err != ErrInvalidCredentials {
		t.Errorf("错误密码应被拒，实际 %v", err)
	}
}

func TestLoginUnknownUser(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Login("nobody", "x"); err != ErrInvalidCredentials {
		t.Errorf("不存在账号应被拒，实际 %v", err)
	}
}

// ADR-018 第一关：教务处（reserved）登录即拒绝。
func TestReservedRoleRejectedAtLogin(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Login("academic01", "Acad@2026"); err != ErrRoleNotActivated {
		t.Errorf("预留角色登录应被拒（第一关），实际 %v", err)
	}
}

func TestVerifyValidToken(t *testing.T) {
	s := newTestStore(t)
	token, _, _ := s.Login("teacher01", "Teach@2026")
	sess, err := s.Verify(token)
	if err != nil {
		t.Fatalf("令牌应有效: %v", err)
	}
	if sess.Username != "teacher01" {
		t.Error("会话用户名不符")
	}
}

func TestVerifyTamperedToken(t *testing.T) {
	s := newTestStore(t)
	token, _, _ := s.Login("teacher01", "Teach@2026")
	// 篡改 payload
	if _, err := s.Verify(token[:len(token)-2] + "xx"); err != ErrTokenInvalid {
		t.Errorf("篡改令牌应被拒，实际 %v", err)
	}
	if _, err := s.Verify("garbage"); err != ErrTokenInvalid {
		t.Errorf("非法令牌应被拒，实际 %v", err)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	s := newTestStore(t)
	// 令牌在签发时即固定 ExpiresAt；将 TTL 设为负值后再登录即可得到已过期令牌
	s.tokenTTL = -time.Hour
	token, _, err := s.Login("teacher01", "Teach@2026")
	if err != nil {
		t.Fatalf("登录应成功: %v", err)
	}
	if _, err := s.Verify(token); err != ErrTokenExpired {
		t.Errorf("过期令牌应被拒，实际 %v", err)
	}
}

// RBAC 发布权限：仅 teacher（ADR-018）。
func TestCanPublishOnlyTeacher(t *testing.T) {
	if !CanPublish(RoleTeacherRole) {
		t.Error("teacher 应有发布权限")
	}
	if CanPublish(RoleAdmin) {
		t.Error("admin 不应有内容发布权限（仅运维）")
	}
	if CanPublish(RoleAcademicOffices) {
		t.Error("教务处不应有发布权限")
	}
}