# EduDesk Offline — 实现说明（Go 版 · 对齐原设计）

> 状态：**参考实现 / DESIGN ALIGNED**，尚未获得生产部署授权。
> 真实数据上屏仍须通过 Gate G-0（7 条 Exit Criteria）与五道放行门槛。

本目录是 EduDesk Campus Display MVP 的 **Go 实现**，替代此前的 Node 参考实现
（`../server/` 保留作历史对照，已不再维护）。

## 1. 目录结构

```
server-go/
├── cmd/
│   ├── edudesk/        服务端主程序（只读 API + 教师后台 API + 静态托管）
│   └── genuser/        口令哈希生成工具
├── internal/
│   ├── canon/          规范化 JSON + HMAC-SHA256 签名（与浏览器逐字节一致）
│   ├── privacylint/    数据防火墙（白名单 + 黑名单）
│   ├── model/          内容模型、Bundle 构建、撤回生命周期
│   ├── store/          内容源持久化（原子落盘 + 事务回滚）
│   ├── auth/           TP-02 认证 + RBAC（ADR-018）
│   ├── validate/       内容规范校验（G-4 §5.5）
│   └── audit/          操作审计（append-only）
├── admin/              G-1 教师后台界面（登录 / 内容管理 / 审计）
├── terminal/           展示终端（与 ../terminal 同步，见 §5）
└── data/
    ├── content-source.json   内容源（服务端唯一写入方落盘）
    ├── users.json            用户与口令哈希
    └── audit.log             审计日志（运行后生成）
```

## 2. 运行

```bash
cd implementation/server-go

# 开发运行：密钥 fail-closed（P0-1）要求显式声明降级
go run ./cmd/edudesk --dev

# 试点 / 生产：密钥必须 ≥32 字节，否则服务端拒绝启动
export EDUDESK_SECRET="<32字节以上随机串>"
go build -o edudesk ./cmd/edudesk
./edudesk -addr :8080
```

**P0-1 密钥 fail-closed**：未设 `EDUDESK_SECRET`（或长度 <32）且未传 `--dev` 时，
服务端**直接拒绝启动**并打印原因。理由：终端验签密钥随前端下发，演示密钥一旦被带到
生产，等价于把 Bundle 签名私钥公开。宁可启动失败，也不要"看起来正常但可被伪造"。

启动参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `-addr` | `:8080` | 监听地址 |
| `-secret` | `$EDUDESK_SECRET` | Bundle 签名密钥（≥32 字节） |
| `--dev` | 关 | 允许演示密钥，仅本机调试 |
| `-revocations` | `data/revocations.json` | 令牌吊销清单（P1-7） |
| `-tls-cert` / `-tls-key` | 空 | 同时提供则启用原生 HTTPS（P1-9） |
| `-login-max-fails` | 5 | 登录失败锁定阈值（P1-8） |
| `-login-lock-for` | 15m | 触发阈值后的锁定时长（P1-8） |
| `-web` | `.` | 静态资源根（含 `terminal/`、`admin/`） |
| `-content` / `-users` / `-audit` | `data/…` | 内容源 / 用户 / 审计日志 |

TLS 二选一：原生 `-tls-cert/-tls-key`，或反向代理（样例见
`deploy-windows/Caddyfile.sample`）。内网也不能裸奔 —— 教师登录口令走
`POST /admin/api/login`，明文传输等于公开。

| 入口 | 地址 | 说明 |
|------|------|------|
| 教室屏终端 | `http://<host>:8080/?profile=classroom` | 作业 / 课表 / 公告 / 天气 |
| 走廊屏终端 | `http://<host>:8080/?profile=corridor` | 公告 / 活动 / 服务 / 天气 |
| 教师后台 | `http://<host>:8080/admin/` | 需登录 |

账号口令**由部署时签发，仓库内不保存明文**（`data/users.json` 只存
PBKDF2-HMAC-SHA256(120k) 加盐哈希，不可反推）。

| 账号 | 角色 | 说明 |
|------|------|------|
| `teacher01` | teacher | 本版本最高活跃权限（内容发布） |
| `admin01` | admin | 仅终端运维，**无内容发布权** |

