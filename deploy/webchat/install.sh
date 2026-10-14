#!/usr/bin/env bash
set -euo pipefail
umask 077
bundle_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [ "$(id -u)" -ne 0 ]; then echo "Run with sudo: sudo bash install.sh" >&2; exit 1; fi
case "$(uname -m)" in
    x86_64) binary="$bundle_dir/webchat-linux-amd64" ;;
    aarch64|arm64) binary="$bundle_dir/webchat-linux-arm64" ;;
    *) echo "Unsupported architecture" >&2; exit 1 ;;
esac
if [ ! -f /etc/jetaime-webchat.env ]; then echo "Create /etc/jetaime-webchat.env with WEBCHAT_PUBLIC_URL and WEBCHAT_INVITE_CODE first." >&2; exit 1; fi
if ! id webchat >/dev/null 2>&1; then useradd --system --home /opt/jetaime-webchat --shell /usr/sbin/nologin webchat; fi
install -d -m 755 /opt/jetaime-webchat
install -d -m 700 -o webchat -g webchat /var/lib/jetaime-webchat
if [ -f /opt/jetaime-webchat/webchat ]; then cp -p /opt/jetaime-webchat/webchat "/opt/jetaime-webchat/webchat.$(date +%Y%m%d%H%M%S).bak"; fi
install -m 755 "$binary" /opt/jetaime-webchat/webchat.next
mv /opt/jetaime-webchat/webchat.next /opt/jetaime-webchat/webchat
chmod 600 /etc/jetaime-webchat.env
install -m 644 "$bundle_dir/webchat.service" /etc/systemd/system/webchat.service
systemctl daemon-reload
systemctl enable --now webchat
systemctl restart webchat
systemctl is-active webchat
