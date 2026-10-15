// EduDesk Offline —— Campus Display MVP 服务端（对齐设计 03/04/05/08 + ADR-016/017/018）
//
// 设计定位：
//   - 展示侧**只读**：仅 GET/HEAD /bundle 与 /health，其它方法 405 + Allow 头（设计 04）；
//   - 写方**仅教师后台**：/admin/api/* 需登录态 + 发布权限，服务端唯一写入方（ADR-016）；
//   - 数据防火墙前置：内容写入前先过 privacylint 与内容规范（ADR-017）；
//   - 撤回 = 硬删除 + tombstone（ADR-007），审计留痕与硬删除解耦（G-4 §5.8）。
//
// 运行：go run ./cmd/edudesk -addr :8080 -secret <签名密钥>
// 真实部署务必通过环境变量注入密钥，并启用 TLS 反向代理。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"edudesk/internal/audit"
	"edudesk/internal/auth"
	"edudesk/internal/heartbeat"
	"edudesk/internal/model"
	"edudesk/internal/store"
	"edudesk/internal/validate"
)

var (
	addr        = flag.String("addr", ":8080", "监听地址")
	secret      = flag.String("secret", envOr("EDUDESK_SECRET", "dev-only-secret-change-me"), "Bundle 签名密钥")
	contentPath = flag.String("content", "data/content-source.json", "内容源文件路径")
	usersPath   = flag.String("users", "data/users.json", "用户文件路径")
	auditPath   = flag.String("audit", "data/audit.log", "审计日志路径")
	webRoot     = flag.String("web", ".", "静态资源根目录（含 terminal/ 与 admin/）")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type server struct {
	content *store.Store
	users   *auth.Store
	audit   *audit.Log
	secret  string
	webRoot string
}

func main() {
	flag.Parse()

	// 心跳：每 5s 落盘一次，供 EduDeskWatchdogSvc 判定本进程存活（设计 05 §2.1）。
	// 心跳是**旁路信号**：写入失败不阻塞主服务，只在 stderr 提示。
	// 缺失心跳会让看门狗在 15s 超时后 kill 本进程，因此这里是可用性的关键路径。
	stopHeartbeat := heartbeat.Start(context.Background(), heartbeat.WriteInterval)
	defer stopHeartbeat()

	cs, err := store.Load(*contentPath)
	if err != nil {
		log.Fatalf("加载内容源失败: %v", err)
	}
	us, err := auth.NewStore(*usersPath, *secret)
	if err != nil {
		log.Fatalf("加载用户失败: %v", err)
	}
	al, err := audit.New(*auditPath)
	if err != nil {
		log.Fatalf("初始化审计日志失败: %v", err)
	}

	s := &server{content: cs, users: us, audit: al, secret: *secret, webRoot: *webRoot}
	mux := http.NewServeMux()

	// ---- 展示侧只读 API ----
	// 只读端点：仅 GET/HEAD。
	// 设计 04 要求：写方法必须 405 且带 Allow 头。
	// 注意：静态 catch-all "/" 会在方法不匹配时抢先返回 404，
	// 因此对每个只读端点显式拦截非 GET/HEAD 方法。
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /bundle", s.handleBundle)
	mux.HandleFunc("/bundle", s.methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/health", s.methodNotAllowed("GET"))

	// ---- 教师后台写方 API（需鉴权）----
	mux.HandleFunc("POST /admin/api/login", s.handleLogin)
	mux.HandleFunc("POST /admin/api/logout", s.handleLogout)
	mux.HandleFunc("GET /admin/api/session", s.handleSession)
	mux.HandleFunc("GET /admin/api/items", s.handleListItems)
	mux.HandleFunc("POST /admin/api/items", s.handleCreateItem)
	mux.HandleFunc("PUT /admin/api/items/{id}", s.handleUpdateItem)
	mux.HandleFunc("POST /admin/api/items/{id}/withdraw", s.handleWithdrawItem)
	mux.HandleFunc("GET /admin/api/audit", s.handleAuditTail)
	mux.HandleFunc("GET /admin/api/meta", s.handleMeta)

	// ---- 静态资源：终端 + 后台界面（同源，免 CORS，契合 kiosk 直开）----
	mux.Handle("/", s.staticHandler())

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("EduDesk 服务端已启动: http://localhost%s", *addr)
	log.Printf("  教室屏终端: http://localhost%s/?profile=classroom", *addr)
	log.Printf("  走廊屏终端: http://localhost%s/?profile=corridor", *addr)
	log.Printf("  教师后台:   http://localhost%s/admin/", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("服务异常退出: %v", err)
	}
}

// ---------------- 只读展示侧 ----------------

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
		// pid 供运维与看门狗交叉核对（心跳文件里的 pid 应与此一致）
		"pid": os.Getpid(),
	})
}

