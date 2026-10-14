#!/usr/bin/env bash
# Build Linux binaries and package the local Feishu configuration for deployment.
set -euo pipefail
umask 077

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
output_dir="$repo_root/outputs/feishu/deploy"
env_file="$repo_root/local/feishu/.env"
archive="$repo_root/outputs/feishu/feishu-deploy.tar.gz"
arch=

if [ ! -f "$env_file" ]; then
    echo "Missing configuration: $env_file" >&2
    exit 1
fi
mkdir -p "$output_dir"
for arch in amd64 arm64; do
    (
        cd "$repo_root/services/api"
        CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
            -o "$output_dir/feishu-linux-$arch" ./cmd/feishu
    )
done
cp "$env_file" "$output_dir/feishu.env"
chmod 600 "$output_dir/feishu.env"
cp "$repo_root/deploy/feishu/feishu.service" "$repo_root/deploy/feishu/install.sh" "$repo_root/deploy/feishu/update.sh" "$output_dir/"
python3 - "$output_dir" "$archive" <<'PYTHON'
import sys
import tarfile
from pathlib import Path

source = Path(sys.argv[1])
with tarfile.open(sys.argv[2], "w:gz") as archive:
    for name in ("feishu-linux-amd64", "feishu-linux-arm64", "feishu.env", "feishu.service", "install.sh", "update.sh"):
        archive.add(source / name, arcname="feishu-deploy/" + name)
PYTHON
chmod 600 "$archive"
printf 'Deployment package: %s\nContains private Feishu configuration.\n' "$archive"