签发 / 轮换口令：

```bash
go run ./cmd/genuser <username> <password> <displayName> <role>
```

> - 新口令请交由现场管理员保管，**不要提交进仓库**；轮换后须重启服务
>   （`users.json` 仅在启动时加载一次）。
> - 预留角色 `academic_offices`（教务）**不写入 `users.json`** ——
>   ADR-018 规定其在本版本不激活，登录一律 401。

### 角色能力矩阵（授权层实证）

闸门在服务端，前端隐藏只是 UX。两个函数分工明确、**不得合并**：

- `requireSession` —— 只做认证（令牌有效 / 账号启用 / 角色激活）；
- `requirePublisher` —— 认证之上叠加内容发布授权（`auth.CanPublish`，**仅 teacher**）。

| 端点 | 所需层 | teacher | admin | academic_offices |
|------|--------|---------|-------|------------------|
| `POST /admin/api/login` | 无 | ✅ | ✅ | ❌ 401（预留不激活） |
| `GET /admin/api/session` | `requireSession` | ✅ 200 | ✅ 200 | ❌ 401 |
| `GET /admin/api/items` | `requirePublisher` | ✅ 200 | ❌ 403 | ❌ 401 |
| `POST /admin/api/items` | `requirePublisher` | ✅ 201 | ❌ 403 | ❌ 401 |
| `PUT /admin/api/items/{id}` | `requirePublisher` | ✅ 200 | ❌ 403 | ❌ 401 |
| `POST /admin/api/items/{id}/withdraw` | `requirePublisher` | ✅ 200 | ❌ 403 | ❌ 401 |
| `GET /admin/api/audit` | `requirePublisher` | ✅ 200 | ❌ 403 | ❌ 401 |
| `GET /admin/api/meta` | `requirePublisher` | ✅ 200 | ❌ 403 | ❌ 401 |
| `GET /bundle`、`GET /health` | 无（只读开放） | ✅ | ✅ | ✅ |

> **为什么 admin 一个内容端点都进不去**：这不是权限缺失，而是设计选择。
> ADR-018 规定 MVP 最高活跃权限为教师，不设凌驾其上的内容管理员；
> `01 §1.1` 把终端运维定义为「装机、配 device_id、看状态屏 —— **本地物理访问，非 UI 访问**」。
> 因此 admin 登录后的正确语义是"已认证、无 web 操作能力"，后台会直接展示说明页
> 而不是让用户面对一片 403 空白。
>
> 若将来要给 admin 增加 web 能力（例如只读的终端运维视图），**必须先改设计**
> （`01 §1.1` / ADR-018），再动 `requirePublisher` 的绑定，不要只改代码。

生成新用户口令哈希：

```bash
go run ./cmd/genuser <username> <password> <displayName> <role>
```

### 2.1 服务化运行与看门狗（设计 05 §2.1）

主进程每 5 s 写一次心跳（`%ProgramData%\EduDesk\heartbeat.json`），由
`EduDeskWatchdogSvc` 以 **Windows 服务**形态守护：

- 心跳超时 **15 s**（进程仍在但卡死）→ kill + restart；
- 进程消失 → 立即重启（实测恢复 **1.71–1.77 s**，设计目标 5 s）；
- 5 min 内连续 **3 次**崩溃 → 停止重启循环 + 进入 FALLBACK
  （写 `terminal-state.json` 与本地日志，防重启风暴）；
- 服务自身故障 → 由 Windows 服务恢复策略接管（重启 ×2，第 3 次运行恢复脚本）。

```powershell
go build -o edudesk-watchdog.exe ./cmd/edudesk-watchdog
.\edudesk-watchdog.exe install -exe "<abs>\edudesk.exe" -args "-addr :8080" -code TERMINAL-01
.\edudesk-watchdog.exe status      # 服务状态 + 恢复策略 + 心跳 + 终端状态
```

完整说明（设计映射、实测证据、与设计的偏差项）见
[`cmd/edudesk-watchdog/README.md`](cmd/edudesk-watchdog/README.md)。

## 3. 只读边界（设计 04）

展示侧端点**仅接受 GET/HEAD**，任何写方法返回 `405` 且带 `Allow: GET, HEAD`：

