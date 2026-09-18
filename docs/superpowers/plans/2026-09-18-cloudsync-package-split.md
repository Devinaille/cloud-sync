# cloud-sync 包拆分为 internal/* 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把单包 `package main` 拆成 `internal/*` 领域包 + 瘦入口 `main.go`，实现单向无环的包依赖，行为不变。

**Architecture:** 保持 `go.mod` 位于 `cloud-sync/`（模块根），入口 `cloud-sync/main.go` 留在根包，其余代码下沉到 `cloud-sync/internal/`。依赖方向：叶子包 `buildinfo/logging/config/state/openlist/media` ← `watcher` ← `pipeline` ← `supervisor` ← `httpapi` ← `main`。`mockopenlist` 只依赖 `openlist`；`testutil` 只依赖 `config`。

**Tech Stack:** Go 1.22，单模块 `cloud-sync`，`//go:embed` 静态资源，fsnotify。

**Branch:** `refactor/package-split`（从 `dev` 切出，完成后 PR 回 `dev`）。

## Global Constraints

- 所有 Go 命令在 `cloud-sync/` 下执行，不是仓库根。
- 每个任务一次提交，提交时保持 `gofmt -l .`（空）、`go vet ./...`、`go test -race -count=1 ./...` 全绿。
- 提交信息用 Conventional Commits（`refactor:` 前缀）。
- 不改变任何运行时行为；除非为跨包可见性必须，否则不改函数体。
- `cloud-sync/` 模块根仍是入口包，因此 `go build .`、`go test ./...`、CI 的 `working-directory: cloud-sync`、Docker `context/file`、smoke 脚本路径、二进制输出 `cloud-sync/cloud-sync` 全部保持不变。

## 目标结构

```
cloud-sync/
├── go.mod / go.sum / Dockerfile / README.md
├── main.go                      # package main，仅装配
└── internal/
    ├── buildinfo/   version.go
    ├── logging/     logging.go (+logging_test.go)
    ├── config/      config.go (+config_test.go)
    ├── state/       state.go (+state_test.go)
    ├── openlist/    openlist.go (+openlist_test.go)
    ├── media/       media.go (+media_test.go)
    ├── watcher/     watcher.go (+watcher_test.go)   # 保留 FileEvent
    ├── pipeline/    pipeline.go (+pipeline_test.go)
    ├── cleanup/     cleanup.go (+cleanup_test.go)
    ├── supervisor/  supervisor.go (+supervisor_test.go)
    ├── mockopenlist/ mock.go
    ├── testutil/    fixtures.go
    └── httpapi/     api*.go, web.go, web/ (+api_test.go, web_test.go)
```

## 符号迁移对照

| 现状（package main） | 新归属 / 形式 |
|---|---|
| `version/commit/buildTime` | `buildinfo.Version/Commit/BuildTime`（导出） |
| `Load`、`Config` | `config.Load`、`config.Config` |
| `Init` | `logging.Init` |
| `StateManager`、`StatusRecord`、`NewStateManager` | `state.*` |
| `StateManager.recordPath` | 导出为 `state.StateManager.RecordPath` |
| `Client`、`TaskStatus`、`NewClient` | `openlist.*` |
| `shouldEmit` | `media.ShouldEmit` |
| `videoExts` | `media.IsVideoExt` |
| `hasPrefix` | `media.HasPrefix` |
| `whitelisted` | `media.Whitelisted` |
| `FileEvent` | `watcher.FileEvent` |
| `Pipeline`、`NewPipeline`、`Uploader` | `pipeline.*` |
| `Cleanup`、`NewCleanup` | `cleanup.*` |
| `Supervisor`、`SupervisorDeps`、`NewSupervisor` | `supervisor.*` |
| `WebServer`、`NewWebServer` | `httpapi.*` |
| `embed web` | `httpapi`，资源位于 `internal/httpapi/web/` |

ldflags 4 处改为 `-X cloud-sync/internal/buildinfo.{Version,Commit,BuildTime}`：
`cloud-sync/Dockerfile:12`、`.github/workflows/dev.yml:62`、`.github/workflows/rc.yml:74`、`.github/workflows/release.yml:77`。

---

## Task 1: 分支与基线

