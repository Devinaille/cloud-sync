#!/usr/bin/env bash
# Start the cloud-sync local dev playground: build, OpenList mock, cloud-sync + Web UI.
#
# Usage:
#   ./local-dev/run.sh              # start (ports 8099 UI, 5244 mock)
#   UI_PORT=9099 MOCK_PORT=6244 ./local-dev/run.sh
#
# Idempotent: re-running starts only what is not already running.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
DATA="$HERE/data"
RUN="$HERE/run"
LOGS="$HERE/logs"
BIN="$HERE/bin/cloud-sync"
CFG="$HERE/config.yaml"
UI_PORT="${UI_PORT:-8099}"
MOCK_PORT="${MOCK_PORT:-5244}"

mkdir -p "$DATA"/{media,ani-rss,cloud,.sync_status} "$RUN" "$LOGS" "$HERE/bin"

# 1. build the binary
echo "[local-dev] building cloud-sync..."
( cd "$ROOT/cloud-sync" && CGO_ENABLED=0 go build -o "$BIN" . )

# 2. generate config.yaml from the template on first run
if [ ! -f "$CFG" ]; then
    echo "[local-dev] generating $CFG"
    sed -e "s#__BASE__#$DATA#g" \
        -e "s#__MOCK_PORT__#$MOCK_PORT#g" \
        -e "s#__UI_PORT__#$UI_PORT#g" \
        "$HERE/config.example.yaml" > "$CFG"
fi

running() {  # running <pidfile>
    [ -f "$1" ] && kill -0 "$(cat "$1")" 2>/dev/null
}

# 3. OpenList mock
if running "$RUN/mock.pid"; then
    echo "[local-dev] OpenList mock already running (pid $(cat "$RUN/mock.pid"))"
else
    echo "[local-dev] starting OpenList mock on :$MOCK_PORT"
    OPENLIST_LOCAL_SRC_DIR="$DATA" \
    OPENLIST_LOCAL_DST_DIR="$DATA/cloud" \
    MOCK_LOG_PATH="$LOGS/mock-requests.log" \
    MOCK_OPENLIST_PORT="$MOCK_PORT" \
    nohup python3 "$ROOT/scripts/mock-openlist.py" > "$LOGS/mock.log" 2>&1 &
    echo $! > "$RUN/mock.pid"
fi

# wait for the mock to accept requests
for _ in $(seq 1 40); do
    if curl -sf -o /dev/null -X POST -H 'Content-Type: application/json' \
        -d '{}' "http://127.0.0.1:$MOCK_PORT/api/fs/list"; then
        break
    fi
    sleep 0.25
done

# 4. cloud-sync
if running "$RUN/cloud-sync.pid"; then
    echo "[local-dev] cloud-sync already running (pid $(cat "$RUN/cloud-sync.pid"))"
else
    echo "[local-dev] starting cloud-sync (UI on :$UI_PORT)"
    nohup "$BIN" "$CFG" > "$LOGS/cloud-sync.log" 2>&1 &
    echo $! > "$RUN/cloud-sync.pid"
fi

sleep 1
cat <<EOF

[local-dev] up.

  Web UI       http://127.0.0.1:$UI_PORT/
  OpenList     http://127.0.0.1:$MOCK_PORT/   (mock)
  Watch dirs   $DATA/media  |  $DATA/ani-rss
  Config       $CFG   (also editable in the UI Config tab)
  Logs         $LOGS/cloud-sync.log  |  $LOGS/mock.log

Try it:
  # create a >100 MB fake video; watcher picks it up, "uploads" to the mock cloud
  mkdir -p "$DATA/media/Movies"
  dd if=/dev/urandom of="$DATA/media/Movies/Demo.mkv" bs=1M count=120 status=none

  # watch it happen:
  tail -f "$LOGS/cloud-sync.log"
  ls -lh "$DATA/cloud/media/Movies/"          # the "cloud" copy
  ls "$DATA/.sync_status"/*/Movies/           # the state record

Stop with:  $HERE/stop.sh
EOF
