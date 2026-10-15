# EduDeskWatchdogSvc — Windows 看门狗服务

设计依据：`05-Windows终端运行规格.md` §2「看门狗与崩溃恢复」。

本目录把设计里的「双进程架构」从规格落成**真实可安装、可运维的 Windows 服务**。

---

## 1. 设计规格 → 实现映射

| 设计 05 §2.1 场景 | 策略 | 实现位置 | 实测 |
| --- | --- | --- | --- |
| 主进程崩溃 | 5 s 内重启 | `internal/watchdog` `EvCrash` → `start()` | ✅ 实测 1.71–1.77 s |
| 主进程挂起（渲染卡死/无帧率） | 心跳超时 15 s → kill + restart | `internal/watchdog` `EvHang` | ✅ 单测覆盖 |
| 连续崩溃 ≥ 3 次 | 停止重启循环；FALLBACK 屏 + 写本地日志 | `internal/watchdog` `StateFallback` | ✅ 实测 crashes 1→2→3 触发 |
| Watchdog 服务本身故障 | Windows 服务恢复策略（第 1/2 次重启服务，第 3 次运行恢复脚本） | `install` 子命令 `SetRecoveryActions` | ✅ 实测 SCM 自动重启服务 |

心跳写入周期 5 s、超时阈值 15 s，均取自设计原文。

---

## 2. 架构

```
┌──────────────────────────────────────────────┐
│ EduDeskWatchdogSvc   (Session 0, LocalSystem)│
│  - 轮询（默认 2s）读取心跳时间戳               │
│  - 心跳 > 15s 无更新 → kill + restart         │
│  - 5min 内连续 3 次崩溃 → FALLBACK + 留证      │
│  - 内置 Adoption：接管已在健康运行的主进程      │
└──────────────────┬───────────────────────────┘
                   │ spawn / monitor
┌──────────────────▼───────────────────────────┐
│ edudesk.exe  (主进程：只读 Bundle 服务 + 后台) │
│  - 每 5s 写心跳文件（%ProgramData%\EduDesk）   │
└──────────────────────────────────────────────┘
```

### 运行期文件

| 路径 | 用途 |
| --- | --- |
| `%ProgramData%\EduDesk\watchdog.json` | 服务配置（主进程路径/参数/终端编号/轮询间隔） |
| `%ProgramData%\EduDesk\heartbeat.json` | 主进程心跳（pid + seq + 时间戳） |
| `%ProgramData%\EduDesk\terminal-state.json` | 终端状态快照（RUNNING / FALLBACK / DOWN） |
| `%ProgramData%\EduDesk\logs\watchdog.log` | 服务日志（按 5 MB 滚动，保留 4 份） |
| `%ProgramData%\EduDesk\logs\display-stdout.log` | 主进程 stdout/stderr |
| `%ProgramData%\EduDesk\logs\watchdog-recovery.log` | SCM 第 3 次失败动作的恢复留证 |

---

## 3. 安装与运维

### 安装（需管理员）

```powershell
# 1) 编译
cd implementation/server-go
go build -o edudesk.exe ./cmd/edudesk
go build -o edudesk-watchdog.exe ./cmd/edudesk-watchdog

# 2) 安装并启动服务（-recovery 指向第 3 次失败的恢复脚本）
.\edudesk-watchdog.exe install `
  -exe  "<abs>\implementation\server-go\edudesk.exe" `
  -args "-addr :8080" `
  -code TERMINAL-01 `
  -recovery "<abs>\implementation\deploy-windows\watchdog-recovery.cmd"
```

`install` 会依次完成：

1. 写 `watchdog.json`；
2. 对 `%ProgramData%\EduDesk` 授予 `BUILTIN\Users` Modify 权限
   （**必要**：主进程运行在用户会话，需写入心跳；服务以 LocalSystem 只读该文件。
   若缺此权限，主进程写心跳失败 → 被误判为挂起 → 陷入重启循环）；
3. 注册服务（自动启动 + 延迟启动）；
4. 设置恢复策略：`restart@5s → restart@15s → run-command@30s`（reset period 24h）；
5. 启动服务。

### 日常命令

```powershell
.\edudesk-watchdog.exe status     # 服务状态 + 恢复策略 + 心跳 + 终端状态
.\edudesk-watchdog.exe start      # 启动服务
.\edudesk-watchdog.exe stop       # 停止服务（不终止主进程，见 §5）
.\edudesk-watchdog.exe debug      # 前台运行监督循环（开发验证，不注册服务）
.\edudesk-watchdog.exe uninstall  # 停止并删除服务
```

> **为什么不直接用 `sc.exe`**：部分受管环境（含本项目开发机）把 `sc.exe`
> 列入程序黑名单，运维无法调用。因此提供内建 `start` / `stop` 子命令，
> 便于写进 Runbook 且不受该限制影响。

