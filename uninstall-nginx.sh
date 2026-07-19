#!/usr/bin/env bash
set -euo pipefail

AUTO_CONFIRM=0
if [ "${1:-}" = "-y" ]; then
    AUTO_CONFIRM=1
fi

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    printf 'ERROR: 请使用 root 权限运行此脚本\n' >&2
    exit 1
fi

if [ "$AUTO_CONFIRM" != "1" ]; then
    printf '此操作将停止并卸载 NGINX，同时删除 /etc/nginx 配置。继续？[y/N] '
    read -r answer
    case "$answer" in
        y|Y) ;;
        *) exit 0 ;;
    esac
fi

systemctl disable --now nginx 2>/dev/null || true
DEBIAN_FRONTEND=noninteractive apt-get purge -y nginx
rm -f /etc/apt/sources.list.d/nginx.list
rm -f /etc/apt/preferences.d/99nginx
rm -f /usr/share/keyrings/nginx-archive-keyring.gpg
rm -rf /usr/local/nginx
apt-get update -qq
printf 'NGINX 已卸载。\n'
