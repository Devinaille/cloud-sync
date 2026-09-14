# cloud-sync

监听 `media/` + `ani-rss/` 新视频 → 调 OpenList API 上传到 139yun → 写 `.sync_status/` → 72h 后清理本地源视频。

完整架构见 [`../ARCHITECTURE.md`](../ARCHITECTURE.md)。组件实现规格见 [`../docs/superpowers/specs/2026-09-14-cloudsync-design.md`](../docs/superpowers/specs/2026-09-14-cloudsync-design.md)。

---

## 本地测试（端到端，不需要真的 OpenList / 139yun）

最快验证整个上传 + 状态机 + cleanup 流水线的方式。整段大约 5 分钟。

### 0. 前置

- Go 1.22+
- `python3`（3.7+）
- `curl`（可选，仅 sanity check 用）
- 至少 **120 MB** 空闲磁盘（`MinFileSize` 是硬编码的 100 MB 阈值，测试文件必须超过）

### 1. 准备临时工作区

```bash
WS=/tmp/cloud-sync-smoke
rm -rf "$WS"
mkdir -p "$WS"/{media/Movies,ani-rss,cloud,.sync_status}
ls "$WS"
# 应看到: ani-rss  cloud  media  .sync_status
```

### 2. 编译二进制

```bash
cd "$(git rev-parse --show-toplevel)/cloud-sync"
CGO_ENABLED=0 go build -o /tmp/cloud-sync-bin .
/tmp/cloud-sync-bin --help 2>&1 | head -1   # 没 --help；不存在的 env 会打印 config error
```

预期：`config error: required env OPENLIST_URL is empty` 并 exit code 2 → 说明二进制能跑起来。

### 3. 启动本地 OpenList mock

`scripts/mock-openlist.py` 实现了 cloud-sync 用到的三个 endpoint（`/api/fs/list`、`/api/fs/copy`、`/api/admin/task/<id>/done`），把"上传"拷贝到本地目录。

```bash
export OPENLIST_LOCAL_SRC_DIR="$WS"
export OPENLIST_LOCAL_DST_DIR="$WS/cloud"
python3 "$(git rev-parse --show-toplevel)/cloud-sync/scripts/mock-openlist.py"
```

预期 stdout：

```
mock-openlist: /local_media->/tmp/cloud-sync-smoke  /139yun_media->/tmp/cloud-sync-smoke/cloud  port=5244
```

另起一个终端，先 sanity check（可选）：

```bash
curl -sX POST -H 'Content-Type: application/json' \
  -d '{"path":"/","page":1,"per_page":1}' \
  http://127.0.0.1:5244/api/fs/list
# 预期: {"code": 200, "message": "ok", "data": {"content": []}}
```

### 4. 启动 cloud-sync

```bash
export OPENLIST_URL=http://127.0.0.1:5244
export OPENLIST_TOKEN=local-test-token
export OPENLIST_SRC_STORAGE=/local_media
export OPENLIST_DST_STORAGE=/139yun_media

export WATCH_MEDIA_DIR="$WS/media"
export WATCH_ANIRSS_DIR="$WS/ani-rss"
export SYNC_STATUS_DIR="$WS/.sync_status"

export ALLOWED_SOURCE_PREFIXES="$WS/media,$WS/ani-rss"
export REQUIRE_NFO_FOR_MEDIA=false       # 本地测试跳过 NFO 校验
export REQUIRE_NFO_FOR_ANIRSS=false

export CLEANUP_AFTER_HOURS=72
export CLEANUP_DRY_RUN=true              # 第一次跑必须 true
export UPLOAD_CONCURRENCY=2
export STABILIZE_WAIT_SECONDS=5          # 默认 30 太慢，测试用 5 秒
export POLL_INTERVAL_SECONDS=1
export TASK_TIMEOUT_SECONDS=60

export LOG_LEVEL=debug
export LOG_FILE=                          # 留空 = stdout

/tmp/cloud-sync-bin 2>&1 | tee "$WS/cloud-sync.log"
```

预期启动日志（注意每条都是 JSON）：

```json
{"time":"...","level":"INFO","msg":"cloud-sync starting", ...}
{"time":"...","level":"INFO","msg":"openlist ping ok"}
{"time":"...","level":"INFO","msg":"startup scan done","files":0}
```

### 5. 投放测试视频

MinFileSize 是硬编码 100 MB，所以文件必须 ≥ 100 MB。`dd` 从 `/dev/urandom` 拉：

```bash
dd if=/dev/urandom of="$WS/media/Movies/SmokeTest.mkv" bs=1M count=120 status=none
ls -lh "$WS/media/Movies/SmokeTest.mkv"
```

