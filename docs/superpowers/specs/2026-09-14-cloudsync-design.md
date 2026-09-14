# cloud-sync 设计稿

> 日期：2026-09-14
> 状态：Draft（待用户审阅）
> 关联：[ARCHITECTURE.md v3.1](../../ARCHITECTURE.md)

## 1. 目标与非目标

### 1.1 目标

实现 `cloud-sync` 容器 — 监听 `media/` 与 `ani-rss/` 新视频文件，调 OpenList API 上传到 139yun，写 `.sync_status/` 标记，72h 后清理本地源视频。

### 1.2 非目标（本期不做）

- 139strm 容器实现（已部署，由其他流程负责）
- OpenList 容器实现（已部署）
- 真实的 Prometheus /metrics 端点
- Bark / Telegram 等外部告警通道
- 精细化崩溃恢复（区分 uploading vs synced 状态）
- 存量媒体的一次性迁移脚本
- 跨平台支持（仅 Linux/inotify）
- 与 OpenList 的集成测试（仅 mock + httptest）

### 1.3 与 ARCHITECTURE.md 的关系

ARCHITECTURE.md 描述了系统设计，本文档是其 cloud-sync 组件的实现规格。ARCHITECTURE.md 后续会新增 §14 引用本文档；本文档不重复 ARCHITECTURE 已有的全局设计（如 §8.3 状态机的语义、§7 OpenList API 协议），仅描述实现层面的决策（语言、包结构、并发模型、接口签名、错误处理）。

## 2. 架构

### 2.1 进程模型

单 Go 进程，多 goroutine pipeline：

```
┌─────────────────────────────────────────────────────────┐
│ cloud-sync                                              │
│                                                         │
│  main goroutine                                         │
│    ├─ watcher goroutine     (fsnotify event loop)        │
│    ├─ startup-scan goroutine (启动时一次性)              │
│    ├─ pipeline workers      (N=UPLOAD_CONCURRENCY)       │
│    │   每个事件一个 goroutine                            │
│    │   stabilize → validate → upload → mark             │
│    ├─ poller goroutines     (per upload)                 │
│    └─ cleanup ticker        (hourly)                     │
│                                                         │
│  共享：*slog.Logger, *Config, *StateManager              │
└─────────────────────────────────────────────────────────┘
```

所有 goroutine 通过 `context.Context` 联动取消，收到 SIGINT/SIGTERM 时干净退出。

### 2.2 目录结构

扁平布局（`cloud-sync/` 根目录下直接放 .go 文件）：

```
cloud-sync/
├── Dockerfile              # multi-stage: golang → distroless
├── go.mod
├── go.sum
├── README.md               # 子模块说明（如何 build / run / test）
├── main.go                 # 入口，装配所有组件
├── config.go               # 环境变量加载与校验
├── watcher.go              # fsnotify 封装
├── pipeline.go             # 单文件状态机
├── openlist.go             # OpenList HTTP client
├── state.go                # .sync_status 读写
├── cleanup.go              # 72h 清理 ticker
├── logging.go              # slog 初始化
└── *_test.go               # 与上述各文件对应
```

预估 ~1500 行 Go（含测试）。后续如组件增多再拆 `internal/`。

### 2.3 Docker 镜像

- 构建阶段：`golang:1.23-alpine`
- 运行阶段：`gcr.io/distroless/static-debian12:nonroot`（非 root，UID 65532）
- 体积目标：< 30MB

## 3. 组件接口

### 3.1 `config`

```go
type Config struct {
    // OpenList
    OpenListURL   string
    OpenListToken string
    SrcStorage    string  // e.g. "/local_media"
    DstStorage    string  // e.g. "/139yun_media"

    // Watch paths
    WatchMediaDir  string
    WatchAniRSSDir string

    // Status
    SyncStatusDir string

    // Cleanup
    CleanupAfter  time.Duration  // CLEANUP_AFTER_HOURS
    CleanupDryRun bool

    // Upload pipeline
    UploadConcurrency int           // UPLOAD_CONCURRENCY
    StabilizeWait     time.Duration  // STABILIZE_WAIT_SECONDS
    PollInterval      time.Duration  // POLL_INTERVAL_SECONDS
    TaskTimeout       time.Duration  // TASK_TIMEOUT_SECONDS
    MinFileSize       int64          // derived, 100MB

    // Safety
    AllowedPrefixes     []string  // ALLOWED_SOURCE_PREFIXES (comma-split)
    RequireNFOForMedia  bool
    RequireNFOForAniRSS bool

    // Logging
    LogLevel string  // debug|info|warn|error
    LogFile  string  // empty → stdout
}

func Load() (*Config, error)
```

