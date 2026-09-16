#!/usr/bin/env bash
# Stop the cloud-sync local dev playground started by run.sh.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
RUN="$HERE/run"

for name in cloud-sync mock; do
    pidfile="$RUN/$name.pid"
    if [ -f "$pidfile" ]; then
        pid="$(cat "$pidfile")"
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            echo "[local-dev] stopped $name (pid $pid)"
        fi
        rm -f "$pidfile"
    fi
done

# belt-and-braces: reap anything left over from this playground
pkill -f "$HERE/bin/cloud-sync" 2>/dev/null || true
pkill -f "mock-openlist.py" 2>/dev/null || true
echo "[local-dev] down."
