#!/usr/bin/env bash
# End-to-end local smoke test for cloud-sync in **YAML config mode**.
#
# Same as smoke.sh, but instead of exporting every variable into the
# environment, we write a config.example.yaml-style file to a temp dir
# and pass its path as the first argument to the binary. This is exactly
# what main() does in production (it calls Load("/config/cloud-sync.yaml")).
#
# The config file overrides a couple of env values to prove the
# "file wins" priority semantics.

set -euo pipefail

WS=/tmp/cloud-sync-smoke-config
BIN=/tmp/cloud-sync-bin
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$SCRIPT_DIR/../cloud-sync"
CGO_ENABLED=0 go build -o "$BIN" .

rm -rf "$WS"
mkdir -p "$WS"/{media/Movies,ani-rss,cloud,.sync_status}

export OPENLIST_LOCAL_SRC_DIR="$WS"
export OPENLIST_LOCAL_DST_DIR="$WS/cloud"
python3 "$SCRIPT_DIR/mock-openlist.py" &
MOCK_PID=$!

cleanup() {
    local rc=$?
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
    rm -rf "$WS"
    exit $rc
}
trap cleanup EXIT INT TERM

sleep 1

# --- env that the YAML file does NOT override ---
export OPENLIST_URL=http://127.0.0.1:5244
export OPENLIST_TOKEN=test
export OPENLIST_SRC_STORAGE=/local_media
export OPENLIST_DST_STORAGE=/139yun_media
export WATCH_MEDIA_DIR="$WS/media"
export WATCH_ANIRSS_DIR="$WS/ani-rss"
export SYNC_STATUS_DIR="$WS/.sync_status"
export ALLOWED_SOURCE_PREFIXES="$WS/media,$WS/ani-rss"
export CLEANUP_AFTER_HOURS=72
export LOG_FILE=

# --- env that the YAML WILL override (to prove file wins) ---
export UPLOAD_CONCURRENCY=99       # YAML overrides this with 2
export LOG_LEVEL=warn              # YAML overrides this with debug
export STABILIZE_WAIT_SECONDS=999  # YAML overrides this with 5

# --- YAML config file ---
CFG="$WS/cloud-sync.yaml"
cat > "$CFG" <<EOF
upload_concurrency: 2
log_level: debug
stabilize_wait_seconds: 5
cleanup_dry_run: true
poll_interval_seconds: 1
task_timeout_seconds: 60
EOF

# /config/cloud-sync.yaml is the production default; we pass an explicit
# path here so the test works outside a container.
"$BIN" "$CFG" > "$WS/cloud-sync.log" 2>&1 &
CS_PID=$!
sleep 2

dd if=/dev/urandom of="$WS/media/Movies/SmokeTest.mkv" bs=1M count=120 status=none

# stabilize (5s) + copy + poll should finish well within this.
sleep 10

echo "--- synced log line ---"
grep '"msg":"pipeline: synced"' "$WS/cloud-sync.log" || {
    echo "FAIL: no pipeline: synced log"
    echo "--- full log ---"
    cat "$WS/cloud-sync.log"
    exit 1
}

echo "--- cloud copy ---"
ls -lh "$WS/cloud/media/Movies/SmokeTest.mkv"

echo "--- state record ---"
STATE=$(find "$WS/.sync_status" -name 'SmokeTest.mkv.json' | head -1)
echo "$STATE"
cat "$STATE"

echo "--- cleanup dry-run line ---"
STATE=$(find "$WS/.sync_status" -name 'SmokeTest.mkv.json' | head -1)
kill "$CS_PID" 2>/dev/null || true
wait "$CS_PID" 2>/dev/null || true
python3 -c "
import json, pathlib
p = pathlib.Path('$STATE')
rec = json.loads(p.read_text())
rec['cleanup_at'] = '2020-01-01T00:00:00Z'
p.write_text(json.dumps(rec, indent=2))
"

"$BIN" "$CFG" > "$WS/cloud-sync.log.2" 2>&1 &
CS_PID=$!
sleep 2

grep '\[DRY-RUN\]' "$WS/cloud-sync.log.2" || {
    echo "FAIL: no [DRY-RUN] cleanup log after restart"
    cat "$WS/cloud-sync.log.2"
    exit 1
}

# Source file should still exist (dry-run does not delete).
ls -lh "$WS/media/Movies/SmokeTest.mkv"

kill "$CS_PID" 2>/dev/null || true
wait "$CS_PID" 2>/dev/null || true
echo OK