校验规则：
- 所有 `string` 必填字段非空
- 所有路径存在且可读
- 数值字段 > 0
- `AllowedPrefixes` 至少一项
- 启动时 `os.Stat` 检查目录存在

### 3.2 `logging`

```go
func Init(level, file string) *slog.Logger
```

- 默认输出 JSON（便于结构化收集）
- `LogFile == ""` → stdout，否则 `os.OpenFile(..., O_APPEND|O_CREATE|O_WRONLY, 0644)`
- 启动时确保父目录存在（`os.MkdirAll`）

### 3.3 `watcher`

```go
type Category int
const (
    CatMedia Category = iota
    CatAniRSS
)

type FileEvent struct {
    Path     string    // absolute path
    Size     int64     // bytes
    Detected time.Time
    Category Category
}

type Watcher struct { /* ... */ }

func New(dirs []string, minSize int64, log *slog.Logger) (*Watcher, error)
func (w *Watcher) Events() <-chan FileEvent
func (w *Watcher) Errors() <-chan error
func (w *Watcher) Close() error
```

行为：
- 启动时对每个 dir 做 `filepath.Walk` 收集所有子目录，调用 `fsnotify.Add`
- 事件循环只关注 `IN_CLOSE_WRITE` 与 `IN_MOVED_TO`
- 过滤：扩展名 ∈ {`.mkv` `.mp4` `.ts` `.iso`}，size > minSize
- Category 判定：父目录包含 `WatchMediaDir` → `CatMedia`，包含 `WatchAniRSSDir` → `CatAniRSS`
- 内部 Events chan 缓冲 1024（避免 watcher 阻塞）

### 3.4 `pipeline`

```go
type Uploader interface {
    Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (taskID string, err error)
    TaskDone(ctx context.Context, taskID string) (TaskStatus, error)
}

type TaskStatus string
const (
    TaskPending   TaskStatus = "pending"
    TaskSucceeded TaskStatus = "succeeded"
    TaskFailed    TaskStatus = "failed"
)

type Pipeline struct {
    cfg  *Config
    log  *slog.Logger
    up   Uploader
    st   *StateManager
    sem  chan struct{}  // buffer cap = UploadConcurrency
}

func New(cfg *Config, log *slog.Logger, up Uploader, st *StateManager) *Pipeline
func (p *Pipeline) Run(ctx context.Context, events <-chan FileEvent)
```

#### 状态机（每个 FileEvent 一个 goroutine）

```
DETECTED
  │ stabilize: 5s 一次 stat，size+mtime 不变则下一步
  │              持续 30s 还在变 → skip（事件已丢失，不主动重试）
STABILIZED
  │ validate:
  │   - 路径必须在 AllowedPrefixes 之一
  │   - state.AlreadySynced(relPath) == false
  │   - size > MinFileSize
  │   - (CatMedia && RequireNFOForMedia) → 必须存在 .nfo
  │   - (CatAniRSS && RequireNFOForAniRSS) → 必须存在 .nfo
  │   任一失败 → log + skip
VALIDATED
  │ acquire sem slot
QUEUED
  │ openlist.Copy(srcDir=SrcStorage, srcName=<rel_under_media_or_anirss>,
  │              dstDir=DstStorage, dstName=<rel>)
  │ → taskID
UPLOADING
  │ openlist.TaskDone 轮询 PollInterval
  │   pending → continue
  │   succeeded → SYNCED
  │   failed → FAILED (3 次重试 + 指数退避 1/2/4/8/.../60s)
  │   TaskTimeout → FAILED
SYNCED
  │ state.Write(StatusRecord{Status: "synced", ...})
DONE
  │ release sem slot
```

