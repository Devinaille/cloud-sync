# cloud-sync

监听 `media/` + `ani-rss/` 新视频 → 调 OpenList API 上传到 139yun → 写 `.sync_status/` → 72h 后清理本地源视频。

完整架构见 [`../ARCHITECTURE.md`](../ARCHITECTURE.md)。组件实现规格见 [`../docs/superpowers/specs/2026-09-14-cloudsync-design.md`](../docs/superpowers/specs/2026-09-14-cloudsync-design.md)。

---

## 配置（推荐：YAML 文件）

`main()` 在启动时调 `Load("/config/cloud-sync.yaml")`。流程：

1. 如果 `/config/cloud-sync.yaml` **存在**——把里面出现的字段注入进程 env，再走原有 env 解析。文件里**没写**的字段继续走 env，env 也没设的报 required 错误。
2. 如果 `/config/cloud-sync.yaml` **不存在**——退回纯 env 模式（向后兼容老的 docker-compose 部署）。

字段名与 env 名一一对应（小写下划线 ↔ 大写下划线）。`docker-compose.yml` 默认把仓库里的 `cloud-sync/config.example.yaml` 挂到 `/config/cloud-sync.yaml`，**复制并按需修改即可**：

```bash
cp cloud-sync/config.example.yaml /your/deploy/path/cloud-sync/config.yaml
# 编辑
vim /your/deploy/path/cloud-sync/config.yaml
docker compose restart cloud-sync
```

完整字段说明 + 注释见 `cloud-sync/config.example.yaml`。

### 优先级

| 来源 | 优先级 |
|---|---|
| `/config/cloud-sync.yaml` 中**出现**的字段 | 最高 |
| `docker-compose.yml` `environment:` | 次之 |
| `os.Setenv` 其它来源 | 同 env |
| 文件 / env 都没设 | required 字段报错 |

**重要**：配置文件用 `yaml.NewDecoder` 的 `KnownFields(true)` 解析，所以字段名拼写错误（典型如 `uploadconcurrency` 漏了下划线）会立刻报错而不是静默回退 default——避免"我以为我改过了"。

---

## 本地测试（端到端，不需要真的 OpenList / 139yun）

两套 smoke 脚本，区别在配置来源：

- `../scripts/smoke.sh` —— 纯 env 模式（演示传统部署）
- `../scripts/smoke-config.sh` —— YAML 配置模式（演示推荐部署）

两者都重建二进制、启动 mock、投放 120 MiB 测试文件、验证 `pipeline: synced` 日志、云盘副本落地、状态记录、cleanup dry-run。整段约 15–20 秒。

### 0. 前置

- Go 1.22+
- `python3`（3.7+）
- 至少 **120 MB** 空闲磁盘（`MinFileSize` 是硬编码的 100 MB 阈值，测试文件必须超过）

### 推荐：跑 smoke-config.sh

```bash
cd cloud-sync
../scripts/smoke-config.sh
```

预期末尾：`OK`。日志片段：
```
mock-openlist: /local_media->/tmp/cloud-sync-smoke-config  ...
--- synced log line ---
{"time":"...","level":"INFO","msg":"pipeline: synced","key":"Movies/SmokeTest.mkv","task_id":"..."}
--- cloud copy ---
-rw-r--r-- ... 120M ... cloud/media/Movies/SmokeTest.mkv
--- state record ---
{ "src_path": "...", "status": "synced", ... }
--- cleanup dry-run line ---
{"msg":"[DRY-RUN] would delete","path":".../media/Movies/SmokeTest.mkv"}
OK
```

### 手工步骤（env 模式，step-by-step）

跟 `../scripts/smoke.sh` 等价，适合理解细节。

#### 1. 准备临时工作区

```bash
WS=/tmp/cloud-sync-smoke
rm -rf "$WS"
mkdir -p "$WS"/{media/Movies,ani-rss,cloud,.sync_status}
```

#### 2. 编译二进制

```bash
cd cloud-sync
CGO_ENABLED=0 go build -o /tmp/cloud-sync-bin .
/tmp/cloud-sync-bin 2>&1 | head -1   # 应: "config error: required env OPENLIST_URL is empty"
```

