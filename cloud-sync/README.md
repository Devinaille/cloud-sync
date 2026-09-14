# cloud-sync

监听 `media/` + `ani-rss/` 新视频 → 调 OpenList API 上传到 139yun → 写 `.sync_status/` → 72h 后清理本地源视频。

## Build

```bash
# Local
go build -o cloud-sync .

# Docker
docker build -t cloud-sync:1.0.0 .
```

## Run

```bash
# All config via env (see ../.env.example for the full list)
docker compose up cloud-sync
```

## Test

```bash
go test ./...
```

## Env vars

See `../.env.example` for the full list. Key ones:

- `OPENLIST_URL`, `OPENLIST_TOKEN` — OpenList HTTP endpoint
- `OPENLIST_SRC_STORAGE` — OpenList local storage mount path (e.g. `/local_media`)
- `OPENLIST_DST_STORAGE` — OpenList cloud storage mount path (e.g. `/139yun_media`)
- `WATCH_MEDIA_DIR`, `WATCH_ANIRSS_DIR` — absolute local paths to watch
- `SYNC_STATUS_DIR` — absolute path for `.sync_status/`
- `ALLOWED_SOURCE_PREFIXES` — comma-separated absolute path prefixes
- `CLEANUP_AFTER_HOURS` (default 72), `CLEANUP_DRY_RUN` (default false)
- `UPLOAD_CONCURRENCY` (default 2), `STABILIZE_WAIT_SECONDS`, `POLL_INTERVAL_SECONDS`, `TASK_TIMEOUT_SECONDS`
- `LOG_LEVEL` (debug|info|warn|error), `LOG_FILE` (empty = stdout)

## Operations

- **Dry-run cleanup**: leave `CLEANUP_DRY_RUN=true` for at least 24h after first deploy.
- **Logs**: tail `LOG_FILE` (defaults to stdout). Search for `"msg":"pipeline: synced"` and `"msg":"cleanup"`.
- **Failed uploads**: records appear under `.sync_status/FAILED/<date>/<rel>.json` with a non-empty `error` field.
- **Manual re-process**: stop the container, delete the offending `.sync_status/<date>/<rel>.json`, restart.
