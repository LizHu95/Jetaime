#!/usr/bin/env bash
# Run remotely with root privileges; read the new site configuration from stdin.
set -euo pipefail

target=/etc/nginx/sites-available/default
enabled=/etc/nginx/sites-enabled/default
command -v nginx >/dev/null
command -v systemctl >/dev/null
mkdir -p /etc/nginx/sites-available /etc/nginx/sites-enabled
# The trace UI contains private conversations. Keep its proxy credentials persistent.
if [[ ! -s /etc/nginx/phoenix.htpasswd ]]; then
    command -v openssl >/dev/null
    install -d -m 700 /etc/jetaime-phoenix
    password=$(openssl rand -hex 18)
    hash=$(printf '%s' "$password" | openssl passwd -6 -stdin)
    (umask 077; printf 'username=phoenix\npassword=%s\n' "$password" > /etc/jetaime-phoenix/access.txt)
    printf 'phoenix:%s\n' "$hash" > /etc/nginx/phoenix.htpasswd
    chown root:www-data /etc/nginx/phoenix.htpasswd
    chmod 640 /etc/nginx/phoenix.htpasswd
    unset password hash
fi
if [[ -e "$enabled" || -L "$enabled" ]]; then
    [[ "$(readlink -f "$enabled")" == "$target" ]] || {
        echo "启用站点 $enabled 未指向 $target，请先检查配置" >&2
        exit 1
    }
fi

staging=$(mktemp /etc/nginx/.default-new.XXXXXX)
trap 'rm -f -- "$staging"' EXIT
cat > "$staging"
[[ -s "$staging" ]] || { echo 'Nginx 配置为空' >&2; exit 1; }
backup=
if [[ -e "$target" ]]; then
    backup=$(mktemp /etc/nginx/default.backup.XXXXXX)
    cp -p -- "$target" "$backup"
fi
created_link=false
rollback() {
    local status=$?
    trap - ERR
    if [[ -n "$backup" ]]; then
        cp -p -- "$backup" "$target"
    else
        rm -f -- "$target"
    fi
    if [[ "$created_link" == true ]]; then rm -f -- "$enabled"; fi
    echo "Nginx 配置应用失败，已恢复原配置；备份：${backup:-无原配置}" >&2
    exit "$status"
}
trap rollback ERR
install -m 644 "$staging" "$target"
if [[ ! -e "$enabled" && ! -L "$enabled" ]]; then
    ln -s "$target" "$enabled"
    created_link=true
fi
nginx -t
if systemctl is-active --quiet nginx; then
    systemctl reload nginx
else
    systemctl start nginx
fi
trap - ERR
printf 'Nginx 配置已应用：%s\n原配置备份：%s\n' "$target" "${backup:-无原配置}"
