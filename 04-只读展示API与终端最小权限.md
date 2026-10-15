# 04 ｜ 只读展示 API 与终端最小权限方案

> 对应本阶段任务 5。
> **原则：终端是"只读的哑终端"，它对服务端只有一个能力 —— 定时拉取已签名的公开数据。**

---

## 1. 信任边界

```
┌────────── 管理 VLAN ──────────┐        ┌──── 展示终端 VLAN ────┐
│  教师管理后台 (admin-web)      │   ✗    │   Windows Display Client │
│  教师数据存储 / PostgreSQL     │◀─隔离─▶│   (标准本地账户)          │
│  内部文件共享 SMB:445          │        │                          │
│  Bundle Generator (内部)       │        │   仅允许出站 → :8443      │
└───────────────────────────────┘        └──────────────────────────┘
              │                                        ▲
              │ 单向：生成 signed bundle                │ 仅 GET /health, /bundle, /config
              ▼                                        │
        ┌──────────────── Display Sync Service (:8443) ─┘
        │  只读 API / HMAC 鉴权 / scope 行过滤 / 白名单投影
        └──────────────── 通过内网专用只读视图访问数据
```

| 边界 | 措施 |
|---|---|
| 终端 → DB | **物理不可达**（不同 VLAN + 防火墙 + DB 不在终端 resolvable） |
| 终端 → SMB/445 | 出站阻断 |
| 终端 → 管理后台 | DNS 不解析 + IP 不可路由 |
| 服务端 → 终端 | 无（终端不开放任何入站端口） |
| 终端 ↔ 终端 | 二层隔离（private VLAN） |

---

## 2. 只读 API 契约（全部 GET，零写操作）

### 2.1 端点清单

| Method | Path | 用途 | 幂等 | 鉴权 |
|---|---|---|---|---|
| GET | `/api/display/v1/health` | 健康探测 + **服务端权威时间** | ✅ | 签名（弱校验，仅 device header） |
| GET | `/api/display/v1/config?profile=&scope=` | 获取 display 配置 | ✅ | HMAC 签名 |
| GET | `/api/display/v1/bundle?scope=&cursor=` | 获取 full/delta bundle | ✅ | HMAC 签名 |
| GET | `/api/display/v1/ping` | 轻量探活（不查库） | ✅ | 无 |

**响应示例 —— `/health`**
```json
{ "status":"ok", "server_time":"2026-10-15T09:35:04+08:00",
  "schema_version":1, "min_client_version":"0.1.0",
  "bundle_generated_at":"2026-10-15T09:35:00+08:00" }
```

**HTTP 状态码约定**

| 码 | 含义 | 终端动作 |
|---|---|---|
| 200 | 有新内容 | 校验 → 应用 |
| 304 | 未变更（etag/cursor 命中） | 保持现状，重算时效 |
| 401 | 签名错误/未知 device | → S9，本地告警，**不重试退避**（可能是凭据问题，立即通知运维） |
| 403 | scope 越权 | → S9 + 强告警（可能是配置错误） |
| 404 | 未知 scope | → S9 |
| 429 | 限流 | 按 `Retry-After` |
| 5xx | 服务端故障 | 指数退避 → S7/S9 |

> **禁止存在的端点**：任何 `POST/PUT/PATCH/DELETE`；任何 `/admin/*`、任何写/回传/上报终端状态的接口（MVP 阶段连"终端上报状态"都不要做——上报通道就是反向渗透通道）。✅ **已决 D-08（2026-10-15）**：不建设独立远程运维平台，但保留本地健康状态 / 最后成功同步时间 / 看门狗重启记录 / 人工巡检机制（见 `05` §8.3 与 `07` R 登记）——本约束与 ADR-012 一致。

> **已验证参考（源码包 MVP v1 · G-1③）**：源码 `hDisplayOnly` 已实现"非 GET → 405 + `Allow: GET` + 免登录"，与本案设计一致，可作实现样板。

### 2.2 请求签名（HMAC）

```
X-EduDesk-Device:     DEV-A2F-07
X-EduDesk-Timestamp:  1760500504            // Unix 秒
X-EduDesk-Nonce:      8f3c1a9b              // 随机，服务端 5 min 内去重
X-EduDesk-Signature:  base64(HMAC-SHA256(secret,
                        "GET\n/api/display/v1/bundle\n<ts>\n<nonce>\nsha256(body)"))
```

| 规则 | 值 |
|---|---|
| 时间窗 | ±120 s（超出 → 401） |
| Nonce | 服务端 Redis/内存去重窗口 300 s，重放 → 401 |
| Secret 长度 | ≥ 32 字节随机 |
| Secret 轮换 | 每 180 天，支持双 secret 并存过渡 |
| Secret 存储（终端） | **Windows DPAPI (CryptProtectData, per-user)** 或 Credential Manager；**禁止明文落盘** |

### 2.3 Bundle Payload 签名

| 项 | 选择 | 说明 |
|---|---|---|
| 算法 | **Ed25519**（推荐） / HMAC-SHA256（MVP 可接受） | 🟡 待决 D-09：MVP 是否直接上 Ed25519 |
| 签名对象 | 规范化后的 `content` 节点 JSON（canonical JSON, key 排序） | 避免空格差异导致误判 |
| 公钥分发 | 编译进二进制 + 首配时由服务端下发交叉验证 | 双通道防单点伪造 |
| 验证失败动作 | **丢弃整个 bundle**，保持 last-good，转 S9 | 绝不"先展示再说" |

