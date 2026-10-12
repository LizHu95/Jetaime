#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mkdir -p "$repo_root/local/data/phoenix-v2"
export UV_PROJECT_ENVIRONMENT="$repo_root/evals/.venv-server"
export PHOENIX_HOST=127.0.0.1
export PHOENIX_PORT=6006
export PHOENIX_WORKING_DIR="$repo_root/local/data/phoenix-v2"
export PHOENIX_ENABLE_PROMETHEUS=false
export PHOENIX_TELEMETRY_ENABLED=false
exec uv run --project "$repo_root/evals" --locked --group server phoenix serve
