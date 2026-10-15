# EduDesk 服务端 · Linux 节点部署套件（adminjiedian / 192.168.3.123）

> 与 `../deploy-windows/` 平行：那个管**校园 Windows 展示终端**，这个管**LAN 服务端**。
> 两者共用同一套服务端二进制，只是托管方式不同（Windows 用 SCM+看门狗，Linux 用 systemd）。

---

## 1. 文件清单

| 文件 | 作用 |
|---|---|
| `package.sh` | 在开发机（Windows/Git Bash 或 Linux）交叉编译 `linux/amd64` 并打部署包 |
| `install.sh` | 目标机上的安装脚本（root 执行，幂等） |
| `edudesk.service` | systemd 单元（最小权限加固 + 开机自启 + 崩溃自恢复） |
| `DEPLOY.md` | **现场运维说明**，随包投放至 `/opt/edudesk/DEPLOY.md` |

---

## 2. 部署流程（三步）

### 第 1 步：打包（开发机）

```bash
cd implementation/deploy-linux
./package.sh                  # 自动生成密钥，产出 server-go/dist/edudesk-linux-amd64.tar.gz
# 或指定密钥：
EDUDESK_SECRET="$(openssl rand -hex 32)" ./package.sh
```

脚本会：交叉编译 → 组装暂存区（二进制 + `terminal/` + `admin/` + `data/` 初始文件 + 密钥 env）→ 打 tar.gz → 打印 **tar 与二进制的 SHA-256**。

### 第 2 步：投放

```bash
scp server-go/dist/edudesk-linux-amd64.tar.gz zxzjxx@192.168.3.123:/tmp/
ssh zxzjxx@192.168.3.123
```

### 第 3 步：安装（目标机，需 root）

```bash
cd /tmp && tar -xzf edudesk-linux-amd64.tar.gz && cd stage
sha256sum edudesk                 # 与打包时打印的值比对
sudo ./install.sh                 # 密钥自动取自随包 edudesk.env
rm -rf /tmp/stage /tmp/edudesk-linux-amd64.tar.gz   # 清理含密钥的暂存区
```

---

## 3. 幂等与轮换语义（重要）

| 场景 | 行为 |
|---|---|
| 重复执行 `install.sh`（不带参数） | **不轮换密钥**，沿用目标机既有 `edudesk.env`；覆盖二进制与静态资源 |
| `data/` 下已存在的文件 | **不覆盖**（审计日志、吊销清单、现场内容源保持连续） |
| 想轮换密钥 | `sudo ./install.sh '<新密钥≥32字节>'`；注意终端缓存中的旧 Bundle 会验签失败，等一次轮询即恢复 |
| 想重置内容源 | 手动替换 `/opt/edudesk/data/content-source.json` 后 `systemctl restart edudesk` |

---

## 4. 为什么 Linux 端不用看门狗

`cmd/edudesk-watchdog` 依赖 `golang.org/x/sys/windows/svc`，是 **Windows SCM 专属**实现，不可移植。

Linux 上的等价能力由 systemd 承担，语义一一对应：

| 看门狗（Windows） | systemd（Linux） |
|---|---|
| 服务注册与开机自启 | `systemctl enable edudesk` |
| 5s 内重启崩溃进程 | `Restart=always` + `RestartSec=3` |
| 连续崩溃 ≥3 次停止重启循环 | `StartLimitIntervalSec=300` + `StartLimitBurst=5` |
| 心跳文件（`heartbeat.json`） | 无（systemd 直接监督主进程，不需要应用层心跳） |

因此 `live-smoke.js` 的 **T12（心跳 PID 交叉核对）在远程目标上自动跳过**，不是 FAIL —— 见该脚本内的管辖范围注释。

---

## 5. 已知限制

1. **无 TLS**：内网直连 `:8080` 明文 HTTP。若后续要在跨网段/不可信链路使用，改 `-tls-cert/-tls-key` 走原生 HTTPS（P1-9），或在 nginx 上加 `stream`/`server` 反代并配证书。
2. **未挂 nginx 子路径**：服务端资源使用根相对路径（`/terminal/...`、`/admin/...`），挂到 `location /edudesk/` 会破坏资源解析。已改为在导航页放**指向 :8080 的卡片**。
3. **登录限流是内存态**：重启后失败计数清零（设计如此，见 `internal/ratelimit`）。
4. **`users.json` 为启动时加载一次**：轮换口令后必须 `systemctl restart edudesk` 才生效。

---

## 6. 治理提示

- 本次部署**不改变**设计包 Gate G-0 的准入状态：七条 Exit Criteria 与五道放行门槛仍未通过。
- 部署内容为**演示/样例数据**，不含真实学生数据 —— 符合 Owner「隐私/安全/撤回验收前禁止真实数据上屏」的硬门槛。
- 本节点（192.168.3.123）是**内网边缘节点**，非校园网；在校内网络开端口须另行走设计包 §4 STOP 边界与 Owner 批准。
