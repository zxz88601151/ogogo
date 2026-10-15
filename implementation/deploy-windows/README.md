# Windows 终端部署套件（D-06）

> 对应设计文档 `05-Windows终端运行规格.md`。
> 本目录把「终端侧运行形态」从设计落成**可执行脚本**。全部操作**仅本机**，
> 不加载远端 HTML、不嵌入 WebView、无外部依赖（对齐 05 §7 禁用清单）。

---

## 0. 这份套件解决什么

设计 05 §1.2 的硬约束 W-01 ~ W-10，以及 §2.1 的双进程看门狗。当前实现形态是
**浏览器 Kiosk 打开本机 `127.0.0.1:8080`**（05 §1.1 的「⭐ 保底 fallback」档）。

---

## 1. ⚠️ 必须先读：与 05 §7 的偏差（需 Owner 裁决，勿默认通过）

| 项 | 设计 05 的规定 | 当前实现 | 结论 |
| --- | --- | --- | --- |
| 渲染载体 | 原生 `EduDeskDisplay.exe`（Go + 渲染层） | 系统 Edge/Chrome Kiosk 打开 **本机** 页面 | **偏差** |
| §7 禁用「嵌入 Chromium/WebView 加载**远端** HTML」 | 禁用 | 未嵌入 WebView；加载的是 **127.0.0.1 本机**，非远端 | **不撞红线，但≠目标形态** |
| §7 禁用「计划任务执行 powershell 拉**远端内容**」 | 禁用 | 计划任务仅启动**本机**启动器，不拉远端内容 | **不撞红线** |

**诚实结论**：本套件是 05 §1.1 明确列出的「保底 fallback」，可以上屏验证，
但**不等于设计 05 §6 的正式部署物形态**。是否接受该形态进入试点，属 D-06 裁决范围，
应由 Owner 决定；AI 不代为认定。

---

## 2. 脚本清单

| 脚本 | 作用 | 对应设计 |
| --- | --- | --- |
| `01-power-and-ui.ps1` | 关闭息屏/睡眠/休眠/屏保/USB 选择性暂停 | W-06、§5 |
| `02-ntp.ps1` | 配置校内 NTP 校时（`w32tm`） | §3（校时必须在拉数据之前完成） |
| `edudesk-launcher.ps1` | **核心**：拉起服务 + Kiosk 全屏 + 本地看门狗 + 8h 心跳 | W-01~W-05、W-09、§2.1 |
| `03-install-autostart.ps1` | 注册登录自启任务（+ 可选每日 05:00 维护重启） | W-01、W-07、W-10 |
| `04-assigned-access.ps1` | 配置 Windows Assigned Access（Kiosk 锁定） | §1.1、D-06 |
| `99-uninstall.ps1` | 回滚任务与 Kiosk 配置 | — |
| `fallback.html` | 连续崩溃后的 FALLBACK 屏（`本终端故障 · TERMINAL-xx`） | §2.1、§4.2 |

---

## 3. 部署顺序（目标机：Windows 11 专业版 Build 26200）

```powershell
# 全部以「管理员」身份执行
cd <本目录>

# 1) 电源与 UI 抑制
.\01-power-and-ui.ps1

# 2) 校时（把 ntp.school.local 换成学校实际 NTP）
.\02-ntp.ps1 -NtpServer ntp.school.local

# 3) 自启任务（走廊屏示例，并启用每日 05:00 维护重启）
.\03-install-autostart.ps1 -Profile corridor -TerminalId B1-02 -DailyRestart 05:00

# 4) Kiosk 锁定（需先创建 kiosk 本地用户）
.\04-assigned-access.ps1 -KioskUser EduDeskKiosk -Profile corridor -TerminalId B1-02

# 5) 手动验证一次（前台窗口会自动弹出，Ctrl+Alt+Del 可退出）
.\edudesk-launcher.ps1 -Profile corridor -TerminalId B1-02
```

回滚：

```powershell
.\99-uninstall.ps1 -TerminalId B1-02
```

### 关于 W-01「无人登录即进入展示」
计划任务需要**已有用户会话**才能渲染。两种做法（属 D-06 决策）：
- **A（推荐）**：Assigned Access 指定 kiosk 用户 + 为该账户配置自动登录；
- **B**：`AutoAdminLogon`。**注意**：它会把口令写入
  `HKLM\...\Winlogon\DefaultPassword`（明文）。是否接受该风险，须 Owner 明确书面确认。

