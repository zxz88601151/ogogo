#!/usr/bin/env bash
# package.sh —— 为 Linux 节点（adminjiedian / 192.168.3.123）构建 EduDesk 服务端部署包。
#
# 用法（开发机上执行，Windows Git Bash / Linux / WSL 均可）：
#   ./package.sh
#   EDUDESK_SECRET="$(openssl rand -hex 32)" ./package.sh
#
# 产出：
#   server-go/dist/edudesk-linux-amd64.tar.gz
#   server-go/dist/stage/                     （暂存区，含密钥，切勿入库）
#
# 打印 tar 与二进制的 SHA-256，供目标机上比对。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER="$HERE/../server-go"
DIST="$SERVER/dist"
STAGE="$DIST/stage"

if ! command -v go >/dev/null 2>&1; then
  echo "未找到 go，请将 Go 加入 PATH。" >&2
  exit 1
fi

echo "==> 1/5 交叉编译 linux/amd64"
mkdir -p "$DIST"
( cd "$SERVER" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "$DIST/edudesk" ./cmd/edudesk )

echo "==> 2/5 组装暂存区"
rm -rf "$STAGE"
mkdir -p "$STAGE/data"
install -m 0755 "$DIST/edudesk"        "$STAGE/edudesk"
cp -r "$SERVER/terminal"               "$STAGE/terminal"
cp -r "$SERVER/admin"                  "$STAGE/admin"
install -m 0644 "$SERVER/data/content-source.json"  "$STAGE/data/content-source.json"
install -m 0644 "$SERVER/data/users.json"           "$STAGE/data/users.json"
install -m 0644 "$HERE/install.sh"     "$STAGE/install.sh"
install -m 0644 "$HERE/README.md"      "$STAGE/README.md"
install -m 0644 "$HERE/DEPLOY.md"      "$STAGE/DEPLOY.md"
install -m 0644 "$HERE/edudesk.service" "$STAGE/edudesk.service"
chmod +x "$STAGE/install.sh"

# terminal-config.json：以部署副本覆盖（指向本节点），避免误用开发机配置
TEMPLATE="$SERVER/data/terminal-config.json"
if [ -f "$HERE/terminal-config.json" ]; then
  install -m 0644 "$HERE/terminal-config.json" "$STAGE/data/terminal-config.json"
elif [ -f "$TEMPLATE" ]; then
  install -m 0644 "$TEMPLATE" "$STAGE/data/terminal-config.json"
fi

echo "==> 3/5 生成密钥环境文件（0600）"
umask 077
SECRET="${EDUDESK_SECRET:-}"
if [ -z "$SECRET" ]; then
  if command -v openssl >/dev/null 2>&1; then
    SECRET="$(openssl rand -hex 32)"
  else
    SECRET="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  fi
  echo "    未提供 EDUDESK_SECRET，已自动生成新的 32 字节密钥"
else
  echo "    使用调用方提供的 EDUDESK_SECRET"
fi
if [ "${#SECRET}" -lt 32 ]; then
  echo "密钥长度 ${#SECRET} < 32，拒绝打包。" >&2
  exit 1
fi
printf 'EDUDESK_SECRET=%s\n' "$SECRET" > "$STAGE/edudesk.env"
chmod 0600 "$STAGE/edudesk.env"

TARBALL="$DIST/edudesk-linux-amd64.tar.gz"
rm -f "$TARBALL"
( cd "$DIST" && tar -czf "$TARBALL" stage )

echo "==> 4/5 校验和"
echo "    tar       : $(sha256sum "$TARBALL" | cut -d' ' -f1)"
echo "    二进制    : $(sha256sum "$STAGE/edudesk" | cut -d' ' -f1)"
echo "    密钥指纹  : $(printf '%s' "$SECRET" | sha256sum | cut -c1-16)"

echo "==> 5/5 完成"
echo "    产物: $TARBALL"
echo "    投放: scp '$TARBALL' zxzjxx@192.168.3.123:/tmp/"
echo "    安装: ssh zxzjxx@192.168.3.123 'tar -xzf /tmp/edudesk-linux-amd64.tar.gz -C /tmp && sudo /tmp/stage/install.sh'"
echo
echo "注意：$STAGE 与 $TARBALL 含密钥，已在 .gitignore 中排除，用完请删除。"