```bash
curl -i -X POST http://127.0.0.1:8080/bundle
# HTTP/1.1 405 Method Not Allowed
# Allow: GET, HEAD
```

写入只发生在 `/admin/api/*`，且必须同时满足：有效登录态 + `teacher` 角色。
**服务端是唯一写入方**（ADR-016），终端永不写。

## 4. 教师后台写方（G-1）

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| POST | `/admin/api/login` | 匿名 | TP-02 账号密码登录 |
| GET | `/admin/api/session` | 登录态 | 会话校验 |
| GET | `/admin/api/items` | teacher | 内容列表 + 撤回记录 |
| GET | `/admin/api/meta` | teacher | 表单元信息（类型白名单、长度上限） |
| POST | `/admin/api/items` | teacher | 新建内容 |
| PUT | `/admin/api/items/{id}` | teacher | 编辑内容 |
| POST | `/admin/api/items/{id}/withdraw` | teacher | **撤回 = 硬删除 + tombstone** |
| GET | `/admin/api/audit?n=50` | teacher | 操作审计 |

### 双重校验（写入前）

1. **内容规范**（`validate`）：标题 ≤40 字、摘要 ≤120 字、优先级 1-3、
   时间格式与顺序、ID 字符集；**禁止内容正则**拦截成绩 / 考勤 / 考试 / 家长联系方式 /
   身份证 / 手机号 / 学号 / 教师备注（ADR-010 红线 + D-07 冻结）。
2. **数据防火墙**（`privacylint`）：字段白名单 + 黑名单，未知字段与 PII 一律整包拒绝。

### RBAC（ADR-018）

`academic_offices` 为预留角色，本版本**不激活**，四关全部拒绝：

| 关卡 | 位置 | 行为 |
|------|------|------|
| 登录 | `auth.Login` | 返回"账号或密码不正确"（不泄露角色存在性） |
| 会话恢复 | `auth.Verify` | 每次校验重新确认角色仍激活 |
| API 授权 | `requireAuth` | 403 + 审计 `PUBLISH_GATE_DENY` |
| 发布入口 | `auth.CanPublish` | 仅 `teacher` 返回 true |

`admin` 角色**不能发布内容**（仅运维），这是刻意区分，避免"运维=内容权限"。

## 5. 终端（F-2 / F-3 补强）

`terminal/` 为展示终端，与 `../terminal` 保持同步（F-2/F-3 改动后已复制）。
若修改终端，请以 `implementation/terminal/` 为准并同步至此。

**F-2 撤回记忆跨重启持久化**：

- 验签通过的 Bundle 落盘 `edudesk.lastgood.v1`（含 `verifiedAt`）；
- tombstone ID 集合落盘 `edudesk.withdrawn.v1`，**只增不减** ——
  撤回是硬删除，语义不可逆，终端不得因后续 Bundle 裁剪 tombstone 而"忘记"撤回；
- 重启后先按持久化撤回集合过滤，再考虑续显；
- 陈旧度以 `verifiedAt` 为基准跨重启延续，**重启不会刷新 72h 计时**。

**F-3 时效过滤与网络状态解耦**：`selectVisibleItems` 不接受任何网络状态入参，
`S4_VALID` / `S5_STALE_SOFT` / `S7_OFFLINE` 共用同一实现，
保证 `effective_until` 在离线续显时同样被本地执行。

## 6. 测试

```bash
# Go 单元测试
go test ./...

# 终端核心逻辑单元测试（F-2 / F-3，38 条）
cd ../ && node tests/core.test.js

# 运行态冒烟测试（需服务已在运行；只发 GET/HEAD 与"应被拒绝"的写请求，无副作用）
cd .. && node tests/live-smoke.js
# 指定目标与密钥：
#   EDUDESK_BASE=http://127.0.0.1:8080 EDUDESK_SECRET=<secret> node tests/live-smoke.js
```

`live-smoke.js` 与单元测试的分工：单元测试验证**纯逻辑**，
冒烟测试验证**真实进程暴露出来的契约**——跨语言验签、只读边界、
数据防火墙的管辖范围、心跳与 `/health` 的 PID 一致性。
退出码非 0 即表示运行实例存在契约偏差，禁止放行。

