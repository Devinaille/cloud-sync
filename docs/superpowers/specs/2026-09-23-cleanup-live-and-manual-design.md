# 清理实时到期 + 单文件手动清理 —— 设计文档

- 日期：2026-09-23
- 状态：已批准，待实施
- 关联：`ARCHITECTURE.md` 清理章节、`internal/{config,state,cleanup,pipeline,httpapi}`

## 1. 背景

清理到期时间在同步成功时写死为 `SyncedAt + 当时的 CleanupAfter`（`pipeline.writeSynced`），并持久化到记录 `CleanupAt`。`state.ListForCleanup` 以 `CleanupAt.Before(now)` 选取。因此之后修改 `cleanup_after_hours` 对已同步文件不生效。

## 2. 目标

1. 到期时间改为**实时计算**：`SyncedAt + 当前 cleanup_after_hours`，改配置后立即影响已有文件。
2. 清理触发间隔可配置：新增 `CLEANUP_INTERVAL_SECONDS`，默认 3600，**最小 300**。
3. Files 明细每行新增「清理」按钮（重试左侧），仅 `synced` 可用，遵守 `cleanup_dry_run`。

## 3. 设计

- `state.ListForCleanup(now, after)` 改为 `synced && now >= SyncedAt+after`；`state` 不依赖 config，duration 由调用方传入。
- `cleanup.Run` 用 `cfg.CleanupInterval` 作 ticker。
- `/api/files` 的 `cleanup_at` 按当前 `CleanupAfter` 实时计算（synced 记录）；持久化 `CleanupAt` 降级为"同步时快照"，不再作为依据。
- 新增 `state.Get(key)` 与 `cleanup.CleanKey(ctx,key)`；`POST /api/cleanup/file` 暴露单文件清理。

## 4. 约束/风险

- 调小 `cleanup_after_hours` 会让一批已同步文件在下一个 tick（≤ 间隔）到期并删除（每次≤50）。建议先用 `cleanup_dry_run` 演练。
- dry-run 只记日志、不置 cleaned，会随 tick 重复记录（既有行为）。
- `CLEANUP_INTERVAL_SECONDS` 过小会频繁遍历状态目录；下限 300s。

## 5. 非目标

- 不改云盘侧（139）行为；不改 `cleanup_dry_run` 的重复记录问题。