### 6. 观察流水线

大约 5–10 秒（stabilize 窗口 + Copy + poll）后，应该看到：

```bash
# 1. cloud-sync 日志（一条关键行）
grep '"msg":"pipeline: synced"' "$WS/cloud-sync.log"

# 2. mock 那边落地的"云盘副本"（pipeline 在 src/dst name 上加 `media/` 前缀，所以落在 cloud 的子目录）
ls -lh "$WS/cloud/media/Movies/SmokeTest.mkv"

# 3. 状态文件
ls "$WS/.sync_status/"
# 形如: 2026-09-14/
cat "$(find "$WS/.sync_status" -name 'SmokeTest.mkv.json' | head -1)"
# 关键字段: status=synced, has_nfo=false, cloud_path=/139yun_media/media/Movies/SmokeTest.mkv
```

### 7. 验证 cleanup（dry-run）

启动时 cleanup tick 会跑一次；但因为刚 sync 的记录 `cleanup_at` 是 72 小时后，**第一次 tick 看不到任何候选**。要立刻看到 `[DRY-RUN] would delete`，把记录的 `cleanup_at` 改成过去时间，再重启 cloud-sync：

```bash
# Ctrl-C 当前 cloud-sync
kill %1 2>/dev/null; wait

# 把 synced 记录的 cleanup_at 改成 2020-01-01
STATE=$(find "$WS/.sync_status" -name 'SmokeTest.mkv.json' | head -1)
python3 -c "
import json, pathlib
p = pathlib.Path('$STATE'); r = json.loads(p.read_text())
r['cleanup_at'] = '2020-01-01T00:00:00Z'
p.write_text(json.dumps(r, indent=2))
"

# 重启 cloud-sync；启动时 cleanup tick 会立刻命中
/tmp/cloud-sync-bin 2>&1 | tee "$WS/cloud-sync.log.2"
```

应看到：

```json
{"msg":"[DRY-RUN] would delete","path":"/tmp/cloud-sync-smoke/media/Movies/SmokeTest.mkv"}
```

源文件仍在（dry-run 不删）。要真删：把 `CLEANUP_DRY_RUN=false` 重启，再做上面同样的 `cleanup_at` 改写。

### 8. 清理

```bash
# Ctrl-C cloud-sync（SIGINT 触发干净 shutdown）
kill %1 2>/dev/null
# mock 也 Ctrl-C
rm -rf "$WS"
```

---

## 验证脚本（一键跑完 0–7 步）

放在 `scripts/smoke.sh`（可选）：

```bash
#!/usr/bin/env bash
set -euo pipefail
WS=/tmp/cloud-sync-smoke
rm -rf "$WS"
mkdir -p "$WS"/{media/Movies,ani-rss,cloud,.sync_status}

cd "$(dirname "$0")/.."   # cloud-sync/
CGO_ENABLED=0 go build -o /tmp/cloud-sync-bin .

export OPENLIST_LOCAL_SRC_DIR="$WS"
export OPENLIST_LOCAL_DST_DIR="$WS/cloud"
python3 scripts/mock-openlist.py &
MOCK_PID=$!

trap 'kill $MOCK_PID 2>/dev/null; rm -rf "$WS"' EXIT

sleep 1
export OPENLIST_URL=http://127.0.0.1:5244
export OPENLIST_TOKEN=test
export OPENLIST_SRC_STORAGE=/local_media
export OPENLIST_DST_STORAGE=/139yun_media
export WATCH_MEDIA_DIR="$WS/media" WATCH_ANIRSS_DIR="$WS/ani-rss"
export SYNC_STATUS_DIR="$WS/.sync_status"
export ALLOWED_SOURCE_PREFIXES="$WS/media,$WS/ani-rss"
export REQUIRE_NFO_FOR_MEDIA=false REQUIRE_NFO_FOR_ANIRSS=false
export CLEANUP_AFTER_HOURS=72 CLEANUP_DRY_RUN=true
export UPLOAD_CONCURRENCY=2
export STABILIZE_WAIT_SECONDS=5 POLL_INTERVAL_SECONDS=1 TASK_TIMEOUT_SECONDS=60
export LOG_LEVEL=debug LOG_FILE=

/tmp/cloud-sync-bin > "$WS/cloud-sync.log" 2>&1 &
CS_PID=$!
sleep 2

dd if=/dev/urandom of="$WS/media/Movies/SmokeTest.mkv" bs=1M count=120 status=none
sleep 10

echo "--- synced log ---"
grep '"msg":"pipeline: synced"' "$WS/cloud-sync.log" || { echo FAIL; exit 1; }
echo "--- cloud copy ---"
ls -lh "$WS/cloud/Movies/SmokeTest.mkv"
echo "--- state ---"
cat "$WS/.sync_status"/*/Movies/SmokeTest.mkv.json
echo "--- dry-run cleanup ---"
grep '\[DRY-RUN\]' "$WS/cloud-sync.log"

kill $CS_PID
echo OK
```

