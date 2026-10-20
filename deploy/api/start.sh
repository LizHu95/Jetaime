#!/usr/bin/env bash
set -euo pipefail

# Place this script beside the packaged Linux binaries.
# Authentication is enabled by the binary by default; provide JETAIME_DATABASE_URL.
api_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
case "$(uname -m)" in
  x86_64) binary="$api_dir/api-linux-amd64" ;;
  aarch64|arm64) binary="$api_dir/api-linux-arm64" ;;
  *) echo "不支持的服务器架构：$(uname -m)" >&2; exit 1 ;;
esac
runtime_dir="$api_dir/runtime"
pid_file="$runtime_dir/api.pid"
log_file="$runtime_dir/api.log"
action=${1:-restart}
if (($#)); then shift; fi

# Only actions that launch the binary need runtime configuration.
if [[ "$action" == start || "$action" == restart || "$action" == migrate || "$action" == memory-backfill ]]; then
  if [[ -f "$api_dir/.env" ]]; then
    set -a
    source "$api_dir/.env"
    set +a
  fi
fi

is_api() {
  local candidate=$1 executable
  [[ "$candidate" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$candidate" 2>/dev/null || return 1
  executable=$(readlink "/proc/$candidate/exe") || return 1
  # Replacing a deployment binary can leave the old process marked as deleted.
  [[ "${executable% (deleted)}" == "$binary" ]]
}

running() {
  [[ -f "$pid_file" ]] || return 1
  read -r pid < "$pid_file" || return 1
  is_api "$pid"
}

stop_pid() {
  local target=$1
  is_api "$target" || return 0
  kill -TERM "$target"
  for ((i=0; i<10; i++)); do
    if ! is_api "$target"; then return 0; fi
    sleep 1
  done
  echo "API 尚未退出，PID：$target，请查看：$log_file" >&2
  return 1
}

restart_api() {
  local address="${API_ADDR:-0.0.0.0:8081}" port listeners candidate
  local -a owners=()
  # Match the effective -addr flag passed to the Go binary.
  while (($#)); do
    case "$1" in
      -addr|--addr)
        (($# >= 2)) || { echo '缺少 -addr 参数值' >&2; return 1; }
        address=$2; shift ;;
      -addr=*|--addr=*) address=${1#*=} ;;
    esac
    shift
  done
  port=${address##*:}
  [[ "$port" =~ ^[0-9]+$ ]] || { echo "无效监听地址：$address" >&2; return 1; }
  command -v ss >/dev/null || { echo '需要 ss 检查端口，请安装 iproute2' >&2; return 1; }
  listeners=$(ss -H -ltnp "sport = :$port") || return 1
  if [[ -n "$listeners" ]]; then
    while read -r candidate; do
      [[ -n "$candidate" ]] || continue
      if ! is_api "$candidate"; then
        echo "端口 $port 被其他程序占用，未停止该进程：" >&2
        printf '%s\n' "$listeners" >&2
        return 1
      fi
      owners+=("$candidate")
    done < <(printf '%s\n' "$listeners" | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u)
    if ((${#owners[@]} == 0)); then
      echo "无法识别端口 $port 的占用进程，请使用有权限的账号运行：" >&2
      printf '%s\n' "$listeners" >&2
      return 1
    fi
  fi
  if running; then stop_pid "$pid" || return 1; fi
  for candidate in ${owners[@]+"${owners[@]}"}; do stop_pid "$candidate" || return 1; done
  rm -f "$pid_file"
}

case "$action" in
  migrate|memory-backfill)
    [[ -f "$binary" ]] || { echo "缺少程序：$binary" >&2; exit 1; }
    chmod +x "$binary"
    cd "$api_dir"
    if [[ "$action" == migrate ]]; then
      exec "$binary" -migrate "$@"
    fi
    exec "$binary" -memory-backfill -memory-config "${JETAIME_MEMORY_CONFIG:-$api_dir/memory.json}" "$@"
    ;;
  start|restart)
    [[ -f "$binary" ]] || { echo "缺少程序：$binary" >&2; exit 1; }
    if [[ "$action" == restart ]]; then restart_api "$@"; fi
    mkdir -p "$runtime_dir"
    if running; then
      echo "API 已运行，PID：$pid"
      exit 0
    fi
    [[ -f "$binary" ]] || { echo "缺少程序：$binary" >&2; exit 1; }
    chmod +x "$binary"
    cd "$api_dir"
    model_args=()
    if [[ -n "${JETAIME_MODEL_CONFIG:-}" || -f "$api_dir/models.json" ]]; then
      model_args+=(-model-config "${JETAIME_MODEL_CONFIG:-$api_dir/models.json}")
    fi
    model_args+=(-memory-config "${JETAIME_MEMORY_CONFIG:-$api_dir/memory.json}")
    nohup "$binary" -addr "${API_ADDR:-0.0.0.0:8081}" "${model_args[@]}" "$@" >> "$log_file" 2>&1 < /dev/null &
    pid=$!
    echo "$pid" > "$pid_file"
    sleep 1
    if ! running; then
      rm -f "$pid_file"
      echo "启动失败，请查看：$log_file" >&2
      exit 1
    fi
    echo "API 已后台启动，PID：$pid"
    echo "日志：$log_file"
    echo "默认健康检查：curl http://127.0.0.1:8081/test/healthz"
    ;;
  stop)
    if running; then
      stop_pid "$pid"
    fi
    rm -f "$pid_file"
    echo "API 已停止"
    ;;
  status)
    if running; then echo "API 运行中，PID：$pid"; else echo "API 未运行"; exit 1; fi
    ;;
  *) echo "用法：bash start.sh {restart [API 参数...]|start [API 参数...]|migrate|memory-backfill|stop|status}（默认 restart）" >&2; exit 2 ;;
esac