覆盖要点：

- `canon`：与 JS 的规范化字节一致性黄金向量、验签往返、篡改检测；
- `privacylint`：8 类 PII 黑名单、白名单严格性、嵌套 audience；
- `model`：双 Profile 差异、撤回硬删 + tombstone、WITHDRAWN 不下发、六态标注；
- `auth`：登录成败、令牌篡改/过期、预留角色登录拒绝、发布权限矩阵、
  密钥强度 fail-closed、jti 唯一性、吊销即时生效、伪造令牌不可进吊销清单、
  吊销清单跨重启恢复、清单损坏拒绝启动；
- `ratelimit`（7 条）：阈值前放行、第 N 次锁定、锁定期满恢复、
  成功登录清零、计数窗口滑出、多来源互不影响；
- `validate`：8 类禁止内容、长度、类型白名单、时间顺序、ID 字符集；
- `store`：快照深拷贝、事务落盘、失败回滚、撤回留痕；
- `watchdog`（12 条）：崩溃重启、心跳超时 kill+重启、启动宽限、
  5 min 内 3 次崩溃 → FALLBACK、窗口外崩溃不计入、冷却重试、
  **接管已在运行的健康主进程**（服务重启场景）；
- `core.test.js`：300s 宽限边界、72h 硬 BLOCKED 边界、三态时效一致性、
  撤回记忆不可逆、跨重启不再展示已撤回项。

## 7. 已知偏差与待决

| 项 | 现状 | 说明 |
|----|------|------|
| D-01 走廊屏竖装 | 响应式兼容 | 布局未针对竖屏优化 |
| D-02 撤回推送 | 60s 轮询 | 未使用 SSE |
| D-06 Kiosk | 脚本就绪，**未真机验证** | 需 Windows Assigned Access 配置 |
| D-09 签名算法 | HMAC-SHA256 | 终端与服务端共享密钥；生产应改 Ed25519 非对称 |
| TP-02 认证 | 本地账号 + HMAC 令牌 | 未对接学校统一身份 |
| TP-01 教务对接 | 手工录入 | 未对接教务库 |
| G-0-e 责任人 | **未指定** | 治理动作，须 Owner 正式指派 |

**已知缺口**：

- 终端 HMAC 验签使用共享密钥，密钥随前端下发 ——
  符合 MVP 演示，**生产必须改为 Ed25519 公钥验签**（P2-12）。
- 登录限流是**单进程内存**计数：服务多实例部署时需换成共享存储，
  否则每个实例各自计数。单机 MVP 拓扑下不构成问题。
- 未实现 `HEAD /bundle` 的独立处理（当前由 GET handler 覆盖，语义正确但未单测）。

### 7.1 已关闭缺口（补丁轮）

| 原缺口 | 补丁 | 落地位置 |
|---|---|---|
| 密钥默认演示值，生产忘记换即裸奔 | P0-1 fail-closed + `--dev` 显式逃生门 | `cmd/edudesk` `auth.SecretOK` |
| 登出只靠客户端丢弃令牌 | P1-7 jti + 文件吊销清单（跨重启） | `internal/auth` `Revoke/IsRevoked` |
| 无登录速率限制 | P1-8 per-IP 5 次失败锁 15 分钟 | `internal/ratelimit` + `handleLogin` |
| README 要求 TLS 但无实现 | P1-9 原生 `-tls-cert/-tls-key` + Caddy 样例 | `cmd/edudesk` `deploy-windows/Caddyfile.sample` |
| 终端参数手填易错 | P1-11 配置模板 + 校验脚本 | `terminal-config.template.json` + `06-validate-terminal-config.ps1` |
| 部署缺统一上线门禁 | P0-2 preflight 门禁（9 项 PASS/FAIL） | `deploy-windows/preflight.ps1` |
| Node 旧实现可被误部署 | P0-6 封存 | `implementation/archive/server-node/` |

## 8. 安全红线（不可突破）

依据 ADR-010 与设计 02，系统在任何模式下都**不得**下发：

学生账户 / 成绩 / 分数 / 排名 / 考勤 / 名单 / 学号 / 联系方式 /
家长信息 / 教师备注 / 考试安排（D-07 已冻结）。

