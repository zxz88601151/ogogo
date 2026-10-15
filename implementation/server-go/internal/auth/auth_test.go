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

// ---------------- P0-1 密钥 fail-closed ----------------

func TestSecretOKRejectsWeakSecrets(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		wantOK bool
	}{
		{"空密钥", "", false},
		{"演示默认值", DevSecret, false},
		{"31 字节", "0123456789012345678901234567890", false},
		{"32 字节", "01234567890123456789012345678901", true},
		{"64 字节 hex", "3f2a1c9e7b4d6058462939aa7cb1e0d5b8c4a2716e3f90d2c5b7a8e1f4d30926", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := SecretOK(c.secret)
			if c.wantOK && err != nil {
				t.Errorf("应通过，实际 %v", err)
			}
			if !c.wantOK && err == nil {
				t.Error("应被拒绝，实际通过")
			}
		})
	}
}

// ---------------- P1-7 令牌吊销 ----------------

func TestTokenCarriesTokenID(t *testing.T) {
	s := newTestStore(t)
	_, sess, err := s.Login("teacher01", "Teach@2026")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.TokenID) != 32 {
		t.Errorf("jti 应为 32 位 hex，实际 %q（len=%d）", sess.TokenID, len(sess.TokenID))
	}
	_, sess2, _ := s.Login("teacher01", "Teach@2026")
	if sess.TokenID == sess2.TokenID {
		t.Error("两次登录的 jti 不应相同")
	}
}

func TestRevokeInvalidatesToken(t *testing.T) {
	s := newTestStore(t)
	token, _, _ := s.Login("teacher01", "Teach@2026")
	if _, err := s.Verify(token); err != nil {
		t.Fatalf("登出前令牌应有效: %v", err)
	}
	if err := s.Revoke(token); err != nil {
		t.Fatalf("吊销应成功: %v", err)
	}
	if _, err := s.Verify(token); err != ErrTokenRevoked {
		t.Errorf("吊销后应返回 ErrTokenRevoked，实际 %v", err)
	}
	// 吊销只针对该令牌：同一账号新登录的令牌不受影响
	token2, _, _ := s.Login("teacher01", "Teach@2026")
	if _, err := s.Verify(token2); err != nil {
		t.Errorf("新令牌不应被旧令牌的吊销影响: %v", err)
	}
}

func TestRevokeRejectsForgedToken(t *testing.T) {
	s := newTestStore(t)
	// 伪造令牌无法进吊销清单（吊销前先验签），避免被用来把别人踢下线
	if err := s.Revoke("forged.token"); err == nil {
		t.Error("伪造令牌不应被接受进吊销清单")
	}
	if s.RevokedCount() != 0 {
		t.Error("伪造吊销不应写入清单")
	}
}

func TestRevocationsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	users := `[{"username":"teacher01","display_name":"张老师","role":"teacher",
		"password_hash":"` + HashPassword("Teach@2026") + `","active":true}]`
	upath := filepath.Join(dir, "users.json")
	if err := os.WriteFile(upath, []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}
	rpath := filepath.Join(dir, "revocations.json")

	s1, err := NewStoreWithRevocations(upath, "test-secret", rpath)
	if err != nil {
		t.Fatal(err)
	}
	token, _, _ := s1.Login("teacher01", "Teach@2026")
	if err := s1.Revoke(token); err != nil {
		t.Fatal(err)
	}

	// 模拟进程重启：新 Store 从磁盘恢复清单
	s2, err := NewStoreWithRevocations(upath, "test-secret", rpath)
	if err != nil {
		t.Fatal(err)
	}
	if s2.RevokedCount() != 1 {
		t.Fatalf("重启后应恢复 1 条吊销记录，实际 %d", s2.RevokedCount())
	}
	if _, err := s2.Verify(token); err != ErrTokenRevoked {
		t.Errorf("重启后已吊销令牌仍应被拒，实际 %v", err)
	}
}

func TestCorruptedRevocationFileRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	users := `[{"username":"teacher01","display_name":"张老师","role":"teacher",
		"password_hash":"` + HashPassword("Teach@2026") + `","active":true}]`
	upath := filepath.Join(dir, "users.json")
	if err := os.WriteFile(upath, []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}
	rpath := filepath.Join(dir, "revocations.json")
	// 清单损坏必须拒绝启动：否则会形成"看起来已吊销、实际没吊销"的安全假象
	if err := os.WriteFile(rpath, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStoreWithRevocations(upath, "test-secret", rpath); err == nil {
		t.Error("吊销清单损坏时应拒绝启动")
	}
}