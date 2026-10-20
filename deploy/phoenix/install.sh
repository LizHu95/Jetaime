#!/usr/bin/env bash
# Install the packaged Phoenix definition on Ubuntu/systemd. Data survives upgrades.
set -euo pipefail
[[ $(id -u) -eq 0 ]] || { echo 'Phoenix 部署需要 root 或 sudo 权限' >&2; exit 1; }
phoenix_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
action=${1:-start}
[[ "$action" == start || "$action" == restart ]] || { echo 'Phoenix action must be start or restart' >&2; exit 2; }
command -v systemctl >/dev/null
command -v curl >/dev/null
command -v flock >/dev/null
exec 9>/run/lock/jetaime-phoenix-deploy.lock
flock -w 120 9
version=$(sed -n 's/^arize-phoenix==//p' "$phoenix_dir/requirements.txt")
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Phoenix 版本必须固定' >&2; exit 1; }
venv=/opt/jetaime-phoenix
changed=false
installed=
if [[ -x "$venv/bin/python" ]]; then
    installed=$("$venv/bin/python" -c 'from importlib.metadata import version; print(version("arize-phoenix"))' 2>/dev/null || true)
fi
if [[ "$installed" != "$version" ]]; then
    if ! python3 -c 'import venv, ensurepip' 2>/dev/null; then
        apt-get update
        apt-get install -y python3-venv python3-pip
    fi
    # SQLite online backup also works while the old collector is running.
    if [[ -f /var/lib/jetaime-phoenix/phoenix.db ]]; then
        install -d -m 700 /var/backups/jetaime-phoenix
        python3 - <<'PY'
import sqlite3
from datetime import datetime, timezone
from pathlib import Path
p = Path('/var/backups/jetaime-phoenix') / (datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ') + '.db')
with sqlite3.connect('file:/var/lib/jetaime-phoenix/phoenix.db?mode=ro', uri=True) as source, sqlite3.connect(p) as target:
    source.backup(target)
p.chmod(0o600)
PY
    fi
    python3 -m venv "$venv"
    "$venv/bin/pip" install --disable-pip-version-check -r "$phoenix_dir/requirements.txt"
    "$venv/bin/pip" freeze > "$venv/requirements.lock"
    changed=true
fi
id phoenix >/dev/null 2>&1 || useradd --system --home-dir /var/lib/jetaime-phoenix --shell /usr/sbin/nologin phoenix
install -d -o phoenix -g phoenix /var/lib/jetaime-phoenix
if ! cmp -s "$phoenix_dir/phoenix.service" /etc/systemd/system/phoenix.service; then
    install -m 644 "$phoenix_dir/phoenix.service" /etc/systemd/system/phoenix.service
    systemctl daemon-reload
    changed=true
fi
systemctl enable phoenix
if [[ "$changed" == true || "$action" == restart ]]; then
    systemctl restart phoenix
else
    systemctl start phoenix
fi
# Phoenix applies its own Alembic migrations during startup; readiness checks DB access.
for ((attempt=0; attempt<90; attempt++)); do
    if curl --max-time 2 -fsS http://127.0.0.1:6006/readyz >/dev/null 2>&1; then
        echo "Phoenix $version 已就绪（数据库迁移完成）"
        exit 0
    fi
    if ! systemctl is-active --quiet phoenix; then break; fi
    sleep 1
done
echo 'Phoenix 未就绪，API 未重启；请运行 journalctl -u phoenix -n 50' >&2
exit 1