### 关于 W-08「断电恢复自动开机」
必须在 **BIOS/UEFI** 设 `AC Recovery = Power On`。操作系统层无法脚本化，需现场设置。

---

## 4. 启动器行为（`edudesk-launcher.ps1`）

```
循环（每 15s）:
  1. 探活 /health  → 连续 3 次失败 → 重启服务 + 重启 Kiosk
                     （5 分钟内 ≥3 次重启 → 判定 CRASH STORM → FALLBACK 屏并退出）
  2. Kiosk 进程存在？否则重新拉起（W-05）
  3. 每 8h 写心跳日志（W-09）：uptime / 内存 / bundle_id / 状态 / profile
日志：deploy-windows/logs/launcher-YYYYMMDD.log
```

**为什么必须双进程**（05 §2.1）：公共屏如果自己挂了，自己无法重启 ——
这正是很多自制信息发布系统最终在走廊上长期黑屏的根因。
本启动器提供了**单进程版的看门狗语义**；设计 05 §2.1 的
`EduDeskWatchdogSvc.exe`（Windows 服务形态）**尚未实现**，属后续项
（见下方「未完成」）。

---

## 5. 验收对照（设计 05 §1.2）

| 编号 | 要求 | 本套件覆盖 | 备注 |
| --- | --- | --- | --- |
| W-01 | 开机免登录进全屏 | ⚠️ 部分 | 需 Assigned Access + 自动登录（D-06） |
| W-02 | 全屏无边框/无任务栏/**无鼠标指针** | ✅ | Kiosk 参数 + `styles.css` `cursor:none` |
| W-03 | 冷启动首屏 < 3s | ✅ | 终端 S0 渲染 last-good 缓存，不等网络 |
| W-04 | 首屏永不空白 | ✅ | `fallback.html` + 空屏文案（G-4 §5.7） |
| W-05 | 失焦/被覆盖自动回前台 | ⚠️ 部分 | Kiosk 进程丢失会被拉起；被其他窗口覆盖未处理 |
| W-06 | 禁屏保/睡眠/休眠 | ✅ | `01-power-and-ui.ps1` |
| W-07 | 每日维护重启 | ✅ | `03 -DailyRestart 05:00` |
| W-08 | 断电恢复自动开机 | ❌ | **需 BIOS 设置**，非脚本可及 |
| W-09 | 每 8h 心跳日志 | ✅ | 启动器 heartbeat |
| W-10 | 远程唤醒/运维脚本（仅管理 VLAN） | ❌ | 未实现，属运维通道项 |

---

## 6. 未完成 / 已知缺口（诚实记录）

1. **`EduDeskWatchdogSvc.exe`（设计 05 §2.1 的 Windows 服务）未实现** ——
   当前用 `edudesk-launcher.ps1` 的循环近似，但**若启动器自身被杀，则无人看护**。
   设计要求的"Watchdog 服务自身故障由 Windows 服务恢复策略重启"尚未落地。
2. **`EduDeskProvision.exe` 未实现** —— 设计 05 §6 的注册流程
   （`device_id` + 一次性 token → `/admin/register` → DPAPI 加密落盘）目前在服务端
   **也没有 `/admin/register` 端点**。
3. **屏幕保护类策略**（§5 像素微移 / 反色 / 亮度作息 / 夜间模式）属于渲染层，**未实现**。
4. **终端 HMAC 共享密钥随前端下发** —— 生产必须改为 Ed25519 公钥验签（D-09）。
5. 本套件**未在真实目标机上执行过**（当前环境为开发机）。语法已全部解析通过，
   但 `01/02/04` 会改动系统设置，**请先在测试机跑一遍**。

---

## 7. 快速自检（不上屏也能验）

```bash
# 服务健康
curl -s http://127.0.0.1:8080/health

# 只读边界
curl -s -D- -o /dev/null -X POST http://127.0.0.1:8080/bundle   # 期望 405 + Allow: GET, HEAD

# 终端资源（修过 404 白屏，务必确认三项都 200）
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/app.js
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/core.js
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/styles.css
```
