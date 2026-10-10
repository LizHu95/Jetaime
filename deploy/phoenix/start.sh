#!/usr/bin/env bash
# 本地开发用 Phoenix：只监听本机，SQLite 与追踪资料放入已忽略的 deploy/data。
set -euo pipefail

project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
if ! command -v uv >/dev/null 2>&1; then
  echo "需要 uv；安装方法见 https://docs.astral.sh/uv/getting-started/installation/" >&2
  exit 1
fi
mkdir -p "$project_root/deploy/data/phoenix"
export PHOENIX_WORKING_DIR="$project_root/deploy/data/phoenix"
export PHOENIX_HOST=127.0.0.1
export PHOENIX_PORT=6006
export PHOENIX_TELEMETRY_ENABLED=false
exec uv tool run --from arize-phoenix==20.20.0 arize-phoenix serve
