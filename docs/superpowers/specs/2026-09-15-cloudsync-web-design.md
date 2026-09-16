# cloud-sync Web UI 设计稿

> 日期：2026-09-15
> 状态：Approved（会话中已确认 4 项架构决策 + 3 项细节）
> 关联：[ARCHITECTURE.md](../../../ARCHITECTURE.md)、[cloud-sync 实现规格](./2026-09-14-cloudsync-design.md)

## 1. 目标与非目标

### 1.1 目标

在现有 `cloud-sync` 进程内嵌一个 Web UI + JSON API，让运维可以：

1. 查看当前同步状态概览（synced / failed / cleaned / unsynced 计数、OpenList 连通性、运行时长）。
2. 浏览已同步 / 未同步 / 失败 / 已清理文件列表（过滤、搜索、分页）。
3. 重试单个或多个文件。
4. 编辑并保存配置（写盘 + 热重载，无需重启容器）。

### 1.2 非目标（本期不做）

- 用户体系 / 登录会话（无鉴权；靠网络隔离）。
- 实时日志流（SSE/WebSocket）。
- 多 OpenList 账号管理。
- 前端构建链（无 Node/Vite；原生 HTML/CSS/JS + `//go:embed`）。
- 手动触发全量迁移 / 断点续传控制。

### 1.3 已确认决策

| 决策 | 选择 |
|---|---|
| 运行架构 | 内嵌到现有 cloud-sync 二进制（同一进程） |
| 前端栈 | 原生 HTML/CSS/JS + Go `//go:embed` |
| 配置保存 | 写盘 + 热重载（重建 generation） |
| 鉴权 | 无 |
| 监听端口 | 默认 `:8099`，配置键 `ui_listen`（`UI_LISTEN`）；显式设为空则禁用 |
| 重试已同步文件 | 遵循配置（不强制 overwrite） |
| “未同步”定义 | 仅视频文件（扩展名 ∈ {.mkv,.mp4,.ts,.iso} 且 size > MinFileSize） |

## 2. 架构

### 2.1 进程模型

```
main
 ├─ Supervisor（持有当前 generation，可热重载）
 │    └─ Generation: Config + Client + Watcher + Pipeline + Cleanup + StateManager
 │         （各自的 goroutine 由 generationCtx 联动）
 ├─ http.Server :ui_listen   serve 内嵌前端 + /api/*
 └─ signal.NotifyContext(SIGINT/SIGTERM)
```

Supervisor 用 `sync.RWMutex` 保护 `*Generation`；API 处理函数取快照后操作，避免与 reload 竞争。

### 2.2 目录结构（新增）

```
cloud-sync/
├── supervisor.go        # generation 生命周期 + 热重载
├── web.go               # HTTP server 装配、静态资源、路由
├── api.go               # JSON API 处理函数
├── web/                 # 内嵌静态资源
│   ├── index.html
│   ├── app.js
│   └── style.css
```

### 2.3 Generation 生命周期

```go
type Generation struct {
    cfg      *Config
    client   Uploader      // 满足 Uploader；可选实现 Ping(ctx) error
    state    *StateManager
    watcher  *Watcher
    pipeline *Pipeline
    cleanup  *Cleanup
    cancel   context.CancelFunc
    wg       sync.WaitGroup
}
```

- `Start(ctx)`：按配置构建上传器（Ping 失败不硬退出——见 §2.5）、StateManager.EnsureDirs、Watcher、Pipeline、Cleanup；派生 `genCtx`；启动 StartupScan / pipeline.Run / cleanup.Run 三个 goroutine（记入 wg）。
- `Stop()`：`cancel()` → `wg.Wait()` → `watcher.Close()`。
- `Reload(ctx)`：用 `Load(cfgPath)` 解析新配置；失败则**保留旧 generation**并返回错误。成功则 `Stop()` 旧的、用新配置 `Start()`。

> 说明：`pipeline.Run` 收到 genCtx 取消后会先 `wg.Wait()` 等在途上传结束再返回（现有行为），因此 reload 不会破坏在途任务，也不会为被中断的任务写 `failed`。

### 2.4 可测试性

Supervisor 注入依赖工厂，便于单测替换：

```go
type SupervisorDeps struct {
    NewUploader func(cfg *Config, log *slog.Logger) Uploader
}
```

默认工厂构造 `*Client`。测试注入 mock。

### 2.5 启动失败降级

`Supervisor.Start` 若失败（如 OpenList ping 失败、Watcher 初始化失败），**不退出进程**：记录 `lastErr`，`gen == nil`。HTTP server 仍然启动，`/api/status` 返回 `ok:false` + 错误信息，运维可在 UI 里修正配置后触发 `Reload` 恢复。

> 这是相对旧行为（ping 失败 `os.Exit(3)`）的**有意变更**：没有 UI 时看不到错误，有 UI 时应让面板活下来以便修复。

### 2.6 日志与重载