该约束在**三层**同时强制：内容规范正则 → privacylint 黑名单 → Bundle 构建校验，
任一层命中即整包拒绝，不降级、不部分下发。

## 9. 变更记录（Windows 实机验证轮）

### 9.1 Bundle 补齐设计 03 §4 字段

| 字段 | 值 | 说明 |
| --- | --- | --- |
| `bundle_id` | 26 字符 ULID | 48bit 毫秒时间戳 + 80bit 随机（Crockford Base32） |
| `ttl_sec` | `900` | 内容新鲜度 TTL；终端据此在 15min 后进入 S6 STALE |
| `content_hash` | `sha256:<hex>` | 覆盖 `items` + `tombstones`；终端用同一载荷重算比对 |

终端侧（`terminal/app.js`）在 **S3 VERIFY** 同时校验 `signature` 与 `content_hash`，
任一失败 → **S9 DEGRADED**，绝不渲染未验证内容。

### 9.2 修复：跨语言签名不一致（**严重**）

**现象**：终端用 `JSON.parse` 后重算的签名与 `content_hash` **均不匹配**，
即任何携带 `audience` 的 Bundle 都会让终端永久进入 DEGRADED。

**根因**：`ContentItem.ToMap()` 把 `audience` 直接写为 `*Audience` **结构体指针**，
`Tombstone` 同样以结构体入包；而 `canon.Value()` 对无法识别的类型走 `fmt.Sprint` 兜底，
于是服务端签名是按 `"&{高一 3}"` 这类字符串算的，与终端按 JSON 对象重算的结果必然不同。

**修复**（三处，纵深防御）：
1. `ToMap()` 输出嵌套 `map[string]interface{}`；
2. `BuildBundle()` 的 tombstone 统一转为 map；
3. `canon.Value()` 的 `default` 分支改为 **JSON 往返**（`json.Marshal` → `Unmarshal` → 再规范化），
   从根本上杜绝"结构体被退化成字符串"这一整类问题。

**回归保护**：`internal/model/regress_sig_test.go`
（含 audience 与 tombstone 的 Bundle，签名与摘要必须可独立重算复现，
且载荷中不得出现 `&{`）。

> 该缺陷此前未被发现，是因为早先只在 Node 参考实现里真正验证过签名；
> Go 版端到端只检查了「签名字段存在」。**教训：跨语言协议必须做"另一侧真正验签"的验证，
> 不能只验证字段存在。**

### 9.3 终端修复：静态资源 404 导致白屏

终端首页挂在 `/`，但 HTML 用相对路径引用 `styles.css` / `core.js` / `app.js`，
浏览器会请求 `/styles.css`，而静态根指向 `webRoot`（文件实际在 `webRoot/terminal/`）。
已在 `staticHandler()` 增加 `terminalFS` + 资源白名单。**修复前教室屏/走廊屏均为白屏。**

### 9.4 终端状态机对齐 03 §5

状态名与语义由早期的
`BOOT/IDLE/FETCHING/VERIFYING/VALID/STALE_SOFT/STALE_HARD/OFFLINE/DEGRADED/EMPTY`
**校正为设计原文的**
`S0 BOOT / S1 NET_PROBE / S2 FETCH / S3 VERIFY / S4 APPLY / S5 LIVE / S6 STALE / S7 OFFLINE_SERVING / S8 BLOCKED / S9 DEGRADED`，
并补齐：

- **S1 探活 `/health`（3s 超时）** + **时钟漂移检测**（>60s 徽标；>15min 转 S9，对齐 05 §3）；
- 陈旧度四档：`FRESH(<15min) / STALE(<4h) / STALE_SOFT(<72h) / BLOCKED(≥72h)`；
- STALE 内容去饱和（`saturate .55`，2s 过渡）；
- 空态折叠不再占用 S9（空内容属 S5 LIVE 的展示分支）。

### 9.5 新增部署套件

`implementation/deploy-windows/` —— 见该目录 `README.md`（D-06 落地脚本、
与设计 05 §7 的偏差说明、W-01~W-10 验收对照、已知缺口）。