stabilize 阶段实现：

```go
func stabilize(path string, wait time.Duration, log *slog.Logger) error {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    deadline := time.Now().Add(wait)
    lastSize, lastMtime := stat(path)
    for {
        select {
        case <-time.After(time.Until(deadline)):
            return nil  // 静止时长够
        case <-ticker.C:
            s, m := stat(path)
            if s != lastSize || m != lastMtime:
                lastSize, lastMtime = s, m
                deadline = time.Now().Add(wait)  // 重置窗口
        }
    }
}
```

#### startup scan

```go
func (p *Pipeline) StartupScan(ctx context.Context) error
```

启动时执行一次：
1. 遍历 `WatchMediaDir` + `WatchAniRSSDir` 所有视频文件
2. 对每个文件计算 relPath，调用 `state.AlreadySynced(relPath)`
3. 未同步的 → 走与 watcher 事件相同的 pipeline（直接调用内部 `process(ctx, event)`）
4. 已同步的 → skip
5. 完成时 log "startup scan done, N new files"

实现为单独的方法，在 `main.go` 启动时 `go p.StartupScan(ctx)`，与 watcher 并行。

### 3.5 `openlist`

```go
type Client struct {
    baseURL string
    token   string
    http    *http.Client  // timeout = 30s
    log     *slog.Logger
}

func New(baseURL, token string, log *slog.Logger) *Client

// Ping 在启动时调用，验证连通性
func (c *Client) Ping(ctx context.Context) error

// Copy 调 POST /api/fs/copy
// srcName 形如 "media/Movies/X/X.mkv"（含源子目录）
// dstName 形如 "media/Movies/X/X.mkv"（云盘侧最终路径）
func (c *Client) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error)

// TaskDone 调 GET /api/admin/task/{id}/done
func (c *Client) TaskDone(ctx context.Context, taskID string) (TaskStatus, error)
```

请求示例（与 ARCHITECTURE §7.1 一致）：

```http
POST /api/fs/copy
Authorization: <token>
Content-Type: application/json

{
  "src_dir": "/local_media",
  "src_name": "media/Movies/Interstellar (2014)/Interstellar.mkv",
  "dst_dir": "/139yun_media",
  "dst_name": "media/Movies/Interstellar (2014)/Interstellar.mkv"
}
```

响应：

```json
{ "code": 200, "data": { "task_id": "abc123" } }
```

错误：
- HTTP 非 2xx → `fmt.Errorf("openlist copy: HTTP %d: %s", code, body)`
- `code != 200` → `fmt.Errorf("openlist copy: code=%d msg=%s", ...)`
- 网络错误 → 直接返回

### 3.6 `state`

```go
type StatusRecord struct {
    SrcPath        string     `json:"src_path"`
    SrcSize        int64      `json:"src_size"`
    SrcMtime       time.Time  `json:"src_mtime"`
    CloudPath      string     `json:"cloud_path"`
    OpenListTaskID string     `json:"openlist_task_id"`
    Category       string     `json:"category"`  // "movie" | "anime"
    HasNFO         bool       `json:"has_nfo"`
    SyncedAt       time.Time  `json:"synced_at"`
    CleanupAt      time.Time  `json:"cleanup_at"`
    Status         string     `json:"status"`  // "synced" | "failed" | "cleaned"
    RetryCount     int        `json:"retry_count"`
    CleanedAt      *time.Time `json:"cleaned_at,omitempty"`
    Error          string     `json:"error,omitempty"`
}

type StateManager struct {
    root string  // SYNC_STATUS_DIR
    log  *slog.Logger
}

func NewStateManager(root string, log *slog.Logger) *StateManager
func (s *StateManager) EnsureDirs() error
func (s *StateManager) AlreadySynced(relPath string) (bool, error)
func (s *StateManager) Write(rec *StatusRecord) error
func (s *StateManager) ListForCleanup(now time.Time) ([]*StatusRecord, error)
func (s *StateManager) Update(rec *StatusRecord) error
```

存储布局：

```
.sync_status/
├── 2026-09-14/
│   ├── Movies/Interstellar (2014)/Interstellar.mkv.json
│   └── anime/Some Show/EP01.mkv.json
├── 2026-09-15/
│   └── ...
└── FAILED/
    └── 2026-09-14/
        └── ...
```

