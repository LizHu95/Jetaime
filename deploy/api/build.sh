#!/usr/bin/env bash
# Package the unified HTTP API with local runtime environment configuration.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
[[ -f "$repo_root/local/.env" ]] || { echo '缺少 local/.env，无法打包部署配置' >&2; exit 1; }
bash -n "$repo_root/local/.env"
output_dir="$repo_root/outputs/api"
mkdir -p "$output_dir"
build_dir=$(mktemp -d "$output_dir/.build-XXXXXX")
trap 'rm -rf -- "$build_dir"' EXIT

for arch in amd64 arm64; do
    (
        cd "$repo_root/services/api"
        CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
            -o "$build_dir/api-linux-$arch" ./cmd/api
    )
done
cp "$repo_root/services/api/config/models.example.json" "$build_dir/models.example.json"
cp "$repo_root/deploy/api/README.md" "$build_dir/README.md"
cp "$repo_root/deploy/api/start.sh" "$build_dir/start.sh"
cp "$repo_root/local/.env" "$build_dir/.env"
chmod 600 "$build_dir/.env"
python3 - "$build_dir" <<'PY'
import hashlib
import sys
import tarfile
from pathlib import Path

source = Path(sys.argv[1])
names = ("api-linux-amd64", "api-linux-arm64", "models.example.json", "README.md", "start.sh", ".env")
(source / "SHA256SUMS").write_text("".join(
    f"{hashlib.sha256((source / name).read_bytes()).hexdigest()}  {name}\n"
    for name in names
))
with tarfile.open(source / "api-deploy.tar.gz", "w:gz") as archive:
    for name in (*names, "SHA256SUMS"):
        archive.add(source / name, arcname="api/" + name)
PY
chmod 600 "$build_dir/api-deploy.tar.gz"
for name in api-linux-amd64 api-linux-arm64 models.example.json README.md start.sh .env SHA256SUMS api-deploy.tar.gz; do
    mv -f -- "$build_dir/$name" "$output_dir/$name"
done
printf 'API deployment package: %s\n' "$output_dir/api-deploy.tar.gz"
