# EduDesk Offline — Linux 节点部署说明（adminjiedian / 192.168.3.123）

> 本文件随部署包投放至 `/opt/edudesk/DEPLOY.md`，是这台机器上 EduDesk 服务端的现场唯一操作依据。
> 部署日期：2026-10-15 ｜ 部署者：中哥（Owner）｜ 执行方式：`deploy-linux/install.sh`

---

## 1. 这套东西是什么

EduDesk Offline 校园展示屏 MVP 的**服务端**（Go）。它在设计里的角色是「校内 LAN 的权威服务端」：

- 只向展示终端下发**签名的只读 Bundle**（`GET /bundle`，终端 60s 轮询）；
- 向教师提供后台（教师录入 / 撤回 / 审计）；
- 同源托管展示终端页面与后台页面。

**展示终端本身的目标形态是 Windows Kiosk**（见 `05-Windows终端运行规格.md`）。
本机（Linux）承担的是**服务端**这一半，不承担 Windows 终端那一半。

---

## 2. 部署形态一览

| 项 | 值 |
|---|---|
| 部署路径 | `/opt/edudesk/` |
| 服务账号 | `edudesk`（系统账号，`/usr/sbin/nologin`，无家目录） |
| 进程单元 | `edudesk.service`（systemd，`enabled` 开机自启） |
| 监听 | `0.0.0.0:8080` |
| 展示终端 | `http://192.168.3.123:8080/?profile=classroom`（走廊屏 `?profile=corridor`） |
| 教师后台 | `http://192.168.3.123:8080/admin/` |
| 健康检查 | `http://192.168.3.123:8080/health` |
| 数据目录 | `/opt/edudesk/data/`（0700，属 `edudesk`） |
| 签名密钥 | `/opt/edudesk/edudesk.env`（0600，`root:root`） |
| 反向代理 | **无**（内网直连 :8080；未挂到 nginx，避免子路径前缀破坏根相对资源） |
| 导航页入口 | `http://192.168.3.123/` 已加入 EduDesk 卡片 |

### 目录权限（刻意为之，勿随意 chmod -R 777）

| 路径 | 属主 | 权限 | 理由 |
|---|---|---|---|
| `/opt/edudesk/edudesk` | root | 0755 | 服务不可自改二进制 |
| `/opt/edudesk/terminal`、`admin` | root | 0755 / 文件 0644 | 静态资源只读；**必须对 edudesk 可读**，否则终端与后台一律 403 |
| `/opt/edudesk/edudesk.env` | root | 0600 | 密钥只给 systemd（PID1 以 root 读入）后降权，服务进程自身不读文件 |
| `/opt/edudesk/data` | edudesk | 0700 | 审计与吊销清单的写入区，仅服务账号可达 |

---

## 3. 常用运维命令

```bash
# 状态 / 重启 / 日志
systemctl status edudesk
sudo systemctl restart edudesk
sudo journalctl -u edudesk -n 100 --no-pager
sudo journalctl -u edudesk -f

# 健康检查（dev 必须为 false —— true 表示用的是演示密钥，上线门禁据此否决）
curl -s http://127.0.0.1:8080/health

# 只读冒烟（需要在有 node 的机器上跑；本机已装 Node v22）
EDUDESK_BASE=http://127.0.0.1:8080 EDUDESK_SECRET=<密钥> \
  node <仓库>/implementation/tests/live-smoke.js

# 审计尾查看
sudo tail -20 /opt/edudesk/data/audit.log

# 吊销清单（登出令牌的持久化黑名单）
sudo cat /opt/edudesk/data/revocations.json
```

### 重新部署 / 升级

```bash
# 在打包机上交叉编译并生成新包，然后：
scp edudesk-linux-amd64.tar.gz zxzjxx@192.168.3.123:/tmp/
ssh zxzjxx@192.168.3.123
tar -xzf /tmp/edudesk-linux-amd64.tar.gz -C /tmp/ && cd /tmp/stage
sudo ./install.sh          # 不带密钥参数 = 沿用既有 /opt/edudesk/edudesk.env
```

- `install.sh` **幂等**：二进制与静态资源每次覆盖；`data/` 下已存在的文件**不覆盖**（保留审计与吊销清单）；
- 密钥只在「显式传参」或「目标机尚无 edudesk.env」时写入 —— 常规升级不会顺手轮换密钥；
- 若要轮换密钥：`sudo ./install.sh '<新密钥≥32字节>'`，**终端缓存里的旧 Bundle 会验签失败**，需等一次轮询拿到新包。

---

## 4. 安全属性（与设计文档的对应关系）

| 属性 | 设计依据 | 本机实现 |
|---|---|---|
| 密钥 fail-closed | P0-1 | 无 `EDUDESK_SECRET` 或 <32 字节 → 拒绝启动；unit 不传 `--dev` |
| 只读展示边界 | `04-只读展示API与终端最小权限.md` | `/bundle` 非 GET/HEAD → 405 + `Allow` |
| 数据防火墙 | `02-数据字段白名单.md`（privacylint） | 越界字段整包拒绝（500，不降级下发） |
| 认证与授权分离 | ADR-018 | 未认证 → 401；admin 越界内容操作 → 403 且写 `PUBLISH_GATE_DENY` 审计 |
| 令牌吊销跨重启 | P1-7 | `data/revocations.json` 原子落盘，重启后仍拒绝同令牌 |
| 登录限流 | P1-8 | 同 IP 连错 5 次 → 429 + `Retry-After`（内存态，重启重置） |
| 最小权限运行 | 05 §2.1 思路 | 专用 nologin 账号 + `ProtectSystem=strict` + `NoNewPrivileges` + `MemoryMax=512M` |

> 实测占用：常驻内存约 **3 MB**。这台机器内存 3.2 GB，本服务对其构不成压力。

---

## 5. 治理边界（重要，勿越线）

1. **本机部署 ≠ 获准上屏。** 设计包 Gate G-0 的七条 Exit Criteria 与五道放行门槛
   （隐私 / 撤回 / 可靠性 / 运营 / 推广）**均未因本次部署而通过**。
2. 当前 `data/content-source.json` 是**演示/样例内容**，不含任何真实学生数据 ——
   这符合 Owner 的硬门槛（"真实学生数据上屏前必须有隐私/安全/撤回验收通过"）。
3. 本机是**内网边缘节点**，不是校园网；在校内网络开放端口仍需另行走设计包 §4 STOP 边界与 Owner 批准。
4. `P0-1` 在本机已通过（`dev=false`），但 **PR 门禁中 `P0-1` / `P0-1b` 的判定口径仍需 Owner 裁决**，
   与本次部署无关，勿混为一谈。
