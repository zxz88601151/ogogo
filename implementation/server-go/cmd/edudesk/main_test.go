package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edudesk/internal/audit"
	"edudesk/internal/auth"
)

// clientIP 是 P1-8 限流的计数键。它曾直接返回 r.RemoteAddr（含临时端口），
// 于是每个新建连接都被算成新来源、锁定永远不触发 —— 这个测试把该回归钉住。
func TestClientIPStripsPort(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "IPv4 带端口",
			remoteAddr: "10.0.0.5:51234",
			want:       "10.0.0.5",
		},
		{
			name:       "IPv6 带端口",
			remoteAddr: "[fe80::1]:51234",
			want:       "fe80::1",
		},
		{
			name:       "X-Forwarded-For 优先且取首个",
			remoteAddr: "127.0.0.1:1",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.1"},
			want:       "203.0.113.9",
		},
		{
			name:       "X-Real-IP 次选",
			remoteAddr: "127.0.0.1:1",
			headers:    map[string]string{"X-Real-IP": "198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "转发头带空格需裁剪",
			remoteAddr: "127.0.0.1:1",
			headers:    map[string]string{"X-Forwarded-For": " 203.0.113.9 , 10.0.0.1"},
			want:       "203.0.113.9",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: c.remoteAddr, Header: http.Header{}}
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := clientIP(r); got != c.want {
				t.Errorf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
}

// 同一来源的不同连接必须映射到同一个限流键，否则锁定形同虚设。
func TestClientIPSameHostSameKey(t *testing.T) {
	a := &http.Request{RemoteAddr: "10.0.0.5:40001", Header: http.Header{}}
	b := &http.Request{RemoteAddr: "10.0.0.5:59000", Header: http.Header{}}
	if clientIP(a) != clientIP(b) {
		t.Errorf("同源不同端口应得到同一限流键：%q vs %q", clientIP(a), clientIP(b))
	}
}

// ---------------------------------------------------------------------------
// API 授权层：认证（requireSession）与授权（requirePublisher）必须分离。
//
// 修复背景：此前两者被合并在一个 requireAuth 里，CanPublish 被内嵌进通用认证，
// 导致 admin 这类"终端运维"角色（01 §1.1：本地物理访问，非 UI 访问）
// 登录后连只读端点都被 403 —— 不是"缺权限"，而是授权条件放错了层。
// 下面把正确分层钉住，防止以后有人图省事再合并回去。
// ---------------------------------------------------------------------------

func newAuthTestServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()

	type seedUser struct {
		Username     string `json:"username"`
		DisplayName  string `json:"display_name"`
		Role         string `json:"role"`
		PasswordHash string `json:"password_hash"`
		Active       bool   `json:"active"`
	}
	seed := []seedUser{
		{"teacher01", "张老师", "teacher", auth.HashPassword("Teach@2026"), true},
		{"admin01", "运维管理员", "admin", auth.HashPassword("Admin@2026"), true},
		{"academic01", "教务主任", "academic_offices", auth.HashPassword("Acad@2026"), true},
	}
	raw, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed users: %v", err)
	}
	usersPath := filepath.Join(dir, "users.json")
	if err := os.WriteFile(usersPath, raw, 0o600); err != nil {
		t.Fatalf("write users.json: %v", err)
	}

	us, err := auth.NewStore(usersPath, strings.Repeat("k", auth.MinSecretLen))
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	al, err := audit.New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	return &server{users: us, audit: al}
}

// 角色 → 是否通过认证层 / 授权层。
func TestAuthnAuthzSeparated(t *testing.T) {
	s := newAuthTestServer(t)

	tokenFor := func(t *testing.T, username, password string) string {
		t.Helper()
		tok, _, err := s.users.Login(username, password)
		if err != nil {
			t.Fatalf("登录 %s 失败: %v", username, err)
		}
		return tok
	}

	teacherTok := tokenFor(t, "teacher01", "Teach@2026")
	adminTok := tokenFor(t, "admin01", "Admin@2026")

	cases := []struct {
		name              string
		token             string
		wantSessionPass   bool
		wantPublisherPass bool
	}{
		{"teacher 既是已认证也是发布者", teacherTok, true, true},
		{"admin 是已认证但非发布者（终端运维）", adminTok, true, false},
		{"无令牌两边都拒", "", false, false},
		{"令牌被篡改两边都拒", teacherTok + "x", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/admin/api/session", nil)
				if c.token != "" {
					r.Header.Set("Authorization", "Bearer "+c.token)
				}
				r.RemoteAddr = "10.0.0.9:12345"
				return r
			}

			w1 := httptest.NewRecorder()
			_, okSession := s.requireSession(w1, req())
			if okSession != c.wantSessionPass {
				t.Errorf("requireSession = %v (HTTP %d), want %v",
					okSession, w1.Code, c.wantSessionPass)
			}

			w2 := httptest.NewRecorder()
			_, okPublisher := s.requirePublisher(w2, req())
			if okPublisher != c.wantPublisherPass {
				t.Errorf("requirePublisher = %v (HTTP %d), want %v",
					okPublisher, w2.Code, c.wantPublisherPass)
			}

			// 状态码必须区分：401=没登录，403=登录了但没这个权限。
			if c.wantSessionPass {
				if okSession && w1.Code != http.StatusOK {
					t.Errorf("认证通过时不应写状态码，实际 HTTP %d", w1.Code)
				}
				if !okPublisher && w2.Code != http.StatusForbidden {
					t.Errorf("授权不足应 403，实际 HTTP %d", w2.Code)
				}
			} else if w1.Code != http.StatusUnauthorized {
				t.Errorf("未认证应 401，实际 HTTP %d", w1.Code)
			}
		})
	}
}

// admin 访问内容类端点必须 403 且留下 PUBLISH_GATE_DENY 审计 —— 拒绝要有留痕。
func TestAdminDeniedOnContentEndpointsIsAudited(t *testing.T) {
	s := newAuthTestServer(t)
	tok, _, err := s.users.Login("admin01", "Admin@2026")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/admin/api/items", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.RemoteAddr = "10.0.0.9:12345"
	w := httptest.NewRecorder()

	if _, ok := s.requirePublisher(w, r); ok {
		t.Fatal("admin 不应通过内容端点授权")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("应 403，实际 %d", w.Code)
	}

	entries, err := s.audit.Tail(10)
	if err != nil {
		t.Fatalf("读审计失败: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == audit.ActionPublishGate && e.Actor == "admin01" && e.Result == "DENIED" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("admin 被拒时未写入 PUBLISH_GATE_DENY 审计，entries=%+v", entries)
	}
}

// 预留角色（教务处）即便持有构造出来的令牌也不能通过认证层 —— 会话恢复关。
func TestReservedRoleCannotPassSession(t *testing.T) {
	s := newAuthTestServer(t)
	if _, _, err := s.users.Login("academic01", "Acad@2026"); err == nil {
		t.Fatal("预留角色不应能登录（ADR-018）")
	}
}
