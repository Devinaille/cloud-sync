# cloud-sync

监听 `media/` + `ani-rss/` 新视频 → 调 OpenList API 上传到 139yun → 写 `.sync_status/` → 72h 后清理本地源视频。

完整架构见 [`../ARCHITECTURE.md`](../ARCHITECTURE.md)。组件实现规格见 [`../docs/superpowers/specs/2026-09-14-cloudsync-design.md`](../docs/superpowers/specs/2026-09-14-cloudsync-design.md)。

---

## 配置（推荐：YAML 文件）

`main()` 在启动时调 `Load("/config/cloud-sync.yaml")`。流程：

1. 如果 `/config/cloud-sync.yaml` **存在**——把里面出现的字段注入进程 env，再走原有 env 解析。文件里**没写**的字段继续走 env，env 也没设的报 required 错误。
2. 如果 `/config/cloud-sync.yaml` **不存在**——退回纯 env 模式（向后兼容老的 docker-compose 部署）。

字段名与 env 名一一对应（小写下划线 ↔ 大写下划线）。`docker-compose.yml` 默认把仓库根的 `./config` 目录挂到 `/config`，配置放 `config/cloud-sync.yaml`，**复制并按需修改即可**：

```bash
mkdir -p /your/deploy/path/cloud-sync/config
cp config.example.yaml /your/deploy/path/cloud-sync/config/cloud-sync.yaml
# 编辑
vim /your/deploy/path/cloud-sync/config/cloud-sync.yaml
docker compose restart cloud-sync
```

> 挂目录而不是挂文件：bind-mount 一个尚不存在的文件时，Docker 会把它建成**目录**，导致容器读到目录而报错。

完整字段说明 + 注释见 `config.example.yaml`。

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

`../scripts/mock-openlist.py` 实现 cloud-sync 用到的三个 OpenList v4 endpoint（`POST /api/fs/list`、`POST /api/fs/copy`、`POST /api/admin/task/copy/info?tid=`），把"上传"拷贝到本地目录。

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

# 2. mock 那边落地的"云盘副本"（pipeline 在 src_dir/dst_dir 上追加 `media/` 子目录）
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
docker build -t cloud-sync:dev .
```

## Run（部署到 TrueNAS）

`docker-compose.yml` 默认**拉发布镜像**（`ghcr.io/devinaille/cloud-sync`，用 `CLOUD_SYNC_TAG` 选版本），并把仓库根的 `./config` 目录挂到容器的 `/config`（配置即 `config/cloud-sync.yaml`）。**首次部署先复制一份配置再编辑**：

```bash
mkdir -p config
cp config.example.yaml config/cloud-sync.yaml
vim config/cloud-sync.yaml   # 改 openlist_token、watch_dirs 等

CLOUD_SYNC_TAG=v0.1.0-rc2 docker compose up -d cloud-sync
docker compose logs -f cloud-sync

# 或从本地源码构建：
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

也可以完全退回纯 env 模式：删掉 `/config/cloud-sync.yaml`（或注释 compose 里的挂载），env 走通（注意 `TASKS_ENABLED`、`UI_LISTEN` 等也要在 env 里给）。

## Web UI

二进制内嵌一个单页 UI + JSON API，默认监听 `:8099`（`UI_LISTEN` / `ui_listen`；显式空字符串或 `-` 禁用）。浏览器打开 `http://<host>:8099/`。

三个 Tab：**Dashboard**（计数 / OpenList 连通性 / uptime / watch 目录 / 手动 cleanup）、**Files**（筛选 / 搜索 / 分页 / 勾选重试）、**Config**（默认表单化填写 + 「高级（YAML）」原文编辑 + 「重新生成 YAML」）。

支持中英文切换（右上角 `EN / 中文`），默认跟随浏览器语言，选择存于 `localStorage`。