#### 3. 启动本地 OpenList mock

`../scripts/mock-openlist.py` 实现 cloud-sync 用到的三个 endpoint（`/api/fs/list`、`/api/fs/copy`、`/api/admin/task/<id>/done`），把"上传"拷贝到本地目录。

```bash
export OPENLIST_LOCAL_SRC_DIR="$WS"
export OPENLIST_LOCAL_DST_DIR="$WS/cloud"
python3 ../scripts/mock-openlist.py
```

另开一个终端 sanity check：
```bash
curl -sX POST -H 'Content-Type: application/json' \
  -d '{"path":"/","page":1,"per_page":1}' \
  http://127.0.0.1:5244/api/fs/list
# 预期: {"code": 200, "message": "ok", "data": {"content": []}}
```

#### 4. 启动 cloud-sync（env 模式）

```bash
export OPENLIST_URL=http://127.0.0.1:5244
export OPENLIST_TOKEN=local-test-token
export OPENLIST_SRC_STORAGE=/local_media
export OPENLIST_DST_STORAGE=/139yun_media

export WATCH_DIRS="$WS/media,$WS/ani-rss"
export SYNC_STATUS_DIR="$WS/.sync_status"
export ALLOWED_SOURCE_PREFIXES="$WS/media,$WS/ani-rss"

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

预期启动日志：
```json
{"time":"...","level":"INFO","msg":"cloud-sync starting", ...}
{"time":"...","level":"INFO","msg":"openlist ping ok"}
{"time":"...","level":"INFO","msg":"startup scan done","files":0}
```

#### 5. 投放测试视频

`MinFileSize` 硬编码 100 MB，所以文件必须 ≥ 100 MB：

```bash
dd if=/dev/urandom of="$WS/media/Movies/SmokeTest.mkv" bs=1M count=120 status=none
ls -lh "$WS/media/Movies/SmokeTest.mkv"
```

#### 6. 观察流水线

大约 5–10 秒（stabilize 窗口 + Copy + poll）后：

```bash
# 1. cloud-sync 日志（关键行）
grep '"msg":"pipeline: synced"' "$WS/cloud-sync.log"

# 2. mock 那边落地的"云盘副本"（pipeline 在 src/dst name 上加 `media/` 前缀）
ls -lh "$WS/cloud/media/Movies/SmokeTest.mkv"

# 3. 状态文件
cat "$(find "$WS/.sync_status" -name 'SmokeTest.mkv.json' | head -1)"
# 关键字段: status=synced, cloud_path=/139yun_media/media/Movies/SmokeTest.mkv
```

#### 7. 验证 cleanup（dry-run）

启动时 cleanup tick 跑一次，但因为刚 sync 的记录 `cleanup_at` 是 72 小时后，第一次 tick 看不到任何候选。要立刻看到 `[DRY-RUN] would delete`，把记录的 `cleanup_at` 改成过去时间，再重启 cloud-sync：

```bash
kill %1 2>/dev/null; wait
STATE=$(find "$WS/.sync_status" -name 'SmokeTest.mkv.json' | head -1)
python3 -c "
import json, pathlib
p = pathlib.Path('$STATE'); r = json.loads(p.read_text())
r['cleanup_at'] = '2020-01-01T00:00:00Z'
p.write_text(json.dumps(r, indent=2))
"
/tmp/cloud-sync-bin 2>&1 | tee "$WS/cloud-sync.log.2"
```

应看到：
```json
{"msg":"[DRY-RUN] would delete","path":".../Movies/SmokeTest.mkv"}
```

源文件仍在（dry-run 不删）。要真删：把 `CLEANUP_DRY_RUN=false` 重启，再做同样的 `cleanup_at` 改写。

#### 8. 清理

```bash
kill %1 2>/dev/null   # cloud-sync
# Ctrl-C mock
rm -rf "$WS"
```

---

## Build

```bash
# Local
go build -o cloud-sync .

