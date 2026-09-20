# 持续轮询任务进度 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 去掉单任务总超时，持续轮询同一 OpenList copy 任务并展示上传进度百分比。

**Architecture:** `openlist.Client.TaskPoll` 返回 `{State, Progress, TotalBytes}` 并区分 404；`pipeline.uploadWithRetry` 持续轮询直到终态/取消/404；Pipeline 内存维护每 key 进度并经 `Inflight()` 暴露；httpapi 在 status/files 中输出 `syncing` 与 `progress`；UI 展示。

**Tech Stack:** Go 1.22；无新依赖。

**Spec:** `docs/superpowers/specs/2026-09-20-polling-progress-design.md`

## Global Constraints
- 命令在 `cloud-sync/` 下执行；每任务一次提交；`gofmt -l .` 空、`go vet ./...`、`go test -race -count=1 ./...` 全绿。
- Conventional Commits（`feat:` / `fix:` / `docs:`）。
- 不新增依赖；镜像 < 30 MiB。

---

### Task 1: openlist TaskPoll
**Files:** Modify `internal/openlist/openlist.go`; Test `internal/openlist/openlist_test.go`
**Produces:** `TaskProgress{Status,Progress,TotalBytes}`、`ErrTaskNotFound`、`TaskPoll`。

- [ ] 测试：解析 `data.state/progress/total_bytes`；`taskID==""`→succeeded/100；HTTP 404→`ErrTaskNotFound`。
- [ ] 实现 `TaskProgress`、`ErrTaskNotFound`，把 `TaskDone` 重写为 `TaskPoll`（保留数值状态映射）。
- [ ] `go test ./internal/openlist`，提交 `feat(openlist): return task progress and distinguish 404`。

### Task 2: mock + Uploader 接口
**Files:** Modify `internal/mockopenlist/mock.go`、`internal/pipeline/pipeline.go`（接口）
- [ ] mock：`TaskPoll` 返回 `openlist.TaskProgress`；新增控制 `TaskProgress float64`、`TaskNotFound bool`。
- [ ] `pipeline.Uploader.TaskDone → TaskPoll`。
- [ ] `go build ./...` 通过（修各处编译），提交 `refactor: TaskPoll interface`。

### Task 3: pipeline 轮询改造
**Files:** Modify `internal/pipeline/pipeline.go`; Test `internal/pipeline/timeout_test.go`
- [ ] 先写失败测试：
  - `TestPipeline_PollingContinuesPastRequestTimeout`：短 `TaskTimeout`，mock 返回 pending+progress N 次后 succeeded → Copy 仅 1 次、synced、期间 progress>0。
  - `TestPipeline_TaskNotFoundExistingDestIsSynced`：`TaskNotFound=true` + `Exists` true → synced、Copy 1 次。
  - `TestPipeline_TaskNotFoundMarksFailed`：`TaskNotFound=true` + `Exists` false → 写 failed、Copy 1 次（不重新 Copy）。
- [ ] 实现：去总预算硬闸；`TaskPoll` 每轮读进度；404→`Exists` 回退；`defer clearProgress(key)`；新增 `progress` map + `Inflight()`。
- [ ] `go test -race ./internal/pipeline`，提交 `fix(pipeline): poll task until terminal; expose progress`。

### Task 4: httpapi 暴露
**Files:** Modify `internal/httpapi/{api_types.go,api_status.go,api_files.go,files_query.go}`; Test `internal/httpapi/api_test.go`
- [ ] `statusCounts.Syncing`；`fileItem.Progress`。
- [ ] `statusPayload`：`Syncing=len(inflight)`；`Unsynced` 扣除 inflight key。
- [ ] `handleFiles`：对 inflight key 覆写 `state="syncing"`+`progress`（新增 `applyInflight`）。
- [ ] 测试：构造带 inflight 的 supervisor/直接单测 `applyInflight` 与计数；提交 `feat(httpapi): surface syncing state and progress`。

### Task 5: UI
**Files:** Modify `internal/httpapi/web/{index.html,app.js,style.css}`
- [ ] Dashboard 加 `count-syncing` 卡片；Files 加 `syncing` chip；i18n `state.syncing`（en Syncing / zh 上传中）；`known[]` 增 `syncing`；行渲染显示百分比；样式。
- [ ] 本地 `./local-dev/run.sh` 抽查；提交 `feat(web): show syncing state and progress`。

### Task 6: 注释错位修复
**Files:** `internal/httpapi/{files_query.go,api_precheck.go,api_status.go,...}`
- [ ] 核对并纠正每个函数头注释；提交 `docs(httpapi): fix doc comments after split`。

### Task 7: 文档
**Files:** `config.example.yaml`、`cloud-sync/README.md`、`ARCHITECTURE.md`
- [ ] `TASK_TIMEOUT_SECONDS` 说明改为"单次请求超时"；§7.2/§8.4 更新为持续轮询 + 进度；提交 `docs: task polling and progress`。

### Task 8: 全量验证
- [ ] `gofmt -l . && go vet ./... && go test -race -count=1 ./...`
- [ ] 构建；`./scripts/smoke.sh && ./scripts/smoke-config.sh` 均 OK。
- [ ] 推送 `dev`，确认 `dev.yml` 成功。
