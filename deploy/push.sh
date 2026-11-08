#!/usr/bin/env bash
# Build and upload API/Web, apply Nginx, then migrate and restart the API.
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

command -v rsync >/dev/null || { echo '本机需要 rsync，请安装后重新部署' >&2; exit 1; }
printf 'Checking deployment target %s...\n' "$server"
remote_arch=$(ssh "$server" 'command -v rsync >/dev/null && command -v flock >/dev/null && command -v sha256sum >/dev/null && uname -m') || {
    echo '远端连接或部署工具检查失败，需要 rsync、flock 和 sha256sum' >&2
    exit 1
}
case "$remote_arch" in
    x86_64) api_arch=amd64 ;;
    aarch64|arm64) api_arch=arm64 ;;
    *) echo "不支持的服务器架构：$remote_arch" >&2; exit 1 ;;
esac
printf 'Building API (%s) and Web...\n' "$api_arch"
make -C "$repo_root" api-build web-build API_BUILD_ARGS="--arch $api_arch --no-archive"

deploy_temp=$(mktemp -d)
trap 'rm -rf -- "$deploy_temp"' EXIT
nginx_config="$deploy_temp/nginx.conf"
api_files="$deploy_temp/api-files"
manifest_hash=$(python3 - "$repo_root/outputs/api/SHA256SUMS" "$api_files" <<'PY'
import hashlib
from pathlib import Path
import sys

manifest = Path(sys.argv[1]).read_bytes()
names = [line.split("  ", 1)[1] for line in manifest.decode().splitlines()]
Path(sys.argv[2]).write_text("\n".join([*names, "SHA256SUMS"]) + "\n")
print(hashlib.sha256(manifest).hexdigest())
PY
)
python3 - "$repo_root/deploy/nginx/default.conf" "$web_dir" > "$nginx_config" <<'PY'
from pathlib import Path
import sys

directory = sys.argv[2].rstrip("/")
if "\n" in directory or "\r" in directory:
    raise SystemExit("Web 目录不能含换行符")
directory = directory.replace("\\", "\\\\").replace('"', '\\"').replace("$", "\\$")
config = Path(sys.argv[1]).read_text()
config = config.replace("alias /var/www/jetaime/;", f'alias "{directory}/";')
for asset in ("sw.js", "manifest.webmanifest"):
    config = config.replace(f"alias /var/www/jetaime/{asset};", f'alias "{directory}/{asset}";')
print(config, end="")
PY

printf 'Preparing Web directory on %s: %s...\n' "$server" "$web_dir"
ssh "$server" "mkdir -p -- $quoted_web_dir"
# Resolve remote home explicitly instead of relying on remote-shell tilde expansion.
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
upload_dir="${api_parent%/}/.jetaime-api-upload-$api_arch"
printf -v quoted_upload_dir '%q' "$upload_dir"
printf -v quoted_upload_lock '%q' "$upload_dir.lock"
ssh "$server" "umask 077; mkdir -p -- $quoted_api_parent $quoted_upload_dir/api; chmod 700 -- $quoted_upload_dir"
printf 'Syncing API files to %s:%s/ (compression, delta transfer, resume)...\n' "$server" "$upload_dir"
# The stable private cache provides a delta basis across releases and retains
# partial files after interruption. Never transfer directly onto live binaries.
rsync -rlptzc --partial-dir=.rsync-partial --progress --stats \
    --files-from="$api_files" --rsync-path="flock -w 120 $quoted_upload_lock rsync" \
    "$repo_root/outputs/api/" "$server:$quoted_upload_dir/api/"