---

## 4. 实测证据（2026-10-15，本机 Windows）

| # | 场景 | 证据 | 结果 |
| --- | --- | --- | --- |
| 1 | 单次崩溃恢复（debug 模式） | `13:47:04.838` kill → `13:47:06.612` STARTED | 恢复 **1.77 s** |
| 2 | 单次崩溃恢复（服务模式） | `13:48:33.259` kill → `13:48:34.970` STARTED | 恢复 **1.71 s** |
| 3 | 崩溃风暴 → FALLBACK | `crashes=1→2→3`，日志 `FALLBACK ... restart loop halted`，主进程不再被拉起 | ✅ |
| 4 | 服务自身故障 → SCM 恢复 | `13:50:03.604` 杀服务进程 → `13:50:09.077` 服务自动重启 | ✅ |
| 5 | 服务重启 → 接管已有进程 | 日志 `ADOPTED pid=4720`，未重复拉起、无端口冲突 | ✅ |

`go test ./internal/watchdog/`：**12 条全通过**（含接管、宽限、崩溃风暴、窗口裁剪、冷却重试）。

---

## 5. 与设计的偏差（**待 Owner 裁决 / 请勿默认视为已批准**）

| # | 项 | 设计原文 | 实现 | 理由 |
| --- | --- | --- | --- | --- |
| D-A | 心跳通道 | 「共享内存时间戳 / 命名管道」二选一 | `%ProgramData%\EduDesk\heartbeat.json` 时间戳文件（原子写） | 服务在 **Session 0**、主进程在用户会话；`Local\` 命名对象按会话隔离服务读不到，`Global\` 需额外特权且普通用户进程创建会失败。文件通道跨会话可读、无需特权、可被运维直接观测。仍属设计所说的「时间戳」机制。 |
| D-B | 启动宽限 30 s | 未规定 | 新进程拉起后 30 s 内不因心跳缺失判挂起 | 不加会导致每轮冷启动都被误判为「挂起」（首个心跳未落盘）。 |
| D-C | FALLBACK 冷却重试 30 min | 「停止重启循环」 | 进入 FALLBACK 后 30 min 允许**一次**试探恢复 | 永久停止会复现设计明确反对的「走廊长期黑屏」。 |
| D-D | 停止服务语义 | 未规定 | 停止服务**不**终止主进程，仅停止监控 | 公共屏「自己黑掉」比「无人看护」更糟；停机维护由运维显式操作。 |
| D-E | Adoption（接管） | 未覆盖 | 服务启动时若心跳指向存活且新鲜的主进程 → 接管，不重复拉起 | 缺失会导致服务每次重启都重复拉起主进程 → 端口冲突 → 崩溃风暴 → 把健康终端打黑。 |

---

## 6. 未实现 / 边界（诚实清单）

1. **`EduDeskDisplay.exe` 原生展示进程尚未实现。** 设计 05 §6 的部署物清单里
   主程序是「Go + 渲染层」的原生进程；当前参考实现的展示层是**系统浏览器 Kiosk
   打开本机 `127.0.0.1:8080`**，属设计 05 §1.1 的「保底 fallback」档，
   不撞 §7 依赖禁用清单（未嵌入 Chromium/WebView、未加载远端 HTML），
   但**不等于设计目标形态**。看门狗当前守护的是后端 + 托管终端页的 `edudesk.exe`。
2. **Kiosk 浏览器不由本服务拉起。** Windows 服务位于 Session 0，无法在用户桌面
   创建 GUI 进程。浏览器侧由用户会话的计划任务（`deploy-windows/03-install-autostart.ps1`）
   负责自启与重启。
3. **W-01（免登录）/ W-05（回前台）** 仅部分覆盖；**W-08（断电自启）** 依赖 BIOS
   来电自启，非脚本可及。
4. **日志 30 天保留**：当前实现为「按 5 MB 滚动保留 4 份」，30 天清理需运维侧
   计划任务（设计 05 §5 的磁盘上限要求）。
5. 本机测试环境把 `sc.exe` 列入程序黑名单，故第 3 次失败动作中的 `sc` 调用在
   **开发机**无法验证（日志显示「拒绝访问」）。真实 SCM 以 LocalSystem 运行
   恢复命令，不受该沙箱限制。

---

## 7. 状态

- **IMPLEMENTATION: 可安装、可运行、核心行为已实测**（详见 §4）。
- 真实数据上屏仍须通过 Gate G-0 七条 Exit Criteria 与五道放行门槛。
- §5 的偏差项（尤其 D-A、D-C、D-E）需 Owner 明确裁决后方可视为定稿。