- `AlreadySynced`：扫描 `.sync_status/` 下所有日期子目录（含 `FAILED/`），若存在 `<relPath>.json` 则返回 true。**任意状态都算"已处理"**（synced/cleaned/failed），避免对同名文件重复操作
- `Write`：用 `os.MkdirAll` + 临时文件 `*.json.tmp` + `os.Rename` 保证原子写
- `ListForCleanup`：`filepath.Walk` 所有日期目录，解析每个 `.json`，过滤 `Status == "synced" && CleanupAt < now`
- `Update`：同 Write，写回原路径

### 3.7 `cleanup`

```go
type Cleanup struct {
    cfg *Config
    log *slog.Logger
    st  *StateManager
}

func New(cfg *Config, log *slog.Logger, st *StateManager) *Cleanup
func (c *Cleanup) Run(ctx context.Context)
```

每 `time.Hour` tick 一次。每次循环：

```go
const maxPerTick = 50

for _, rec := range listForCleanup(now) {
    // 1. 白名单硬校验（防御 cleanup 自身的逻辑 bug）
    if !whitelisted(rec.SrcPath, cfg.AllowedPrefixes) {
        log.Error("cleanup blocked: path not in whitelist", "path", rec.SrcPath)
        continue
    }
    // 2. 再次确认 status
    if rec.Status != "synced" {
        continue
    }
    // 3. dry-run vs real
    if cfg.CleanupDryRun {
        log.Info("[DRY-RUN] would delete", "path", rec.SrcPath)
        continue
    }
    // 4. 删除视频（绝不删 nfo/jpg/字幕）
    for _, ext := range []string{".mkv", ".mp4", ".ts", ".iso"} {
        candidate := stripExt(rec.SrcPath) + ext
        if exists(candidate) {
            if err := os.Remove(candidate); err != nil {
                log.Error("delete failed", "path", candidate, "err", err)
            }
        }
    }
    // 5. 更新 record
    now := time.Now()
    rec.Status = "cleaned"
    rec.CleanedAt = &now
    st.Update(rec)
}
```

## 4. 数据流

```
[fsnotify loop]──events──►[Pipeline.Run]
                              │ spawn goroutine per event
                              ▼
                       [stabilize → validate]
                              │
                              ▼ sem acquire (chan struct{}, cap=UPLOAD_CONCURRENCY)
                       [openlist.Copy]──task_id──►[openlist.TaskDone loop]
                                                          │
                                                          ▼ status=succeeded
                                                       [state.Write]
                                                          │
                                                          ▼ sem release

[startup-scan goroutine]
   │ on boot: walk media/ + ani-rss/
   │ for each non-synced video: feed to internal process()
   ▼

[cleanup.Run]──hourly ticker──►[state.ListForCleanup]
                                       │
                                       ▼ per record
                                [whitelist check → log(dry-run) OR delete + state.Update]
```

## 5. 错误处理

| 错误 | 行为 | 重试 / 缓解 |
|---|---|---|
| fsnotify watch 失败 | log + 跳过该目录 | 启动时一次性失败退出 |
| 文件静止超时 | skip 该 event | 不主动重试（事件丢失，但 startup scan 会兜底） |
| 校验失败（白名单/.nfo/.sync_status） | log + skip | 无 |
| OpenList 网络错误 | 指数退避 1/2/4/8/.../60s | 总尝试最多 3 轮；仍失败写 FAILED |
| OpenList 任务失败 | log + 写 FAILED | 同上 |
| 任务超时（>TaskTimeout） | log + 写 FAILED | 同上 |
| 清理白名单不通过 | log error + skip | 绝不删 |
| context 取消 | 各 goroutine 干净退出 | 重启靠 startup scan 恢复 |

### 防抖

不显式 dedup —— stabilize 窗口天然合并重复 inotify 事件。

### 崩溃恢复（MVP）

启动时跑 `StartupScan`，对无 `.sync_status` 的视频走完整 pipeline。**简单可靠**，解决"重启时 in-flight upload 丢失"问题。精细化恢复（区分 uploading vs synced）记入 backlog。