**任务开关（默认关闭）**：`tasks_enabled` 默认 `false`——启动后不监听/不上传/不清理，只提供 UI。在 Dashboard 点「启动任务 / Start tasks」即为运行时开启（`POST /api/tasks`）；暂停会停止监听并安全收尾在途上传，状态目录保留，可随时浏览。**上传预检查**（Dashboard → Run pre-check，`POST /api/precheck`）是只读扫描：列出"若启动任务会上传哪些文件"，并对每个候选调用 OpenList `/api/fs/get` 检测**云盘是否已存在该文件**（每项带 `cloud: exists|missing|unknown`，并汇总 `cloud_exists/missing/unknown`；OpenList 不可达时标 `unknown` 且 `cloud_checked=false`），结果写入 `<sync_status_dir>/precheck.json`，暂停时也可用。暂停期间 `retry` / `cleanup/run` **仍然可用**：它们走 supervisor 的一次性执行路径（无 generation 时用当前配置/状态临时构造 pipeline 或 cleanup），不需要先启动任务；正在进行的一次性任务会随进程关闭被取消并等待。暂停时重启任务会把遗漏文件重新扫描补上。

JSON API：

| Method + Path | 用途 |
|---|---|
| `GET /api/status` | 计数、ping、uptime、生效配置（token **不返回**） |
| `GET /api/files?state=&q=&page=&page_size=` | 文件列表（`state` ∈ all/synced/failed/cleaned/unsynced） |
| `GET /api/config` | 返回配置文件原文（**含 token**） |
| `PUT /api/config` | 校验 → 原子写盘 → 热重载 |
| `POST /api/retry` | 按 keys 或 state 批量重试 |
| `POST /api/cleanup/run` | 立即跑一次 cleanup tick |

> **无鉴权**：任何能访问该端口的人都能读取配置（含 token）、修改配置、触发重试/清理。只在可信内网暴露；或只绑 `127.0.0.1` 再走 SSH 隧道 / 反向代理。

- **保存配置会写盘**：`PUT /api/config` 原子写回 `main()` 加载的配置文件（容器内 `/config/cloud-sync.yaml`）。该文件（或其挂载目录）必须**可写**，否则保存返回 500；compose 挂载用 `:rw`。
- **`ui_listen` 改动需重启**：监听地址在进程启动时绑定，热重载不会换端口。
- **裸机 / systemd**：`ProtectSystem=strict` 时把配置目录加进 `ReadWritePaths`，否则保存配置失败。

## Test

```bash
go test ./...
```

## Env vars / config keys

YAML 与 env 同名（小写 ↔ 大写）。下表是底层 env 名：

| 变量 | 用途 |
|---|---|
| `OPENLIST_URL`, `OPENLIST_TOKEN` | OpenList HTTP endpoint + admin token |
| `OPENLIST_SRC_STORAGE` | OpenList local 存储**根**（`/local_media`；pipeline 上传时自动追加 `media/` 子目录） |
| `OPENLIST_DST_STORAGE` | OpenList 139yun 存储**根**（`/139yun_media`；同上追加 `media/`） |
| `OPENLIST_OVERWRITE` (默认 false) | false=目标已存在时 `skip_existing`（保留云端文件）；true=`overwrite`（覆盖） |
| `UI_LISTEN` / `ui_listen` (默认 `:8099`) | Web UI + JSON API 监听地址；显式空字符串或 `-` 禁用 UI |
| `TASKS_ENABLED` / `tasks_enabled` (默认 **false**) | 任务开关：false 时不监听/不上传/不清理；可在 Web UI 运行时启停。仅**进程启动时**读取，热重载不改变运行状态 |
| `WATCH_DIRS` | 逗号分隔的**绝对**本地路径列表（fsnotify 递归监听每个） |
| `SYNC_STATUS_DIR` (默认 `/config/.sync_status`) | `.sync_status/` 绝对路径；缺省时放挂载的 `/config` 下，启动时自动创建 |
| `ALLOWED_SOURCE_PREFIXES` | 逗号分隔，cleanup 防御性白名单（必须包含每个 WATCH_DIR） |
| `CLEANUP_AFTER_HOURS` | 清理延迟（小时）；到期 = `synced_at + 当前值`，改小/改大对已同步文件实时生效 |
| `CLEANUP_DRY_RUN` | true=只打日志不删，false=真删（代码默认 false） |
| `CLEANUP_INTERVAL_SECONDS` | 清理 tick 间隔（秒），默认 3600，最小 300 |
| `UPLOAD_CONCURRENCY` | 上传并发上限 |
| `STABILIZE_WAIT_SECONDS` | 等文件 size+mtime 稳定多久才上传 |
| `POLL_INTERVAL_SECONDS` | OpenList 任务状态轮询间隔 |
| `TASK_TIMEOUT_SECONDS` | 单次请求超时（Copy 与每次任务查询；不限制单任务总时长） |
| `LOG_LEVEL` (debug/info/warn/error) | 日志等级 |
| `LOG_FILE` (空=stdout) | 日志文件路径；空表示 stdout（由 docker compose 收集） |

