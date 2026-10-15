# EduDesk Offline — Campus Display MVP（参考实现 / Reference Implementation）

> 本目录为**可运行参考实现**，用于把设计文档（01–10、G-4 系列）落地为真实代码。
> 设计基线见上级目录文档。本实现默认采用若干未决选型的合理默认值（见文末「设计偏差与默认值」）。

## ⚠️ 版本说明

- **`server-go/` 是当前主实现（Go，对齐原设计）**，含教师后台（G-1）、TP-02 认证、
  RBAC、内容规范校验与完整单元测试 —— **请优先使用它**，详见 `server-go/README.md`。
- **`archive/server-node/`（Node）已封存，禁止部署**，仅作历史对照，说明见该目录 `FROZEN.md`。

终端（`terminal/`）为两者共用，已包含 F-2 撤回记忆跨重启持久化与 F-3 时效边界处理。

## 部署套件（两套，对应两种机器）

| 目录 | 目标 | 托管方式 |
|---|---|---|
| `deploy-windows/` | 校园 **Windows 展示终端**（Kiosk） | SCM + `edudesk-watchdog` + 启动脚本 |
| `deploy-linux/` | 内网 **Linux 服务端节点**（adminjiedian / 192.168.3.123） | systemd（`Restart=always` 替代看门狗） |

**当前实机部署**：服务端已运行于 `192.168.3.123:8080`（`dev=false`，生产密钥），
详见 `deploy-linux/README.md`。校园 Windows 终端侧尚未部署。

## 架构

```
┌─────────────────────────┐         GET /bundle (只读, 60s 轮询)        ┌──────────────────────────┐
│  Windows 终端 (Kiosk)    │  ───────────────────────────────────────▶  │  LAN 服务端 (Go)          │
│  静态 Web 渲染器          │  ◀──────────────── 签名 Bundle ──────────  │  唯一 Bundle 写入方        │
│  S0–S9 状态机 / 验签     │                                            │  privacylint / HMAC 签名   │
└─────────────────────────┘                                            └──────────────────────────┘
```

- **服务端（server-go/）**：仅暴露只读端点；内容源 `data/content-source.json` 由服务端写方工具修改，热重载并重新签名。
- **终端（terminal/）**：纯静态前端，同源由服务端托管（免 CORS，契合 Kiosk 直接开 `localhost:PORT`）。

## 运行

```bash
cd implementation/server-go

# 首次或本地调试：显式降级到演示密钥（P0-1）
go run ./cmd/edudesk --dev

# 试点/生产：必须提供 ≥32 字节密钥，否则拒绝启动（fail-closed）
export EDUDESK_SECRET="<32+ 字节随机串>"
go run ./cmd/edudesk -addr :8080
# 浏览器打开：
#   教室屏  http://localhost:8080/?profile=classroom
#   走廊屏  http://localhost:8080/?profile=corridor
#   教师后台 http://localhost:8080/admin/
```

环境变量与启动参数见 `server-go/README.md`。密钥缺失或过短时服务端**直接拒绝启动**，
必须显式传 `--dev` 才能用演示密钥 —— 这是为了避免"忘记换密钥"在生产静默发生。

## 验证清单（对齐设计验收点）

| 验收点 | 实现位置 | 验证方式 |
|---|---|---|
| 只读 API，非 GET/HEAD → 405 + Allow | cmd/edudesk/main.go `methodNotAllowed` | `curl -X POST /bundle` → 405 + `Allow: GET, HEAD` |
| 字段白/黑名单（privacylint） | internal/privacylint | 内容源含黑名单字段 → 整包拒绝（500，不降级下发） |
| 整体签名 + 终端验签 | internal/canon + terminal/app.js | 服务端自检 PASS；终端 S3→S4 |
| 撤回硬删 + tombstone | internal/store `Withdraw` | `POST /admin/api/items/{id}/withdraw` → 重包无该项且含 tombstone |
| 双 Profile 差异 | internal/model `AllowedTypes` | corridor 仅含 announcement/event/utility/weather |
| STALE 4h 软 / 72h 硬 BLOCKED | terminal/app.js | 断网 72h 后停止显示 |
| OFFLINE 续显 last-good | terminal/app.js | 断网时显示已验证缓存 |
| DEGRADED 拒显未验证 | terminal/app.js | 篡改签名 → 停止显示 |
| 本地 effective_until 过滤 | terminal/app.js | 过期项不渲染 |
| 空屏文案 | terminal/app.js EMPTY_COPY | 无内容/失败/离线分别展示 |
| 密钥 fail-closed（P0-1） | cmd/edudesk `--dev` 门禁 | 无 `EDUDESK_SECRET` 且不传 `--dev` → 拒绝启动 |
| 令牌吊销（P1-7） | internal/auth `Revoke` | 登出后同令牌调 `/admin/api/*` → 401，重启后仍 401 |
| 登录限流（P1-8） | internal/ratelimit | 同 IP 连续 5 次失败 → 429 + `Retry-After` |

## 设计偏差与默认值（Owner 已知并备案）

1. **语言**：已按设计改为 **Go**（`server-go/`）；Node 版封存于 `archive/server-node/`。
2. D-01 走廊竖屏：终端采用响应式网格，竖屏可用（未单独做竖屏适配稿）。
3. D-02 撤回推送：采用 **60s 轮询**（不加 SSE）。
4. D-06 Kiosk：采用 **浏览器 Kiosk + 看门狗脚本** 方案。
5. D-09 签名算法：**HMAC-SHA256**（默认；Ed25519 公钥验签为后期可选项，终端已留注释位）。
6. TP-01 教务对接：MVP **教师手工录入**（content-source.json 即演示录入）。
7. TP-02 教师认证：已实现（`internal/auth` + `/admin/` 登录页，PBKDF2 120k + HMAC 令牌 8h）。
8. G-0-e 内容运营责任人：属治理动作，代码先行、留位待 Owner 指派（`preflight.ps1` 会拦）。

## 不覆盖范围（仍按设计冻结 / 待完成）

- 教师发布后台 UI 剩余交互、教务对接（TP-01）、试点 Runbook 执行（G-5）。
- Ed25519 非对称验签（D-09）、撤回 SSE 推送（D-02）、走廊竖屏专门排版（D-01）。
- 真实数据上屏仍须通过五道放行门槛与 Gate G-0；本实现仅交付可运行架构骨架与核心安全属性。