`chmod +x scripts/smoke.sh && ./scripts/smoke.sh`。

---

## Build

```bash
# Local
go build -o cloud-sync .

# Docker (distroless/static, non-root, <30MB)
docker build -t cloud-sync:1.0.0 .
```

## Run（部署到 TrueNAS）

见 [`../docker-compose.yml`](../docker-compose.yml) 的 `cloud-sync` service。所有配置通过环境变量注入（模板：[`../.env.example`](../.env.example)）。

```bash
docker compose up -d cloud-sync
docker compose logs -f cloud-sync
```

## Test

```bash
go test ./...
```

## Env vars

详见 [`../.env.example`](../.env.example)。要点：

| 变量 | 用途 |
|---|---|
| `OPENLIST_URL`, `OPENLIST_TOKEN` | OpenList HTTP endpoint + admin token |
| `OPENLIST_SRC_STORAGE` | OpenList 内 local 存储挂载路径（`/local_media`） |
| `OPENLIST_DST_STORAGE` | OpenList 内 139yun 存储挂载路径（`/139yun_media`） |
| `WATCH_MEDIA_DIR`, `WATCH_ANIRSS_DIR` | 监听目录的**绝对**本地路径 |
| `SYNC_STATUS_DIR` | `.sync_status/` 绝对路径 |
| `ALLOWED_SOURCE_PREFIXES` | 逗号分隔，cleanup 防御性白名单 |
| `CLEANUP_AFTER_HOURS` (默认 72), `CLEANUP_DRY_RUN` (默认 false) | 清理策略 |
| `UPLOAD_CONCURRENCY` (默认 2), `STABILIZE_WAIT_SECONDS`, `POLL_INTERVAL_SECONDS`, `TASK_TIMEOUT_SECONDS` | 流水线调优 |
| `REQUIRE_NFO_FOR_MEDIA`, `REQUIRE_NFO_FOR_ANIRSS` | NFO 校验 |
| `LOG_LEVEL` (debug/info/warn/error), `LOG_FILE` (空=stdout) | 日志 |

**未通过 env 暴露**：`MinFileSize` 硬编码 100 MB（编译时常量）。要调，改源码 `cloud-sync/config.go:minFileSizeBytes` 后重 build。

## Operations

- **Dry-run cleanup**：上线后至少 24h 保持 `CLEANUP_DRY_RUN=true`，观察日志中 `[DRY-RUN] would delete` 的路径是否合理。
- **日志检索**：
  - `"msg":"pipeline: synced"` — 上传成功
  - `"msg":"cleanup"` 或 `"msg":"[DRY-RUN]"` — 清理动作
  - `"msg":"watcher error"` — fsnotify 问题
- **失败记录**：`.sync_status/FAILED/<date>/<rel>.json` 的 `error` 字段有原因。重启会自动重试（失败记录也阻塞 AlreadySynced——人工删除该文件可强制重试）。
- **强制重处理**：
  ```bash
  rm /mnt/basic/media/.sync_status/<date>/<rel>.json
  # 重启 cloud-sync 即可（StartupScan 会重新跑 process）
  ```

## 故障排查

| 现象 | 原因 |
|---|---|
| `config error: required env ...` | env 没设全。`/tmp/cloud-sync-bin` 单跑会列出第一个缺失的。 |
| `openlist ping failed` | OpenList URL 错、token 无效、或 `/api/fs/list` 路由不通。`curl -X POST "$OPENLIST_URL/api/fs/list"` 验证。 |
| 文件落地但 cloud 没副本 | mock 没启或 `OPENLIST_LOCAL_SRC_DIR` / `OPENLIST_LOCAL_DST_DIR` 不对。看 mock stdout。 |
| 完全没反应 | 文件 < 100 MB（MinFileSize 是硬编码 100 MB）。`ls -lh` 看一下。 |
| `.sync_status` 不出现 | watch 路径不在 `WATCH_*_DIR` 内，或 `ALLOWED_SOURCE_PREFIXES` 把它挡掉了。`LOG_LEVEL=debug` 看 `pipeline: path not whitelisted` / `too small`。 |
| cleanup 没日志 | `CLEANUP_DRY_RUN` 默认 false 时真删，但只在 `CleanupAt < now` 才动。改 `cleanup_at` 过去时间或调小 `CLEANUP_AFTER_HOURS`。 |
