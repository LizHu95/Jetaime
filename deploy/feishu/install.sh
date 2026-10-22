#!/usr/bin/env bash
# First-time installation on a Linux systemd server.
set -euo pipefail

bundle_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
arch=$(uname -m)
binary=

if [ "$(id -u)" -ne 0 ]; then
    echo "Run with sudo: sudo bash install.sh " >&2
    exit 1
fi
case "$arch" in
    x86_64) binary="$bundle_dir/feishu-linux-amd64" ;;
    aarch64|arm64) binary="$bundle_dir/feishu-linux-arm64" ;;
    *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac
if [ -e /etc/jetaime-feishu.env ] || [ -e /opt/jetaime-feishu/feishu ] || [ -e /etc/systemd/system/feishu.service ]; then
    echo "Existing installation detected; use the update procedure instead." >&2
    exit 1
fi
if ! id feishu >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin feishu
fi
install -d -o root -g root -m 755 /opt/jetaime-feishu
install -m 755 "$binary" /opt/jetaime-feishu/feishu
install -m 600 "$bundle_dir/feishu.env" /etc/jetaime-feishu.env
install -m 644 "$bundle_dir/feishu.service" /etc/systemd/system/feishu.service
systemctl daemon-reload
systemctl enable --now feishu
systemctl is-active feishu