## 6. 配置

通过环境变量加载（详见 docker-compose.yml 与 .env.example）。所有 env var 在 `config.Load()` 中读取并校验，启动失败立即退出非 0。

无配置文件、无 CLI flag，遵循 ARCHITECTURE §10 "环境变量驱动"。

## 7. Docker

`cloud-sync/Dockerfile`：

```dockerfile
# syntax=docker/dockerfile:1.7

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/cloud-sync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cloud-sync /usr/local/bin/cloud-sync
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/cloud-sync"]
```

`docker-compose.yml` 中挂载 `/mnt/basic/media` 只读顶层 + `.sync_status/`、`media/`、`ani-rss/` 子目录单独 rw（与 ARCHITECTURE §8.2 一致）。

## 8. 测试

### 8.1 单元测试（每个组件一个 `_test.go`）

| 测试文件 | 范围 | 工具 |
|---|---|---|
| `config_test.go` | env 解析、必填校验、默认值、错误情况 | stdlib |
| `logging_test.go` | level 过滤、JSON 输出 | `bytes.Buffer` |
| `watcher_test.go` | 过滤逻辑、Category 判定 | 直接构造 FileEvent |
| `pipeline_test.go` | 状态机各分支、并发限流 | mock Uploader + temp dir state |
| `openlist_test.go` | 真实 HTTP 路径、错误响应 | `httptest.NewServer` |
| `state_test.go` | 读写、原子性、ListForCleanup 过滤 | temp dir |
| `cleanup_test.go` | dry-run、真删、白名单保护、单 tick 上限 | temp dir |

### 8.2 Mock 策略

- `Uploader` interface：测试用 mock 实现
- `StateManager`：实际 temp dir（避免过度 mock）
- `OpenList` client：`httptest.NewServer` mock API

### 8.3 覆盖率目标

不强求 80%。每个组件至少：
- 1 个正常路径
- 1 个错误路径
- 1 个边界（empty / nil / 超时）

## 9. Hygiene 文件

### 9.1 项目根 `.gitignore`

```
.env
*.log
logs/
.sync_status/
cloud-sync/cloud-sync
cloud-sync/dist/
```

### 9.2 项目根 `README.md`

简介 + 目录说明 + 子模块链接 + 部署引用 ARCHITECTURE.md。

### 9.3 项目根 `.env.example`

所有 docker-compose.yml 中 `cloud-sync` 服务的 env var 模板（带说明注释）。

### 9.4 `cloud-sync/README.md`

子模块说明：
- 如何 build（`docker build` / `go build`）
- 如何跑测试（`go test ./...`）
- 关键 env var 说明
- 故障排查（看哪几个日志字段）

## 10. Backlog

以下项本期**不做**，记入 `ARCHITECTURE.md` 或独立 issue：

1. Prometheus `/metrics` endpoint（上传/失败计数、队列长度、并发）
2. Bark / Telegram 告警通道
3. 精细化崩溃恢复（区分 uploading vs synced）
4. 存量媒体一次性迁移脚本
5. 真实 OpenList 集成测试（需要测试 fixture）
6. Healthcheck endpoint
7. GitHub Actions CI
8. 跨平台支持（macOS/Windows）

## 11. 决策记录

| 决策 | 选项 | 理由 |
|---|---|---|
| 语言 | Go | 单二进制、镜像小、原生并发 |
| fsnotify 库 | fsnotify (Linux inotify backend) | 唯一主流选择 |
| HTTP 库 | stdlib net/http | 3 个 endpoint，无需第三方 |
| 日志库 | log/slog (stdlib) | Go 1.21+ 内置 |
| cron 库 | 自实现 time.Ticker | 不需要 cron 表达式 |
| 进程模型 | 单进程 goroutine pipeline | 与 ARCHITECTURE §8.3 状态机匹配 |
| 目录结构 | 扁平 | MVP 规模够用 |
| 基础镜像 | distroless/static | < 30MB，非 root |
| 崩溃恢复 | startup scan | 简单可靠 |
| 告警 | 仅结构化日志 | 后续接外部通道 |
| 指标 | 无 | 后续按需加 |
