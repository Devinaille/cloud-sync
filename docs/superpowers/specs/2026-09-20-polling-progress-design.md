# 持续轮询任务进度 + 去掉单任务总超时 —— 设计文档

- 日期：2026-09-20
- 状态：已批准，待实施
- 关联：`ARCHITECTURE.md` §7.2 / §8.4；`docs/superpowers/specs/2026-09-18-openlist-task-timeout-design.md`

## 1. 背景

上一轮修复（A+C，commit `cbadf0a`）解决了"轮询传输错误被当成任务失败并重新 Copy"的问题，但仍保留了 `TASK_TIMEOUT_SECONDS`（默认 1800s）作为**单任务总预算**：一旦超过就返回 `errTaskTimeout` 并写 `failed`。

配合 139 存储的 `custom_upload_part_size` 调整后，大文件上传可以正常持续超过 30 分钟。此时总预算会导致**误判失败**。用户诉求：**30 分钟到点不要判失败，继续拿到该任务的实时进度即可**。不需要 OpenList 任务持久化，也不需要跨重启重连/续传。

## 2. 目标 / 非目标

**目标**
- `TASK_TIMEOUT_SECONDS` 改义为**单次 HTTP 请求超时**（约束 `Copy` 与每次任务查询），不再限制单任务总时长。
- 持续轮询同一 OpenList `taskID` 直到终态 / 父 ctx 取消 / 任务不存在（404）。
- 获取并展示**上传进度百分比**：Dashboard 计数 + Files 每行百分比（内存，不落盘）。

**非目标**
- OpenList 任务持久化、跨重启重连、字节级续传。
- 进度落盘（cloud-sync 重启后进度消失，由 StartupScan 重传兜底）。

## 3. 行为

### 3.1 轮询循环
`uploadWithRetry` 中，`Copy` 成功后持续轮询同一 `taskID`：
- `state==2`（succeeded）→ 写 `synced`；
- `state==4/7`（canceled/failed）→ 失败，可重试 `Copy`（`maxAttempts=3`）；
- 其他状态（pending/running/canceling/errored/failing/waiting_retry/before_retry）→ 继续轮询；
- 轮询请求错误（网络/超时）→ log + 按 `PollInterval` 继续（**不重新 Copy**，服务端任务仍在跑）；
- **404 任务不存在** → 先 `Exists(dst)`：存在→ `synced`；不存在→重新 `Copy`（同样受 `maxAttempts` 限制）；仍失败→ `failed`；
- 父 ctx 取消 → 返回 `ctx.Err()`，不写 `failed`。

### 3.2 超时语义
- `Copy` 请求：`context.WithTimeout(ctx, TaskTimeout)`。
- 每次任务查询：`context.WithTimeout(ctx, TaskTimeout)`（或客户端 30s 超时）。
- 无总预算硬闸。

### 3.3 进度
- 每次轮询读取 OpenList `TaskInfo.progress`（0–100）与 `total_bytes`。
- Pipeline 在内存维护 `map[key]float64`；上传结束（任何路径）清理。
- `GET /api/status`：`counts.syncing` = 进行中 key 数；`counts.unsynced` 需扣除这些 key。
- `GET /api/files`：进行中的项 `state="syncing"` + `progress`。
- UI：Dashboard "Syncing/上传中" 卡片；Files 的 `syncing` 筛选项与行内百分比。

## 4. 接口与数据结构

```go
// internal/openlist
type TaskProgress struct {
    Status     TaskStatus
    Progress   float64
    TotalBytes int64
}
var ErrTaskNotFound = errors.New("openlist task not found")

func (c *Client) TaskPoll(ctx context.Context, taskID string) (TaskProgress, error)
// taskID == "" → {TaskSucceeded, 100, 0}
// HTTP 404      → ErrTaskNotFound
// state 2→succeeded; 4,7→failed; else pending

// internal/pipeline
type Uploader interface {
    Copy(ctx, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error)
    TaskPoll(ctx context.Context, taskID string) (openlist.TaskProgress, error)
    Exists(ctx context.Context, path string) (bool, error)
}
func (p *Pipeline) Inflight() map[string]float64 // 快照

// internal/httpapi
type statusCounts struct { Synced, Failed, Cleaned, Unsynced, Syncing int }
type fileItem struct { ...; Progress float64 `json:"progress"` }
```

## 5. 影响面

- `internal/openlist/openlist.go`、`internal/mockopenlist/mock.go`
- `internal/pipeline/pipeline.go`（`uploadWithRetry`、进度 map）
- `internal/httpapi/{api_status.go,api_files.go,files_query.go,api_types.go}`
- `internal/httpapi/web/{index.html,app.js,style.css}`
- 文档：`config.example.yaml`、`cloud-sync/README.md`、`ARCHITECTURE.md` §7.2/§8.4

## 6. 取舍 / 风险

- 移除总超时后，若 OpenList 任务长期卡死会持续轮询；由父 ctx（关停/暂停）兜底。后续可加"无进展看门狗"，本次不做。
- 进度仅内存：重启后消失；重启期间云盘任务继续，StartupScan 会重新 Copy（`skip_existing`/`Exists` 部分去重）。
- 暂停态一次性 retry 使用临时 pipeline，其进度不在 Dashboard 显示（仅 generation 内上传显示）。

## 7. 顺带修复

上次 `api.go` 按行切片拆分为多文件时，若干函数头注释落到了错误函数上（`files_query.go`、`api_precheck.go`、`api_status.go`），一并纠正并全量核对。