`Reload` 成功后调用 `Init(newCfg.LogLevel, newCfg.LogFile)` + `slog.SetDefault`，新 generation 的组件使用新 logger。旧 logger 随旧 generation 丢弃。

## 3. HTTP API

无鉴权。所有响应 `application/json`（错误用 `{"error":"..."}` + 相应状态码）。

### 3.1 `GET /api/status`

```json
{
  "ok": true,
  "started_at": "2026-09-15T10:00:00Z",
  "uptime_seconds": 3600,
  "openlist_ping": true,
  "watch_dirs": ["/mnt/basic/media/media", "/mnt/basic/media/ani-rss"],
  "counts": { "synced": 10, "failed": 2, "cleaned": 5, "unsynced": 3 },
  "config": {
    "openlist_url": "http://openlist:5244",
    "openlist_overwrite": false,
    "cleanup_dry_run": true,
    "upload_concurrency": 2,
    "ui_listen": ":8099"
  }
}
```

- `ok:false` 时附 `"error": "..."`（generation 未启动）。
- `config.openlist_token` 字段**省略**（不返回）。

### 3.2 `GET /api/files`

Query：`state` ∈ `{all,synced,failed,cleaned,unsynced}`（默认 `all`）、`q`（子串匹配 key/src_path）、`page`（默认 1）、`page_size`（默认 50，上限 500）。

```json
{
  "items": [
    {
      "key": "Movies/X.mkv",
      "src_path": "/mnt/basic/media/media/Movies/X.mkv",
      "size": 8589934592,
      "state": "synced",
      "cloud_path": "/139yun_media/media/Movies/X.mkv",
      "synced_at": "2026-09-15T10:00:00Z",
      "cleanup_at": "2026-09-18T10:00:00Z",
      "retry_count": 0,
      "error": ""
    }
  ],
  "total": 42,
  "page": 1,
  "page_size": 50
}
```

数据来源：

| state | 来源 |
|---|---|
| `synced` / `failed` / `cleaned` | `.sync_status/` 全量记录（`StateManager.ListAll()`） |
| `unsynced` | 递归 `WatchDirs`，命中 `shouldEmit`（扩展名 + size > MinFileSize）且 `AlreadySynced(key)==false` 的文件 |
| `all` | 以上合并（记录优先；同 key 不重复） |

### 3.3 `GET /api/config`

返回 `{"yaml": "<配置文件原文>"}`。原文包含 token（面板无鉴权，且 token 本就落盘；文档中说明）。

### 3.4 `PUT /api/config`

Body `{"yaml": "..."}`：

1. 写入临时文件，用 `Load(tmpPath)` 校验（含必填/路径存在性/格式）。
2. 校验失败 → `400` + 错误，不改动线上配置。
3. 成功 → 原子写回 `cfgPath`（写 `*.tmp` 再 `os.Rename`）。
4. 调 `Supervisor.Reload`。
5. 返回 `{"ok":true,"status": <同 /api/status>}`；reload 失败返回 `500` + 错误（磁盘已是新配置，下次重启生效）。

### 3.5 `POST /api/retry`

Body：

```json
{ "keys": ["Movies/X.mkv", "Shows/Y/01.mkv"] }
```

或按状态批量：

```json
{ "state": "failed", "all": true }
```

行为：对每个目标 key：

1. `StateManager.Delete(key)` —— 删除所有日期桶与 `FAILED/<date>/` 下该 key 的记录。
2. 解析源文件绝对路径：优先取被删记录的 `SrcPath`，否则 `join(matchedWatchDir, key)`。
3. 源文件不存在 → 该 key 返回错误（“source gone”，通常是已 cleaned）。
4. 存在 → `go pipeline.Process(ctx, FileEvent{Path, Size, Detected: now})`（复用现有状态机、并发信号量与 per-key 去重）。

返回：

```json
{ "results": [ { "key": "Movies/X.mkv", "ok": true, "error": "" } ] }
```

### 3.6 `POST /api/cleanup/run`

立即执行一次 cleanup tick（遵循 `cleanup_dry_run`）。返回处理条数。用于验证 dry-run。

## 4. 组件改动

### 4.1 `StateManager`（state.go）

新增：

```go
// ListAll 返回所有记录（synced/failed/cleaned），Key 为相对 watch root 的路径。
func (s *StateManager) ListAll() ([]*StatusRecord, error)

// Delete 删除 key 在所有日期桶与 FAILED/<date>/ 下的记录；不存在不算错。
func (s *StateManager) Delete(key string) error
```

- 加 `mu sync.Mutex` 保护 Write/Update/Delete（List* 只读，可不加锁）。
- Delete 顺带清理空父目录（best-effort）。

### 4.2 `Pipeline`（pipeline.go）

新增导出方法（供 retry 使用）：

```go
// Process 将单个文件送入状态机（复用 Run 的 process，含并发信号量与去重）。
func (p *Pipeline) Process(ctx context.Context, ev FileEvent)
```

把现有私有 `process` 保留，`Process` 直接调用它。

### 4.3 `Cleanup`（cleanup.go）