func (s *server) handleBundle(w http.ResponseWriter, r *http.Request) {
	profile := r.URL.Query().Get("profile")
	src := s.content.Snapshot()
	res := model.BuildBundle(profile, &src, s.secret)
	if res.Err != "" {
		// 数据防火墙拦截：整包拒绝，绝不降级下发
		s.audit.Append(audit.Entry{
			Actor: "system", Role: "-", Action: audit.ActionPublishGate,
			Result: "DENIED", Reason: res.Err, ClientIP: clientIP(r),
		})
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error":  res.Err,
			"lint":   res.Lint,
		})
		return
	}
	// Bundle 带签名，禁止任何中间层缓存
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res.Bundle)
}

// ---------------- 教师后台写方 ----------------

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "请求格式错误"})
		return
	}
	token, sess, err := s.users.Login(req.Username, req.Password)
	if err != nil {
		s.audit.Append(audit.Entry{
			Actor: req.Username, Role: "-", Action: audit.ActionLoginFailure,
			Result: "DENIED", Reason: err.Error(), ClientIP: clientIP(r),
		})
		// 统一 401，不区分"用户不存在"与"密码错误"
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": "账号或密码不正确"})
		return
	}
	s.audit.Append(audit.Entry{
		Actor: sess.Username, Role: string(sess.Role), Action: audit.ActionLoginSuccess,
		Result: "OK", ClientIP: clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"token": token, "session": sess})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// 令牌为无状态 HMAC，服务端不维护会话表；登出由客户端丢弃令牌完成。
	// 若需强制吊销，后续接入令牌黑名单（Redis/DB）。
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// requireAuth 校验登录态 + 发布权限（API 授权关 + 发布入口关）。
func (s *server) requireAuth(w http.ResponseWriter, r *http.Request) (*auth.Session, bool) {
	token := bearer(r)
	sess, err := s.users.Verify(token)
	if err != nil {
		code := http.StatusUnauthorized
		writeJSON(w, code, map[string]interface{}{"error": err.Error()})
		return nil, false
	}
	if !auth.CanPublish(sess.Role) {
		s.audit.Append(audit.Entry{
			Actor: sess.Username, Role: string(sess.Role), Action: audit.ActionPublishGate,
			Result: "DENIED", Reason: "无发布权限", ClientIP: clientIP(r),
		})
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error": "没有执行该操作的权限",
		})
		return nil, false
	}
	return sess, true
}

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.users.Verify(bearer(r))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"session": sess})
}

// handleMeta 返回后台表单所需的元信息（允许类型、优先级、长度上限等）。
func (s *server) handleMeta(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"classroom_types": model.AllowedTypes("classroom"),
		"corridor_types":  model.AllowedTypes("corridor"),
		"states":          model.LifecycleStates,
		"limits": map[string]int{
			"title":    validate.MaxTitleRunes,
			"summary":  validate.MaxSummaryRunes,
			"location": validate.MaxLocationRunes,
		},
	})
}

func (s *server) handleListItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	src := s.content.Snapshot()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":      src.Items,
		"tombstones": src.Tombstones,
	})
}

func (s *server) handleCreateItem(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	item, err := s.decodeItem(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	if item.ID == "" {
		item.ID = newID(item.Type)
	}
	if errs := validate.Content(item, model.AllowedTypes("classroom")); len(errs) > 0 {
		s.audit.Append(audit.Entry{
			Actor: sess.Username, Role: string(sess.Role), Action: audit.ActionCreate,
			Target: item.ID, Result: "DENIED", Reason: errs[0].Error(), ClientIP: clientIP(r),
		})
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": "内容规范校验未通过", "violations": errs,
		})
		return
	}
	now := time.Now().Format(time.RFC3339)
	if item.State == "" {
		item.State = "PUBLISHED"
	}
	if item.PublishedAt == "" {
		item.PublishedAt = now
	}
	item.UpdatedAt = now

	err = s.content.WithTransaction(func(src *model.Source) error {
		if _, exists := src.Find(item.ID); exists {
			return errExists
		}
		src.Upsert(item)
		return nil
	})
	if err != nil {
		status := http.StatusInternalServerError
		if err == errExists {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]interface{}{"error": err.Error()})
		return
	}
	s.audit.Append(audit.Entry{
		Actor: sess.Username, Role: string(sess.Role), Action: audit.ActionCreate,
		Target: item.ID, Result: "OK", ClientIP: clientIP(r),
	})
	writeJSON(w, http.StatusCreated, map[string]interface{}{"item": item})
}

