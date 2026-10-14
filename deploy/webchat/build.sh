#!/usr/bin/env bash
set -euo pipefail
umask 077
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
output_dir="$repo_root/outputs/webchat/deploy"
mkdir -p "$output_dir"
bash "$repo_root/deploy/api/build.sh"
for arch in amd64 arm64; do
    cp "$repo_root/deploy/api/api-linux-$arch" "$output_dir/webchat-linux-$arch"
done
cp "$repo_root/deploy/webchat/webchat.service" "$repo_root/deploy/webchat/Caddyfile.example" "$repo_root/deploy/webchat/.env.example" "$repo_root/deploy/webchat/install.sh" "$repo_root/deploy/webchat/README.md" "$output_dir/"
tar -czf "$repo_root/outputs/webchat/webchat-deploy.tar.gz" -C "$repo_root/outputs/webchat" deploy
printf 'Web chat package: %s\n' "$repo_root/outputs/webchat/webchat-deploy.tar.gz"
