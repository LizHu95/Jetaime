#!/usr/bin/env bash
# Build API and Web locally, then copy both output directories to the server.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

if [[ $# -gt 2 ]]; then
    printf 'Usage: %s [user@host:api-parent-directory] [absolute-web-directory]\n' "$0" >&2
    exit 1
fi
if [[ ${1:-} == -h || ${1:-} == --help ]]; then
    printf 'Usage: %s [user@host:api-parent-directory] [absolute-web-directory]\nDefaults: API jetaime-server:~/api/, Web jetaime-server:/var/www/jetaime/\n' "$0"
    exit 0
fi

destination=${1:-jetaime-server:~/}
web_dir=${2:-/var/www/jetaime}
[[ "$destination" == *:* && "$web_dir" == /* ]] || {
    echo 'API 目标需为 user@host:目录，Web 目录需为绝对路径' >&2
    exit 1
}
server=${destination%%:*}
printf -v quoted_web_dir '%q' "$web_dir"

printf 'Building API and Web...\n'
make -C "$repo_root" api-build web-build

nginx_config=$(mktemp)
trap 'rm -f -- "$nginx_config"' EXIT
python3 - "$repo_root/deploy/nginx/default.conf" "$web_dir" > "$nginx_config" <<'PY'
from pathlib import Path
import sys

directory = sys.argv[2].rstrip("/")
if "\n" in directory or "\r" in directory:
    raise SystemExit("Web 目录不能含换行符")
directory = directory.replace("\\", "\\\\").replace('"', '\\"').replace("$", "\\$")
config = Path(sys.argv[1]).read_text()
print(config.replace("alias /var/www/jetaime/;", f'alias "{directory}/";'), end="")
PY

printf 'Preparing Web directory on %s: %s...\n' "$server" "$web_dir"
ssh "$server" "mkdir -p -- $quoted_web_dir"
# Resolve remote home explicitly instead of relying on scp/SFTP tilde expansion.
remote_home=$(ssh "$server" 'printf "%s" "$HOME"')
api_parent=${destination#*:}
case "$api_parent" in
    ''|'~') api_parent=$remote_home ;;
    '~/'*) api_parent="$remote_home/${api_parent:2}" ;;
    /*) ;;
    *) api_parent="$remote_home/$api_parent" ;;
esac
api_parent=${api_parent%/}
api_parent=${api_parent:-/}
printf -v quoted_api_parent '%q' "$api_parent"
printf -v quoted_upload_template '%q' "${api_parent%/}/.jetaime-api-upload.XXXXXX"
upload_dir=$(ssh "$server" "mkdir -p -- $quoted_api_parent && mktemp -d $quoted_upload_template")
[[ "$upload_dir" == "${api_parent%/}/.jetaime-api-upload."* ]] || {
    echo '无法确定远端 API 临时上传目录' >&2
    exit 1
}
printf -v quoted_upload_dir '%q' "$upload_dir"
printf 'Uploading API package to %s:%s/...\n' "$server" "$upload_dir"
scp "$repo_root/outputs/api/api-deploy.tar.gz" "$server:$upload_dir/"
# A running Linux executable cannot be overwritten in place. Extract to new
# files on the same filesystem, then rename them over the installed artifacts.
api_installer=$(cat <<'SH'
set -euo pipefail
staging=$1
parent=$2
tar -xzf "$staging/api-deploy.tar.gz" -C "$staging"
(cd "$staging/api" && sha256sum -c SHA256SUMS)
mkdir -p "$parent/api"
for name in api-linux-amd64 api-linux-arm64 models.example.json README.md start.sh .env SHA256SUMS; do
    mv -f -- "$staging/api/$name" "$parent/api/$name"
done
if [[ -f "$staging/api/models.json" ]]; then
    mv -f -- "$staging/api/models.json" "$parent/api/models.json"
fi
mv -f -- "$staging/api-deploy.tar.gz" "$parent/api/api-deploy.tar.gz"
rmdir "$staging/api" "$staging"
SH
)
quoted_api_installer=$(python3 -c 'import shlex, sys; print(shlex.quote(sys.argv[1]))' "$api_installer")
ssh "$server" "bash -c $quoted_api_installer -- $quoted_upload_dir $quoted_api_parent"
printf 'Uploading Web to %s:%s/...\n' "$server" "$web_dir"
scp -r "$repo_root/outputs/web/." "$server:$web_dir/"
ssh "$server" "chmod -R a+rX -- $quoted_web_dir"
printf 'Installing and reloading Nginx configuration on %s...\n' "$server"
quoted_installer=$(python3 -c 'import pathlib, shlex, sys; print(shlex.quote(pathlib.Path(sys.argv[1]).read_text()))' "$repo_root/deploy/nginx/install.sh")
ssh "$server" "if [ \"\$(id -u)\" -eq 0 ]; then bash -c $quoted_installer; else sudo -n bash -c $quoted_installer; fi" < "$nginx_config"
printf 'Upload complete: API %s, Web %s:%s/\n' "$destination" "$server" "$web_dir"