- [ ] `git checkout dev && git pull && git checkout -b refactor/package-split`
- [ ] `cd cloud-sync && gofmt -l . && go vet ./... && go test -race -count=1 ./...` 全绿。
- [ ] 提交本计划文件：`docs: add package-split implementation plan`

## Task 2: internal/buildinfo

**Files:** Create `cloud-sync/internal/buildinfo/version.go`; Delete `cloud-sync/version.go`; Modify `cloud-sync/api.go`, `cloud-sync/main.go`, `cloud-sync/Dockerfile`, 3 个 workflow。

- [ ] 新建 `internal/buildinfo/version.go`，`package buildinfo`，定义导出 `Version/Commit/BuildTime`（默认 `"dev"/"unknown"/"unknown"`），注释改为 `-X cloud-sync/internal/buildinfo.Version=...`。
- [ ] 删除根 `version.go`；`api.go` 中 `version/commit/buildTime` → `buildinfo.Version/...`；`main.go` 启动日志同上。
- [ ] 更新 4 处 ldflags 为 `-X cloud-sync/internal/buildinfo.{Version,Commit,BuildTime}`。
- [ ] 验证：`gofmt -l . && go vet ./... && go test -race -count=1 ./...`。
- [ ] 提交：`refactor: move build metadata into internal/buildinfo`

## Task 3: internal/logging

**Files:** Move `logging.go` + `logging_test.go` → `internal/logging/`；`package logging`；调用点 `Init` → `logging.Init`；测试内 `parseLevel` 保持包内。

- [ ] `git mv` 两个文件到 `internal/logging/`，改 `package logging`。
- [ ] 根包调用点加 `logging.` 前缀。
- [ ] 验证并提交：`refactor: move logging into internal/logging`

## Task 4: internal/config

**Files:** Move `config.go` + `config_test.go` → `internal/config/`；`package config`。

- [ ] `git mv`，改包名；所有 `Config` → `config.Config`，`Load(` → `config.Load(`（根包内）。
- [ ] 验证并提交：`refactor: move config into internal/config`

## Task 5: internal/openlist

**Files:** Move `openlist.go` + `openlist_test.go` → `internal/openlist/`；`package openlist`。

- [ ] `git mv`，改包名；`Client/NewClient/TaskStatus/TaskPending/TaskSucceeded/TaskFailed` → `openlist.*`。
- [ ] 验证并提交：`refactor: move openlist client into internal/openlist`

## Task 6: internal/state

**Files:** Move `state.go` + `state_test.go` → `internal/state/`；`package state`。

- [ ] `git mv`，改包名；`StateManager/StatusRecord/NewStateManager` → `state.*`。
- [ ] `recordPath` 导出为 `RecordPath`（`state_test.go` 与 `pipeline_test.go` 的 `readRecord` 调用点同步）。
- [ ] 验证并提交：`refactor: move state store into internal/state`

## Task 7: internal/mockopenlist + internal/testutil

**Files:** Create `internal/mockopenlist/mock.go`（只导入 `openlist`、`context`、`sync`），Create `internal/testutil/fixtures.go`（只导入 `config`、`log/slog`、`testing`）。

- [ ] `MockUploader`（导出 `Mu/CopyCalls/TaskStatuses/CloudExists`，实现 `Copy/TaskDone/Exists`，`New()`），不导入 `pipeline`。
- [ ] `testutil`：`TestLogger`、`TestConfig`、`WriteConfigYAML`、`PreserveLoadEnv`。
- [ ] 现有 `package main` 测试改用上述导出成员（`up.mu`→`up.Mu`、`up.copyCalls`→`up.CopyCalls`、`up.taskStatuses`→`up.TaskStatuses`、`supervisorTestLogger`→`testutil.TestLogger` 等）；需要 `SupervisorDeps.NewUploader` 处就地写返回 `pipeline.Uploader` 的闭包（此任务阶段 pipeline 仍在根包，写 `Uploader`）。
- [ ] 验证并提交：`refactor: extract mock uploader and test fixtures`

## Task 8: internal/media

**Files:** Create `internal/media/media.go` + `media_test.go`；Modify `watcher.go`、`pipeline.go`、`cleanup.go`、`api.go`。

