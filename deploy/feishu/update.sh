#!/usr/bin/env bash
# Replace an existing installation with the current deployment bundle.
set -euo pipefail
umask 077

bundle_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
arch=$(uname -m)
binary=
backup_suffix=$(date +%Y%m%d%H%M%S)

if [ "$(id -u)" -ne 0 ]; then
    echo "Run with sudo: sudo bash update.sh" >&2
    exit 1
fi
case "$arch" in
    x86_64) binary="$bundle_dir/feishu-linux-amd64" ;;
    aarch64|arm64) binary="$bundle_dir/feishu-linux-arm64" ;;
    *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac
if [ ! -f /opt/jetaime-feishu/feishu ] || [ ! -f /etc/jetaime-feishu.env ]; then
    echo "No existing installation; use install.sh first." >&2
    exit 1
fi
# Preserve the current binary, configuration and service for rollback.
cp -p /opt/jetaime-feishu/feishu "/opt/jetaime-feishu/feishu.$backup_suffix.bak"
cp -p /etc/jetaime-feishu.env "/etc/jetaime-feishu.env.$backup_suffix.bak"
cp -p /etc/systemd/system/feishu.service "/etc/systemd/system/feishu.service.$backup_suffix.bak"
install -m 755 "$binary" /opt/jetaime-feishu/feishu.next
mv /opt/jetaime-feishu/feishu.next /opt/jetaime-feishu/feishu
install -m 600 "$bundle_dir/feishu.env" /etc/jetaime-feishu.env
install -m 644 "$bundle_dir/feishu.service" /etc/systemd/system/feishu.service
systemctl daemon-reload
systemctl restart feishu
systemctl is-active feishu
echo "Process running; confirm 'feishu long connection ready' in journalctl."