新增导出方法：

```go
// TickNow 执行一次清理（供 UI 手动触发）。
func (c *Cleanup) TickNow(ctx context.Context) (int, error)
```

`Tick` 已存在（接受 now）；`TickNow` 用 `time.Now()` 包装，并返回处理计数（需让 Tick 返回计数或增加内部计数）。

### 4.4 `Config`（config.go）

新增字段：

```go
UIListen string  // 默认 ":8099"；显式空/“-” 表示禁用
```

- YAML：`ui_listen`，类型 `*string`（nil = 未设置 → 默认；显式 `""`/`"-"` → 禁用）。
- env：`UI_LISTEN`，用 `os.LookupEnv` 区分“未设置”（默认）与“设置为空”（禁用）。
- 默认值：未设置 → `":8099"`。

### 4.5 `main.go`

重构为：

```go
cfgPath := "/config/cloud-sync.yaml"; if len(os.Args)>1 { cfgPath = os.Args[1] }
cfg, err := Load(cfgPath); if err != nil { stderr; exit(2) }
log := Init(cfg.LogLevel, cfg.LogFile); slog.SetDefault(log)

sup := NewSupervisor(cfgPath, cfg, log, SupervisorDeps{})
ctx, cancel := signal.NotifyContext(...); defer cancel()
if err := sup.Start(ctx); err != nil { log.Error("initial start failed; UI remains up", ...) }
defer sup.Stop()

if cfg.UIListen != "" && cfg.UIListen != "-" {
    srv := NewWebServer(sup, log)
    go srv.Serve(ctx, cfg.UIListen)
}

<-ctx.Done()
```

## 5. 前端

单页，三个 Tab（原生 fetch + DOM，无框架）：

1. **Dashboard**：状态卡片（synced/failed/cleaned/unsynced 计数）、OpenList ping 徽标、uptime、watch dirs；“立即清理”按钮（调 `/api/cleanup/run`，提示 dry-run 状态）。
2. **Files**：状态 Tab + 搜索框 + 分页；表格列 = 路径/大小/状态/云路径/同步时间/清理时间/错误；行 checkbox + “重试选中” + 单行“重试”。
3. **Config**：YAML 文本域 + “校验”（前端仅做基础非空/缩进提示） + “保存并重载”；右侧只读展示 `/api/status` 的 effective config。

行为细节：

- 重试后 toast 提示每条结果，并刷新列表。
- 保存配置成功后刷新 Dashboard（计数/端口可能变化）。
- 每 5s 轮询 `/api/status`（页面可见时）。

## 6. 配置与部署

- `config.example.yaml`：新增 `ui_listen: ":8099"` 注释说明。
- `.env.example`：新增 `UI_LISTEN=:8099`。
- `docker-compose.yml`：cloud-sync 增加 `ports: ["8099:8099"]`；配置挂载由 `:ro` 改为 `:rw`（保存配置需写权限）。
- README（根 + cloud-sync）：新增“Web UI”章节（访问方式、无鉴权警告、端口、保存配置会热重载）。
- Dockerfile 不变（静态二进制 + 内嵌资源；distroless 可跑）。

## 7. 测试

| 层 | 内容 |
|---|---|
| `state_test.go` | ListAll 汇总各桶；Delete 跨桶删除；Delete 不存在不报错 |
| `config_test.go` | `ui_listen` 默认 `:8099`；YAML 覆盖 env；显式空禁用 |
| `supervisor_test.go` | Start 构建并运行；Reload 换 generation 且旧 ctx 取消；Start 失败时 gen=nil 且不影响 UI |
| `api_test.go` | `/api/status` 计数与错误态；`/api/files` 过滤/分页/unsynced 扫描；`/api/config` PUT 校验失败与成功（断言 reload 被调用）；`/api/retry` 删记录 + 入队（mock pipeline） |
| `web_test.go` | 静态首页可访问；未知路径 404 |
| smoke | 扩展 `scripts/smoke.sh`：启动后 `curl /api/status`、`/api/files` 断言计数 |

全部通过 `go test -race -count=1 ./...`、`gofmt -l`、`go vet`。

## 8. 风险

| 风险 | 缓解 |
|---|---|
| 无鉴权 + 可写配置端点 | 文档显式警告，仅内网暴露；token 鉴权记入 backlog |
| 热重载期间短暂停止监听/上传 | generation 切换亚秒级；在途任务安全收尾 |
| reload 时改 `sync_status_dir` 导致状态目录变化 | 新 generation 重新 EnsureDirs；旧目录数据不迁移（文档说明） |
| retry 删除记录与在途上传竞争 | pipeline per-key in-flight 去重；最坏情况重试一次 |
| unsynced 递归扫描大目录 | 分页 + page_size 上限 500；扫描结果做 key 去重 |

## 9. Backlog

- Token / Basic Auth（`ui_token`）。
- 实时日志流。
- 批量清理云端文件（本架构不删云端，保留历史）。
- 前端 SPA 化（如需更复杂交互）。