- [ ] 抽出 `HasPrefix(path, prefix) bool`、`IsVideoExt(path) bool`、`ShouldEmit(path string, size, minSize int64) bool`、`Whitelisted(path string, prefixes []string) bool`。
- [ ] 删除 `watcher.go` 中的 `hasPrefix/videoExts/shouldEmit` 与 `pipeline.go` 中的 `whitelisted`，改调用点为 `media.*`。
- [ ] `watcher_test.go` 中针对 `shouldEmit` 的断言移到 `media_test.go`。
- [ ] 验证并提交：`refactor: extract shared file-selection helpers into internal/media`

## Task 9: internal/watcher

**Files:** Move `watcher.go` + `watcher_test.go` → `internal/watcher/`；`package watcher`。

- [ ] `git mv`，改包名；`FileEvent` 留在 `watcher`；导入 `media`。
- [ ] 根包引用 `FileEvent` → `watcher.FileEvent`。
- [ ] 验证并提交：`refactor: move watcher into internal/watcher`

## Task 10: internal/pipeline

**Files:** Move `pipeline.go` + `pipeline_test.go` → `internal/pipeline/`；`package pipeline`。

- [ ] `git mv`，改包名；导入 `config/state/media/watcher`；`Uploader` 接口随包。
- [ ] `pipeline_test.go` 使用 `mockopenlist.New()`；`readRecord` 用 `st.RecordPath(...)`。
- [ ] 验证并提交：`refactor: move pipeline into internal/pipeline`

## Task 11: internal/cleanup

**Files:** Move `cleanup.go` + `cleanup_test.go` → `internal/cleanup/`；`package cleanup`。

- [ ] `git mv`，改包名；导入 `config/state/media`。
- [ ] 验证并提交：`refactor: move cleanup into internal/cleanup`

## Task 12: internal/supervisor

**Files:** Move `supervisor.go` + `supervisor_test.go` → `internal/supervisor/`；`package supervisor`。

- [ ] `git mv`，改包名；导入 `config/state/openlist/watcher/pipeline/cleanup/logging`；`SupervisorDeps.NewUploader` 类型用 `pipeline.Uploader`。
- [ ] `supervisor_test.go` 用 `testutil` + `mockopenlist`。
- [ ] 验证并提交：`refactor: move supervisor into internal/supervisor`

## Task 13: internal/httpapi

**Files:** Move `api.go`、`web.go`、`web/` + `api_test.go`、`web_test.go` → `internal/httpapi/`；`package httpapi`；将 `api.go` 拆为：`api.go`（传输基座）、`api_types.go`、`api_status.go`、`api_files.go`、`files_query.go`、`api_precheck.go`、`api_actions.go`、`api_config.go`。

- [ ] `git mv` 并改包名；导入 `supervisor/config/state/watcher/buildinfo/media/logging`。
- [ ] `statusPayload` 用 `buildinfo.Version/...`；`handleRetry` 用 `watcher.FileEvent`。
- [ ] 按职责拆分 `api.go`（同包移动，无 import 变化）。
- [ ] 验证并提交：`refactor: move HTTP API and embedded UI into internal/httpapi`

## Task 14: 瘦入口 main.go

**Files:** Modify `cloud-sync/main.go`，导入 `config/logging/buildinfo/supervisor/httpapi`，前缀调用。

- [ ] 入口只做：解析 cfg 路径、`config.Load`、`logging.Init`、`supervisor.NewSupervisor`、`httpapi.NewWebServer`、信号与 `Serve`。
- [ ] 验证并提交：`refactor: thin main to wiring only`

## Task 15: 文档

**Files:** Modify `AGENTS.md`、`cloud-sync/README.md:293`。

- [ ] `AGENTS.md` Layout 段更新为模块根入口 + `internal/*` 分层；Commands 不变。
- [ ] `cloud-sync/README.md` 的 `cloud-sync/config.go:minFileSizeBytes` → `cloud-sync/internal/app/...` 更正为 `cloud-sync/internal/config/config.go:minFileSizeBytes`。
- [ ] 提交：`docs: update layout for internal package split`

## Task 16: 最终验证

- [ ] `cd cloud-sync && gofmt -l . && go vet ./... && go test -race -count=1 ./...`
- [ ] `CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o cloud-sync .`
- [ ] 用 CI ldflags 构建一次并确认 `/api/status` 的 `version` 为注入值。
- [ ] `./scripts/smoke.sh && ./scripts/smoke-config.sh` 输出 `OK`。
- [ ] `git push -u origin refactor/package-split`，开 PR 到 `dev`。
