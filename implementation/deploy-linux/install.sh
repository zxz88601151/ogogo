#!/usr/bin/env bash
# install.sh —— EduDesk 服务端在 Linux 节点（adminjiedian / 192.168.3.123）上的部署脚本。
#
# 用法（在解包后的暂存目录内、以 root 执行）：
#   sudo ./install.sh                 # 首次部署：密钥取自随包 edudesk.env
#   sudo ./install.sh '<32+字节密钥>'   # 显式指定 / 轮换密钥
#   sudo ./install.sh                 # 再次执行即为「幂等升级」：沿用目标机既有密钥与 data/
#
# 设计要点：
#   1. 专用系统用户 edudesk（nologin），least privilege，不跑 root。
#   2. 密钥经 /opt/edudesk/edudesk.env（0600 root:root）注入，不进命令行、不进仓库、不进 ps。
#   3. 二进制与静态资源归 root（服务不可自改），仅 data/ 归 edudesk 可写。
#   4. 生产密钥 fail-closed：拒绝 <32 字节密钥、拒绝演示密钥、绝不传 --dev。
#   5. 幂等：data/ 下既有文件不覆盖（保留审计与吊销清单）；密钥不因常规升级而被动轮换。
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DST=/opt/edudesk
SVC_USER=edudesk

if [ "$(id -u)" -ne 0 ]; then
  echo "必须以 root 执行（sudo ./install.sh）" >&2
  exit 1
fi

# ---------- 密钥解析 ----------
SECRET_ARG="${1:-}"
SECRET="$SECRET_ARG"
if [ -z "$SECRET" ] && [ -f "$SRC/edudesk.env" ]; then
  # 随包投放的 env（避免密钥出现在命令行与 ps 输出中）
  SECRET="$(sed -n 's/^EDUDESK_SECRET=//p' "$SRC/edudesk.env" | head -1)"
fi

# 需要写密钥吗？显式传参、或目标机尚不存在 env 时，必须写。
WRITE_ENV=0
if [ -n "$SECRET_ARG" ] || [ ! -f "$DST/edudesk.env" ]; then
  WRITE_ENV=1
fi

if [ "$WRITE_ENV" -eq 1 ]; then
  if [ -z "$SECRET" ]; then
    echo "缺少签名密钥。用法：sudo ./install.sh '<32+ 字节随机串>'" >&2
    exit 1
  fi
  if [ "${#SECRET}" -lt 32 ]; then
    echo "密钥长度 ${#SECRET} < 32 字节，P0-1 fail-closed 拒绝部署。" >&2
    exit 1
  fi
  if [ "$SECRET" = "dev-only-secret-change-me" ]; then
    echo "检测到演示密钥，拒绝部署到服务器。" >&2
    exit 1
  fi
fi

echo "==> 1/7 创建服务账号 ${SVC_USER}"
if ! id "$SVC_USER" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
  echo "    已创建系统用户（nologin）"
else
  echo "    已存在，跳过"
fi

echo "==> 2/7 同步二进制与静态资源到 ${DST}"
install -d -m 0755 "$DST"
install -d -m 0700 "$DST/data"
install -m 0755 "$SRC/edudesk" "$DST/edudesk"
rm -rf "$DST/terminal" "$DST/admin"
cp -r "$SRC/terminal" "$SRC/admin" "$DST/"
chown -R root:root "$DST/terminal" "$DST/admin"
# 静态资源必须对服务账号可读：tar/cp 会沿用 0700 源权限，若不显式放开，
# 服务以 edudesk 身份运行时读不到文件，终端与后台一律 403（已实测踩到）。
chmod 0755 "$DST/terminal" "$DST/admin"
find "$DST/terminal" "$DST/admin" -type f -exec chmod 0644 {} +
find "$DST/terminal" "$DST/admin" -type d -exec chmod 0755 {} +
[ -f "$SRC/DEPLOY.md" ] && install -m 0644 "$SRC/DEPLOY.md" "$DST/DEPLOY.md"

echo "==> 3/7 同步数据文件（既有文件不覆盖）"
for f in content-source.json users.json terminal-config.json; do
  if [ ! -f "$DST/data/$f" ]; then
    install -m 0644 "$SRC/data/$f" "$DST/data/$f"
    echo "    补种 $f"
  else
    echo "    保留既有 data/$f（不覆盖现场状态）"
  fi
done
chown -R "$SVC_USER:$SVC_USER" "$DST/data"
chmod 0700 "$DST/data"

echo "==> 4/7 处理密钥环境文件"
if [ "$WRITE_ENV" -eq 1 ]; then
  umask 077
  printf 'EDUDESK_SECRET=%s\n' "$SECRET" > "$DST/edudesk.env"
  chown root:root "$DST/edudesk.env"
  chmod 0600 "$DST/edudesk.env"
  echo "    已写入（0600 root:root，长度 ${#SECRET}）"
else
  echo "    保留既有 edudesk.env（如需轮换请显式传密钥：sudo ./install.sh '<新密钥>'）"
fi

echo "==> 5/7 安装 systemd 单元"
install -m 0644 "$SRC/edudesk.service" /etc/systemd/system/edudesk.service
systemctl daemon-reload

echo "==> 6/7 启用并启动服务"
systemctl enable edudesk.service >/dev/null
systemctl restart edudesk.service

echo "==> 7/7 等待就绪并自检"
for _ in $(seq 1 20); do
  if curl -fsS --max-time 2 http://127.0.0.1:8080/health >/dev/null 2>&1; then break; fi
  sleep 0.5
done
echo "--- /health ---"
curl -fsS --max-time 3 http://127.0.0.1:8080/health; echo
echo "--- systemctl ---"
systemctl --no-pager --lines=5 status edudesk.service | head -12
echo
echo "部署完成。终端： http://192.168.3.123:8080/?profile=classroom"
echo "         后台： http://192.168.3.123:8080/admin/"
