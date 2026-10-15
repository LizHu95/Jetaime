#!/usr/bin/env bash
# Run on the Nginx server as root, after deploying default.conf.
set -euo pipefail

ip=${1:?Usage: bash enable-ip-https.sh PUBLIC_IPV4}
python3 - "$ip" <<'PY'
import ipaddress
import sys
address = ipaddress.ip_address(sys.argv[1])
if address.version != 4 or not address.is_global:
    raise SystemExit("Expected a public IPv4 address")
PY
[[ $(id -u) -eq 0 ]] || { echo 'Run as root' >&2; exit 1; }
certbot=/opt/jetaime-certbot/bin/certbot
[[ -x "$certbot" ]] || { echo 'Install Certbot >= 5.4 in /opt/jetaime-certbot first' >&2; exit 1; }
mkdir -p /var/lib/letsencrypt /etc/nginx/jetaime-tls /etc/letsencrypt/renewal-hooks/deploy
nginx -t

"$certbot" certonly --non-interactive --agree-tos --register-unsafely-without-email \
    --preferred-profile shortlived --webroot --webroot-path /var/lib/letsencrypt \
    --ip-address "$ip" --cert-name "$ip"

snippet=/etc/nginx/jetaime-tls/ip.conf
[[ ! -e "$snippet" ]] || { echo "TLS already configured: $snippet" >&2; exit 1; }
cat > "$snippet" <<EOF
listen 443 ssl;
listen [::]:443 ssl;
ssl_certificate /etc/letsencrypt/live/$ip/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/$ip/privkey.pem;
ssl_protocols TLSv1.2 TLSv1.3;
set \$jetaime_request "\$scheme:\$uri";
if (\$jetaime_request ~ "^http:/(?![.]well-known/acme-challenge/)") {
    return 308 https://$ip\$request_uri;
}
EOF
if ! nginx -t; then
    rm -f "$snippet"
    exit 1
fi
systemctl reload nginx

cat > /etc/letsencrypt/renewal-hooks/deploy/jetaime-nginx <<'HOOK'
#!/bin/sh
set -eu
/usr/sbin/nginx -t
/usr/bin/systemctl reload nginx
HOOK
chmod 755 /etc/letsencrypt/renewal-hooks/deploy/jetaime-nginx
cat > /etc/systemd/system/jetaime-certbot-renew.service <<EOF
[Unit]
Description=Renew Jetaime IP HTTPS certificate
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=$certbot renew --quiet
EOF
cat > /etc/systemd/system/jetaime-certbot-renew.timer <<'TIMER'
[Unit]
Description=Check Jetaime HTTPS certificate renewal every six hours

[Timer]
OnCalendar=*-*-* 00/6:00:00
RandomizedDelaySec=1800
Persistent=true

[Install]
WantedBy=timers.target
TIMER
systemctl daemon-reload
systemctl enable --now jetaime-certbot-renew.timer
systemctl is-active jetaime-certbot-renew.timer
echo "HTTPS enabled: https://$ip/page/"
