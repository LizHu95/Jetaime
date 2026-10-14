#!/usr/bin/env bash
set -euo pipefail

# Place this script beside the packaged Linux binaries.
api_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
case "$(uname -m)" in
  x86_64) binary="$api_dir/api-linux-amd64" ;;
  aarch64|arm64) binary="$api_dir/api-linux-arm64" ;;
  *) echo "不支持的服务器架构：$(uname -m)" >&2; exit 1 ;;
esac
runtime_dir="$api_dir/runtime"
pid_file="$runtime_dir/api.pid"
log_file="$runtime_dir/api.log"
action=${1:-start}
if (($#)); then shift; fi

running() {
  [[ -f "$pid_file" ]] || return 1
  read -r pid < "$pid_file"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null && [[ "$(readlink "/proc/$pid/exe")" == "$binary" ]]
}

case "$action" in
  start)
    mkdir -p "$runtime_dir"
    if running; then
      echo "API 已运行，PID：$pid"
      exit 0
    fi
    [[ -f "$binary" ]] || { echo "缺少程序：$binary" >&2; exit 1; }
    chmod +x "$binary"
    cd "$api_dir"
    nohup "$binary" -addr "${API_ADDR:-0.0.0.0:8081}" "$@" >> "$log_file" 2>&1 < /dev/null &
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
      kill -TERM "$pid"
      for ((i=0; i<10; i++)); do
        if ! running; then break; fi
        sleep 1
      done
      if running; then
        echo "API 尚未退出，请查看：$log_file" >&2
        exit 1
      fi
    fi
    rm -f "$pid_file"
    echo "API 已停止"
    ;;
  status)
    if running; then echo "API 运行中，PID：$pid"; else echo "API 未运行"; exit 1; fi
    ;;
  *) echo "用法：bash start.sh {start [API 参数...]|stop|status}" >&2; exit 2 ;;
esac