# Docker (distroless/static, non-root, <30 MiB)
docker build -t cloud-sync:1.0.0 .
```

## Run（部署到 TrueNAS）

`docker-compose.yml` 默认把 `cloud-sync/config.example.yaml` 挂到容器的 `/config/cloud-sync.yaml`，**首次部署先复制一份到本地再编辑**：

```bash
cp cloud-sync/config.example.yaml cloud-sync/config.yaml
vim cloud-sync/config.yaml
# 改 openlist_token 等
docker compose up -d cloud-sync
docker compose logs -f cloud-sync
```

也可以完全退回纯 env 模式：删掉 `/config/cloud-sync.yaml`（或注释 compose 里的挂载），env 走通。

## Test

```bash
go test ./...
```

## Env vars / config keys

YAML 与 env 同名（小写 ↔ 大写）。下表是底层 env 名：

| 变量 | 用途 |
|---|---|
| `OPENLIST_URL`, `OPENLIST_TOKEN` | OpenList HTTP endpoint + admin token |
| `OPENLIST_SRC_STORAGE` | OpenList 内 local 存储挂载路径（`/local_media`） |
| `OPENLIST_DST_STORAGE` | OpenList 内 139yun 存储挂载路径（`/139yun_media`） |
| `WATCH_DIRS` | 逗号分隔的**绝对**本地路径列表（fsnotify 递归监听每个） |
| `SYNC_STATUS_DIR` | `.sync_status/` 绝对路径 |
| `ALLOWED_SOURCE_PREFIXES` | 逗号分隔，cleanup 防御性白名单（必须包含两个 WATCH_DIR） |
| `CLEANUP_AFTER_HOURS` (默认 72) | 清理延迟 |
| `CLEANUP_DRY_RUN` (默认 false) | true=只打日志不删，false=真删 |
| `UPLOAD_CONCURRENCY` (默认 2) | 上传并发上限 |
| `STABILIZE_WAIT_SECONDS` (默认 30) | 等文件 size+mtime 稳定多久才上传 |
| `POLL_INTERVAL_SECONDS` (默认 3) | OpenList 任务状态轮询间隔 |
| `TASK_TIMEOUT_SECONDS` (默认 1800) | 单次任务超时 |
| `LOG_LEVEL` (debug/info/warn/error) | 日志等级 |
| `LOG_FILE` (空=stdout) | 日志文件路径；空表示 stdout（由 docker compose 收集） |

**未通过 env / config 暴露**：`MinFileSize` 硬编码 100 MB（编译时常量）。要调，改源码 `cloud-sync/config.go:minFileSizeBytes` 后重 build。

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
| `config error: required env ...` | env 没设全且 `/config/cloud-sync.yaml` 不存在。 |
| `config: yaml parse ...` | 配置文件语法错。`python3 -c "import yaml; yaml.safe_load(open('path'))"` 验证。 |
| `unknown field ... in YAML` | 字段名拼错（如 `uploadconcurrency` 漏下划线）。文件用 KnownFields(true) 严格校验。 |
| `openlist ping failed` | OpenList URL 错、token 无效、或 `/api/fs/list` 路由不通。`curl -X POST "$OPENLIST_URL/api/fs/list"` 验证。 |
| 文件落地但 cloud 没副本 | mock 没启或 `OPENLIST_LOCAL_SRC_DIR` / `OPENLIST_LOCAL_DST_DIR` 不对。看 mock stdout。 |
| 完全没反应 | 文件 < 100 MB（MinFileSize 是硬编码）。`ls -lh` 看一下。 |
| `.sync_status` 不出现 | watch 路径不在 `WATCH_*_DIR` 内，或 `ALLOWED_SOURCE_PREFIXES` 把它挡掉了。`LOG_LEVEL=debug` 看 `pipeline: path not whitelisted` / `too small`。 |
| cleanup 没日志 | `CLEANUP_DRY_RUN` 默认 false 时真删，但只在 `CleanupAt < now` 才动。改 `cleanup_at` 过去时间或调小 `CLEANUP_AFTER_HOURS`。 |
| 文件改了但容器还是旧值 | 配置文件是挂载的（:ro），改完主机文件后 `docker compose restart cloud-sync`，不是 `up`。 |
