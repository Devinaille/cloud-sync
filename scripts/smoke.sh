#!/usr/bin/env bash
# End-to-end local smoke test for cloud-sync.
#
# Boots:
#   - scripts/mock-openlist.py on 127.0.0.1:5244
#   - /tmp/cloud-sync-bin (rebuilt from current source)
#
# Drops a 120 MiB random "video" into $WS/media/Movies/, then asserts:
#   - cloud-sync logs "pipeline: synced"
#   - mock landed a copy under $WS/cloud/Movies/
#   - .sync_status/<date>/Movies/SmokeTest.mkv.json exists
#   - cleanup tick logged "[DRY-RUN] would delete"
#   - mock captured a request with names[] (not src_name/dst_name) + skip_existing:true
#
# Cleans up $WS and kills the mock on exit.

set -euo pipefail

WS=/tmp/cloud-sync-smoke
BIN=/tmp/cloud-sync-bin
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$SCRIPT_DIR/../cloud-sync"
CGO_ENABLED=0 go build -o "$BIN" .

rm -rf "$WS"
mkdir -p "$WS"/{media/Movies,ani-rss,cloud,.sync_status}

export OPENLIST_LOCAL_SRC_DIR="$WS"
export OPENLIST_LOCAL_DST_DIR="$WS/cloud"
export MOCK_LOG_PATH="/tmp/cloud-sync-smoke-mock.log"
rm -f "$MOCK_LOG_PATH"   # assertions must read only this run's requests
python3 "$SCRIPT_DIR/mock-openlist.py" &
MOCK_PID=$!

cleanup() {
    local rc=$?
    kill "${CS_PID:-}" 2>/dev/null || true
    wait "${CS_PID:-}" 2>/dev/null || true
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
    rm -rf "$WS"
    exit $rc
}
trap cleanup EXIT INT TERM

sleep 1

export OPENLIST_URL=http://127.0.0.1:5244
export OPENLIST_TOKEN=test
export OPENLIST_SRC_STORAGE=/local_media
export OPENLIST_DST_STORAGE=/139yun_media
export WATCH_DIRS="$WS/media,$WS/ani-rss"
export SYNC_STATUS_DIR="$WS/.sync_status"
export ALLOWED_SOURCE_PREFIXES="$WS/media,$WS/ani-rss"
export CLEANUP_AFTER_HOURS=72
export CLEANUP_DRY_RUN=true
export UPLOAD_CONCURRENCY=2
export STABILIZE_WAIT_SECONDS=5
export POLL_INTERVAL_SECONDS=1
export TASK_TIMEOUT_SECONDS=60
export LOG_LEVEL=debug
export LOG_FILE=

"$BIN" > "$WS/cloud-sync.log" 2>&1 &
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
# Rewrite the synced record's cleanup_at to the past, then bounce cloud-sync
# so the startup cleanup tick picks it up (hourly ticker would take too long
# for a smoke test).
STATE=$(find "$WS/.sync_status" -name 'SmokeTest.mkv.json' | head -1)
kill "$CS_PID" 2>/dev/null || true
wait "$CS_PID" 2>/dev/null || true
python3 -c "
import json, datetime, pathlib
p = pathlib.Path('$STATE')
rec = json.loads(p.read_text())
rec['cleanup_at'] = '2020-01-01T00:00:00Z'
p.write_text(json.dumps(rec, indent=2))
"

"$BIN" > "$WS/cloud-sync.log.2" 2>&1 &
CS_PID=$!
sleep 2

grep '\[DRY-RUN\]' "$WS/cloud-sync.log.2" || {
    echo "FAIL: no [DRY-RUN] cleanup log after restart"
    cat "$WS/cloud-sync.log.2"
    exit 1
}

# Source file should still exist (dry-run does not delete)
ls -lh "$WS/media/Movies/SmokeTest.mkv"

echo "--- mock captured OpenList v4 API request ---"
MLOG=/tmp/cloud-sync-smoke-mock.log
grep -qF '"names":["SmokeTest.mkv"]' "$MLOG" \
    || { echo "FAIL: mock request missing names[]"; cat "$MLOG"; exit 1; }
grep -qF '"skip_existing":true' "$MLOG" \
    || { echo "FAIL: mock request missing skip_existing:true"; cat "$MLOG"; exit 1; }
grep -qF '"src_name"' "$MLOG" \
    && { echo "FAIL: legacy 'src_name' field appeared"; cat "$MLOG"; exit 1; }
grep -qF '"dst_name"' "$MLOG" \
    && { echo "FAIL: legacy 'dst_name' field appeared"; cat "$MLOG"; exit 1; }

kill "$CS_PID" 2>/dev/null || true
wait "$CS_PID" 2>/dev/null || true
echo OK