# Copy verified cache files into fresh inodes on the same filesystem, then rename
# them over installed artifacts. Keep the cache intact for subsequent transfers.
api_installer=$(cat <<'SH'
set -euo pipefail
cache=$1
parent=$2
manifest_hash=$3
arch=$4
[[ "$arch" == amd64 || "$arch" == arm64 ]] || exit 1
umask 077
# Share the rsync lock: concurrent uploads cannot change files during validation
# or installation. Detect an intervening release before running its migrations.
exec 9>"$cache.lock"
flock -w 120 9
incoming="$cache/api"
printf '%s  %s\n' "$manifest_hash" "$incoming/SHA256SUMS" | sha256sum -c -
(cd "$incoming" && sha256sum -c SHA256SUMS)
[[ -f "$incoming/api-linux-$arch" ]] || { echo '上传文件缺少目标架构程序' >&2; exit 1; }
staging=$(mktemp -d "$parent/.jetaime-api-install.XXXXXX")
trap 'rm -rf -- "$staging"' EXIT
mkdir -p "$staging/api"
# Only install manifest-listed files; an optional file removed from the next
# build may still exist in the reusable cache and must not be deployed again.
while read -r digest name; do
    if [[ -z "$name" || "$name" == /* || "$name" == ../* || "$name" == */../* || "$name" == */.. ]]; then
        echo '无效部署文件路径' >&2; exit 1
    fi
    mkdir -p -- "$staging/api/$(dirname -- "$name")"
    cp -p -- "$incoming/$name" "$staging/api/$name"
done < "$incoming/SHA256SUMS"
cp -p -- "$incoming/SHA256SUMS" "$staging/api/SHA256SUMS"
# Check the incoming binary/configuration before replacing any installed files.
mkdir -p "$parent/api"
# Keep migration failures separate from model compatibility failures: their remedies differ.
if ! (cd "$parent/api" && bash "$staging/api/start.sh" migrate); then
    echo '数据库迁移失败，未替换 API、配置、Web 或 Nginx，现有进程保持运行。请按上方数据库错误处理后重新部署；已成功提交的迁移不会自动回滚。' >&2
    exit 1
fi
if ! (cd "$parent/api" && bash "$staging/api/start.sh" check); then
    echo '记忆配置预检失败，未替换 API、配置、Web 或 Nginx，现有进程保持运行。请核对上方错误；模型或 pipeline 不兼容时恢复原记忆配置，或完成显式索引重建。' >&2
    exit 1
fi
mkdir -p "$parent/api/phoenix"
for file in "$staging/api/phoenix/"*; do
    mv -f -- "$file" "$parent/api/phoenix/"
done
rmdir "$staging/api/phoenix"
for name in "api-linux-$arch" models.example.json memory.example.json README.md start.sh .env SHA256SUMS; do
    mv -f -- "$staging/api/$name" "$parent/api/$name"
done
if [[ -f "$staging/api/models.json" ]]; then
    mv -f -- "$staging/api/models.json" "$parent/api/models.json"
fi
if [[ -f "$staging/api/memory.json" ]]; then
    mv -f -- "$staging/api/memory.json" "$parent/api/memory.json"
fi
rmdir "$staging/api" "$staging"
SH
)
quoted_api_installer=$(python3 -c 'import shlex, sys; print(shlex.quote(sys.argv[1]))' "$api_installer")
ssh "$server" "bash -c $quoted_api_installer -- $quoted_upload_dir $quoted_api_parent $manifest_hash $api_arch"
printf 'Uploading Web to %s:%s/...\n' "$server" "$web_dir"
rsync -rlptzc --partial-dir=.rsync-partial --delay-updates --progress --stats \
    "$repo_root/outputs/web/" "$server:$quoted_web_dir/"
ssh "$server" "chmod -R a+rX -- $quoted_web_dir"
printf 'Installing and reloading Nginx configuration on %s...\n' "$server"
quoted_installer=$(python3 -c 'import pathlib, shlex, sys; print(shlex.quote(pathlib.Path(sys.argv[1]).read_text()))' "$repo_root/deploy/nginx/install.sh")
ssh "$server" "if [ \"\$(id -u)\" -eq 0 ]; then bash -c $quoted_installer; else sudo -n bash -c $quoted_installer; fi" < "$nginx_config"
printf 'Restarting API on %s...\n' "$server"
ssh "$server" "cd -- $quoted_api_parent && bash api/start.sh restart"
printf 'Deploy complete: API %s, Web %s:%s/\n' "$destination" "$server" "$web_dir"
