#!/usr/bin/env bash
set -euo pipefail

# Install the official NGINX mainline package from nginx.org. The package
# repository is authenticated with the published NGINX signing key.

NGINX_KEY_FINGERPRINT="573BFD6B3D8FBC641079A6ABABF5BD827BD9BF62"
TMP_DIR="$(mktemp -d /tmp/arcway-nginx.XXXXXX)"
chmod 0700 "$TMP_DIR"
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

info() {
    printf '[INFO] %s\n' "$*"
}

fail() {
    printf '[ERROR] %s\n' "$*" >&2
    exit 1
}

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    fail "请使用 root 权限运行此脚本"
fi

if [ ! -r /etc/os-release ]; then
    fail "无法识别操作系统"
fi

. /etc/os-release
case "${ID:-}" in
    debian|ubuntu) ;;
    *) fail "仅支持 Debian 和 Ubuntu" ;;
esac

CODENAME="${VERSION_CODENAME:-}"
if [ -z "$CODENAME" ] && command -v lsb_release >/dev/null 2>&1; then
    CODENAME="$(lsb_release -cs)"
fi
if [ -z "$CODENAME" ]; then
    fail "无法确定发行版代号"
fi

info "安装 NGINX 官方仓库依赖"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg2 debian-archive-keyring

info "校验 NGINX 软件源签名密钥"
curl --proto '=https' --tlsv1.2 -fsSL \
    https://nginx.org/keys/nginx_signing.key \
    -o "$TMP_DIR/nginx_signing.key"
if ! gpg --batch --show-keys --with-colons "$TMP_DIR/nginx_signing.key" \
    | awk -F: '$1 == "fpr" { print $10 }' \
    | grep -qx "$NGINX_KEY_FINGERPRINT"; then
    fail "NGINX 签名密钥指纹不匹配"
fi
gpg --batch --yes --dearmor \
    --output "$TMP_DIR/nginx-archive-keyring.gpg" \
    "$TMP_DIR/nginx_signing.key"
install -m 0644 "$TMP_DIR/nginx-archive-keyring.gpg" \
    /usr/share/keyrings/nginx-archive-keyring.gpg

printf 'deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] https://nginx.org/packages/mainline/%s %s nginx\n' \
    "$ID" "$CODENAME" > /etc/apt/sources.list.d/nginx.list
cat > /etc/apt/preferences.d/99nginx <<'EOF'
Package: *
Pin: origin nginx.org
Pin: release o=nginx
Pin-Priority: 900
EOF

info "安装 NGINX mainline"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y nginx

NGINX_BIN="$(command -v nginx)"
if [ -z "$NGINX_BIN" ] || [ ! -x "$NGINX_BIN" ]; then
    fail "NGINX 安装后未找到可执行文件"
fi

# Arcway historically uses /usr/local/nginx paths. Keep those paths stable
# while letting the signed distribution package own the binary and service.
install -d -m 0755 \
    /usr/local/nginx/sbin \
    /etc/nginx/cert \
    /etc/nginx/servers \
    /etc/nginx/stream_servers \
    /etc/nginx/html
ln -sfn "$NGINX_BIN" /usr/local/nginx/sbin/nginx
ln -sfn /etc/nginx/nginx.conf /usr/local/nginx/nginx.conf
ln -sfn /etc/nginx/cert /usr/local/nginx/cert
ln -sfn /etc/nginx/servers /usr/local/nginx/servers
ln -sfn /etc/nginx/stream_servers /usr/local/nginx/stream_servers
ln -sfn /etc/nginx/html /usr/local/nginx/html

"$NGINX_BIN" -t
systemctl enable --now nginx
info "NGINX 安装完成: $("$NGINX_BIN" -v 2>&1)"