func (s *server) handleUpdateItem(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	item, err := s.decodeItem(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	item.ID = id
	if errs := validate.Content(item, model.AllowedTypes("classroom")); len(errs) > 0 {
		s.audit.Append(audit.Entry{
			Actor: sess.Username, Role: string(sess.Role), Action: audit.ActionUpdate,
			Target: id, Result: "DENIED", Reason: errs[0].Error(), ClientIP: clientIP(r),
		})
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": "内容规范校验未通过", "violations": errs,
		})
		return
	}
	item.UpdatedAt = time.Now().Format(time.RFC3339)

	err = s.content.WithTransaction(func(src *model.Source) error {
		old, exists := src.Find(id)
		if !exists {
			return errNotFound
		}
		// 保留不可由前端篡改的字段
		item.PublishedAt = old.PublishedAt
		if item.State == "" {
			item.State = "PUBLISHED"
		}
		src.Upsert(item)
		return nil
	})
	if err != nil {
		status := http.StatusInternalServerError
		if err == errNotFound {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]interface{}{"error": err.Error()})
		return
	}
	s.audit.Append(audit.Entry{
		Actor: sess.Username, Role: string(sess.Role), Action: audit.ActionUpdate,
		Target: id, Result: "OK", ClientIP: clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// handleWithdrawItem 撤回：硬删除 + tombstone（ADR-007）。
func (s *server) handleWithdrawItem(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "教师撤回"
	}

	err := s.content.WithTransaction(func(src *model.Source) error {
		if !src.Withdraw(id, reason, sess.Username, time.Now()) {
			return errNotFound
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "内容不存在或已撤回"})
		return
	}
	s.audit.Append(audit.Entry{
		Actor: sess.Username, Role: string(sess.Role), Action: audit.ActionWithdraw,
		Target: id, Result: "OK", Reason: reason, ClientIP: clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "id": id,
		"note": "已硬删除并写入 tombstone；离线终端将在下次同步时清除",
	})
}

func (s *server) handleAuditTail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	n := 100
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 500 {
			n = parsed
		}
	}
	entries, err := s.audit.Tail(n)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "读取审计日志失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

// ---------------- 静态资源 ----------------

func (s *server) staticHandler() http.Handler {
	fs := http.FileServer(http.Dir(s.webRoot))
	// 终端自身的样式/脚本。终端首页挂在 "/" 且 HTML 用相对路径引用资源
	//（styles.css / core.js / app.js），浏览器会解析为根路径，
	// 因此这些文件必须从 terminal/ 目录提供，否则 404 白屏。
	terminalFS := http.FileServer(http.Dir(filepath.Join(s.webRoot, "terminal")))
	terminalAssets := map[string]bool{
		"/styles.css": true,
		"/core.js":    true,
		"/app.js":     true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// "/" 默认落到终端首页
		if p == "/" || p == "/index.html" {
			http.ServeFile(w, r, filepath.Join(s.webRoot, "terminal", "index.html"))
			return
		}
		// /admin/ 默认落到后台登录页
		if p == "/admin" || p == "/admin/" {
			http.ServeFile(w, r, filepath.Join(s.webRoot, "admin", "index.html"))
			return
		}
		if terminalAssets[p] {
			terminalFS.ServeHTTP(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// ---------------- 辅助 ----------------

var (
	errExists   = &appError{"内容 ID 已存在"}
	errNotFound = &appError{"内容不存在"}
)

type appError struct{ msg string }

func (e *appError) Error() string { return e.msg }

func (s *server) decodeItem(w http.ResponseWriter, r *http.Request) (model.ContentItem, error) {
	var item model.ContentItem
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	if err := dec.Decode(&item); err != nil {
		return item, &appError{"请求格式错误"}
	}
	return item, nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.Split(v, ",")[0]
	}
	return r.RemoteAddr
}

func newID(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// methodNotAllowed 返回 405 并携带 Allow 头（设计 04 只读 API 硬约束）。
func (s *server) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			// 放行到对应 GET handler（HEAD 语义由 handler 自行处理）
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{
			"error": "METHOD_NOT_ALLOWED",
			"note":  "展示端点为只读，不接受写入操作",
		})
	}
}