> **默认值说明**：除 `CLEANUP_DRY_RUN`（代码默认 false）、`LOG_FILE`（可选）、`UI_LISTEN`（代码默认 `:8099`）和 `CLEANUP_INTERVAL_SECONDS`（默认 3600，最小 300）外，上表数值项**没有代码默认值**——YAML/env 都没设会启动报错。`config.example.yaml` 给出的 72 / 2 / 30 / 3 / 1800 只是推荐示例。

**未通过 env / config 暴露**：`MinFileSize` 硬编码 100 MB（编译时常量）。要调，改源码 `cloud-sync/internal/config/config.go:MinFileSizeBytes` 后重 build。

## Operations

- **Dry-run cleanup**：上线后至少 24h 保持 `CLEANUP_DRY_RUN=true`，观察日志中 `[DRY-RUN] would delete` 的路径是否合理。
- **日志检索**：
  - `"msg":"pipeline: synced"` — 上传成功
  - `"msg":"cleanup"` 或 `"msg":"[DRY-RUN]"` — 清理动作
  - `"msg":"watcher error"` — fsnotify 问题
- **失败记录**：`.sync_status/FAILED/<date>/<rel>.json` 的 `error` 字段有原因。重启会自动重试（失败记录也阻塞 AlreadySynced——人工删除该文件可强制重试）。
- **强制重处理**：
  ```bash
  rm /config/.sync_status/<date>/<rel>.json
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
| `.sync_status` 不出现 | watch 路径不在 `WATCH_DIRS` 内，或 `ALLOWED_SOURCE_PREFIXES` 把它挡掉了。`LOG_LEVEL=debug` 看 `pipeline: path not whitelisted` / `too small`。 |
| cleanup 没日志 | `CLEANUP_DRY_RUN` 默认 false 时真删，但只在 `synced_at + CLEANUP_AFTER_HOURS <= now` 才动（实时计算；调小延迟会让已有文件在下一个 tick 到期）。 |
| 重传同一个文件，云端内容没变 | 默认 `OPENLIST_OVERWRITE=false` → `skip_existing=true`，目标已存在就跳过。要覆盖：把配置文件的 `openlist_overwrite` 改为 `true`（或删掉该 key 再用 env `OPENLIST_OVERWRITE=true`——**文件值优先于 env**）。 |
| `code=403 msg=file [X] exists` | 客户端同时发了 `overwrite=false` + `skip_existing=false`（不该发生；检查配置）。 |
| 文件改了但容器还是旧值 | 配置文件是挂载的（`:rw`），改完主机文件后 `docker compose restart cloud-sync`，不是 `up`（或用 Web UI 的 Save & Reload 热重载）。 |
| UI 打不开 | `UI_LISTEN` 为空/`-` 被禁用、端口未映射（compose `ports`）、或进程未监听（日志里找 `web ui listening`）。 |

## 开源协议

本项目采用 [GNU General Public License v2.0](../LICENSE)（`GPL-2.0-only`），完整条款见 [`LICENSE`](../LICENSE)。

Copyright (C) 2026 滿開 (Devinaille)