> **为什么需要 payload 签名而不只依赖 TLS？** 因为校内 LAN 里任何一个学生笔记本都可能做 ARP 欺骗 / 架设伪 AP。投放假公告到 200 块屏上的后果（谣言大规模传播）远比数据泄漏更难收场。

---

## 3. 终端最小权限（Least Privilege）

### 3.1 网络层

| 控制项 | 配置 |
|---|---|
| 入站 | **全部拒绝**（终端不开任何监听端口） |
| 出站 | 仅允许 `<sync-server-ip>:8443`；其余全部拒绝 |
| DNS | 仅允许解析 sync 域名（或使用 hosts 固定 IP） |
| 代理 | 禁用系统代理自动发现（WPAD） |

### 3.2 Windows 账户层

| 控制项 | 配置 |
|---|---|
| 运行账户 | 专用本地账户 `edudesk-display`，**标准用户（非 Administrators）** |
| 交互限制 | Assigned Access / Shell Launcher（kiosk 模式），或直接全屏无边框自启 |
| Auto-logon | 仅该账户，凭据存 LSA 保护区 |
| 输入设备 | 过滤鼠标/键盘/触摸输入（展示应用不设交互热区；**禁用右键菜单、Alt+F4、Win 键、Ctrl+Alt+Del 组合**：可用 Keyboard Filter） |
| AppLocker | 白名单仅允许 `<install>\EduDeskDisplay.exe` 与系统必要组件 |
| UAC | 保持默认 + 该账户无提权路径 |
| USB 大容量存储 | 禁用（`USBSTOR` start=4） |
| 远程桌面 | 禁用 |

### 3.3 文件系统层

| 路径 | display 账户 ACL |
|---|---|
| `<install>\` (`Program Files\EduDesk`) | 读取 + 执行 |
| `%LOCALAPPDATA%\EduDesk\cache\` | 读写（仅该进程） |
| `%LOCALAPPDATA%\EduDesk\config\` | 读 • 写仅由运维工具带凭据执行 |
| `%LOCALAPPDATA%\EduDesk\secrets\` | DPAPI blob，**管理员亦不可直接读明文** |
| `%LOCALAPPDATA%\EduDesk\logs\` | 仅追加 + 滚动 30 天，上限 50 MB |

### 3.4 应用层

| 控制项 | 说明 |
|---|---|
| 无 UI 输入焦点 | 不创建任何可聚焦控件，无 TextBox/Button |
| 无 URL scheme 处理 | 不注册协议处理器 |
| 无 Shell 执行 | 不调用 `cmd` / `powershell` / `ShellExecute` |
| 无外部资源加载 | 不加载远端图片、不执行远端脚本、不支持 CSS/JS 动态内容 |
| 无遥测 | 不向任何外部端上报数据（含崩溃报告） |

---

## 4. 权限矩阵（RACI 式）

| 能力 | 学生 | 路过者 | 运维(本地) | 教师(后台) | 展示终端进程 |
|---|---|---|---|---|---|
| 观看展示内容 | ✅ | ✅ | ✅ | ✅ | — |
| 与屏幕交互（输入/点击） | ❌ | ❌ | ❌ | ❌ | ❌ 无输入控件 |
| 读本地缓存文件 | ❌ | ❌ | ⚠️ 需管理员权限（且仅公开数据） | ❌ | ✅ |
| 写本地配置 | ❌ | ❌ | ✅ 带凭据工具 | ❌ | ❌ |
| 读 device secret | ❌ | ❌ | ❌ **不可读明文（DPAPI 绑定账户）** | ❌ | ✅ 进程内使用 |
| 写 bundles / 发布内容 | ❌ | ❌ | ❌ | ✅ | ❌ |
| 访问 DB / SMB / admin | ❌ | ❌ | ⚠️（在管理 VLAN，不在终端机） | ✅ | ❌ **网络不可达** |

---

## 5. Go 后端侧最小要求（设计约束，非实现）

| 项 | 要求 |
|---|---|
| Sync 服务进程账户 | 非 root；仅读 DB 只读副本/只读视图 |
| DB 连接 | 使用**只读 role**；Sync 服务无任何写表权限（除本机 nonce 缓存） |
| Bundle 生成 | 独立 package，输入为已投影 struct，**不得直接序列化 DB model** |
| 日志 | 记录 `device_id`、`scope`、`bundle_id`、HTTP 状态；**不记录 bundle 正文** |
| 限流 | per-device ≤ 1 req/5s；per-scope 总额度保护 |
| Http Server | 禁用不必要中间件；禁用目录浏览；关闭 DEBUG/pprof 对外暴露 |

> 🔍 T-S01 ~ T-S08 覆盖本节全部要点。

---

## 6. 本地可观测性（D-08 边界，非远程上报）

> ✅ **已决 D-08（2026-10-15）**：不建设独立远程运维平台（上报通道＝反向渗透通道），但**可观测性不可丢**。
> 每台终端**本地**必须可查、可贴，见 `07` §8.3。要点复录：
> - **本地健康状态屏**：物理组合键调出（运维用），显示当前同步态 `S0~S9`、内存、uptime、`bundle_id`、最后成功同步时间；
> - **看门狗重启记录**：本地环形日志，记录每次崩溃/重启时间 + 原因码；
> - **人工巡检机制**：屏上贴报修码 / 学校报障入口 + 定期巡检表。
>
> 对应测试 **T-S13**（本地健康状态可查，不依赖远程）+ 人工巡检制度文档（T-D03 关联运营门槛④）。
