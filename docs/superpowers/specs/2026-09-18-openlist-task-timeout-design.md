# OpenList 异步复制任务超时处理 —— 设计文档

- 日期：2026-09-18
- 状态：待评审（仅设计，未改代码）
- 关联：`ARCHITECTURE.md` §7.2 / §8.4 / §11.1、`docs/superpowers/specs/2026-09-14-cloudsync-design.md` §4/§5

## 1. 现象

线上出现日志：

```
Post "http://10.10.10.10:5244/api/admin/task/copy/info?tid=oH8jmcHS9qU4NYPUX_sEU": context deadline exceeded
```

但此时 OpenList 仍在传输该文件。即：cloud-sync 侧判定/处理与实际云盘状态不一致。

## 2. 证据链（当前实现）

1. `TASK_TIMEOUT_SECONDS` 默认 `1800`（30 分钟，`config.example.yaml:71`）。
2. `internal/pipeline/pipeline.go:209` 用 `TaskTimeout` 给**整个 Copy + 轮询**套了一个 deadline：

   ```go
   taskCtx, cancel := context.WithTimeout(ctx, p.cfg.TaskTimeout)
   taskID, err := p.up.Copy(taskCtx, ...)   // :210
   ...
   st, err := p.up.TaskDone(taskCtx, taskID) // :226 同一个 taskCtx
   ```

3. 文件大 / OpenList 复制慢，`taskCtx` 到期后 `TaskDone` 返回错误。报错无 `Client.Timeout exceeded while awaiting headers` 后缀，说明来自 `taskCtx`，**不是** `internal/openlist/openlist.go:33` 的 `http.Client{Timeout: 30s}`。
4. `internal/pipeline/pipeline.go:227-231`：`TaskDone` 一旦返回错误即 `break` 跳出轮询，走 `RETRY`（`:264`）→ **重新 `Copy`**（`:210`）。
5. `openlist.Client` 没有任何"取消任务"调用，服务端异步任务不会因 HTTP 断开而停止；重新 `Copy` 会为同一 src→dst **再排一个任务**（`skip_existing` 只在目标已存在时生效，传输中未必生效）。
6. 重试 3 次后 `uploadWithRetry` 返回错误；`process` 见父 `ctx` 仍存活 → `writeFailed` 写 `failed` 记录（`pipeline.go:327`，`process` 约 `:148`）。

### 2.1 OpenList 任务状态映射（`openlist.go:170-177`）

| state | 含义（ARCHITECTURE §7.2） | 当前映射 |
|---|---|---|
| 2 | succeeded | `TaskSucceeded` |
| 4 | canceled | `TaskFailed` |
| 7 | failed（重试用尽） | `TaskFailed` |
| 5 | errored（将重试） | `TaskPending`（继续轮询） |
| 8/9 | waiting_retry / before_retry | `TaskPending`（继续轮询） |
| 其他 | running / canceling / failing | `TaskPending` |

状态映射本身正确；问题出在**轮询的传输错误**与**任务终态**被混为一谈。

## 3. 根因

> `uploadWithRetry` 把"轮询请求的传输错误/超时"当成"任务失败"，并通过重新调用 `Copy` 来重试，从而在服务端产生重复复制，并最终在任务实际仍在进行时写入 `failed`。

`TASK_TIMEOUT_SECONDS` 同时被用于"同步 Copy 请求预算"和"整个异步任务预算"，语义混用是促成因素；`ARCHITECTURE.md` §11.1 #5 已把"大文件 copy 超时"列为待解决风险。

## 4. 影响

- 云盘可能同时存在**重复传输**，浪费带宽、可能触发驱动限流。
- 文件被写 `failed`，而云盘实际可能已完成或即将完成；`AlreadySynced` 对任何状态都为真，会阻塞后续处理，直到用户手动重试。
- 大文件（>30 分钟复制）**必然**触发该路径。

## 5. 目标 / 非目标

**目标**
- 轮询的传输错误不再触发重复 `Copy`。
- 超时/失败语义明确、可预期，且不产生重复传输。
- 保持既有可配置项与默认行为尽量兼容。

**非目标**
- 不实现 OpenList 任务取消（除非确认存在可用端点，见 §9）。
- 不改变文件选择、白名单、stabilize、cleanup 等其他逻辑。

## 6. 方案对比

### 方案 A：轮询错误继续轮询同一 taskID（核心修复）
`TaskDone` 返回非终态错误时，不再 `break`/重试，而是按 `PollInterval` 继续轮询**同一个** `taskID`；仅当任务进入终态、整体超时或父 ctx 取消才结束。
- 优点：直接消除重复 `Copy`；符合"异步任务在服务端继续"的事实。
- 缺点：若 OpenList 长时间不可达，会持续轮询；需要一个终止条件。

### 方案 B：拆分超时语义
`TaskTimeout` 只约束单次 HTTP 调用（Copy 请求），异步任务改用独立、更长的轮询预算（或直到父 ctx 取消）。
- 优点：语义清晰。
- 缺点：改变已文档化行为（§8.4"任务超时→FAILED"）；需要新配置项，兼容性成本高。

### 方案 C：重试前用 `Exists` 去重
`Copy` 报错后先查 `Exists(dstPath)`，存在则视为成功，否则才重试。
- 优点：低成本降低重复。
- 缺点：不解决"轮询超时误判失败"的根因；`Exists` 与进行中的传输存在竞态。

### 方案 D：维持现状，仅调大默认值
把 `task_timeout_seconds` 调大。
- 优点：零代码。
- 缺点：治标；超时后仍会重试并重复复制。

## 7. 推荐设计（A + C 组合）

### 7.1 错误分类

在 `uploadWithRetry` 中区分三类结果：

1. **`Copy` 请求失败**（`Do` 返回错误 / 非 2xx / 解析失败）：此时**不确定**服务端是否已排任务。
   - 先 `Exists(dstPath)`：
     - 存在 → 视为成功（写 `synced` 记录）。
     - 不存在 → 指数退避后重试 `Copy`（受 `maxAttempts` 限制）。
2. **`TaskDone` 传输错误 / 超时**（`errors.Is(err, context.DeadlineExceeded)` 或网络错误）：**不是任务失败**。
   - 不重新 `Copy`；记录告警，按 `PollInterval` 继续轮询**同一 taskID**。
3. **`TaskDone` 终态**：`succeeded` → 写 `synced`；`failed` → 允许重试 `Copy`（真实失败，`maxAttempts` 限制）。

### 7.2 超时与取消语义

- `Copy` 请求：`context.WithTimeout(ctx, TaskTimeout)`（同步请求预算，不变）。
- 轮询：使用父 `ctx`（每个 HTTP 调用已被 client 的 30s 超时约束），并记录 `taskStart := time.Now()`。
- **任务总预算**：`time.Since(taskStart) > TaskTimeout` → 返回 `errTaskTimeout`，**不重新 Copy**；由 `process` 写 `failed`（保持 §8.4 行为）。
- 父 `ctx` 取消 → 返回 `ctx.Err()`；`process` 因 `ctx.Err() != nil` 不写 `failed`（保持既有"重启不算真失败"语义）。

### 7.3 行为伪码（示意）

```
for attempt := 1..maxAttempts:
    ctxCopy, cancel := WithTimeout(ctx, TaskTimeout)
    taskID, err := Copy(ctxCopy, ...)
    cancel()
    if err != nil:
        if exists, _ := Exists(dst); exists: return success
        if attempt < maxAttempts: backoff; continue
        return err
    start := now()
    for:
        if err := ctx.Err(); err != nil: return err          // 父取消，不写 failed
        if time.Since(start) > TaskTimeout: return errTaskTimeout // 超时，不重试
        st, err := TaskDone(ctx, taskID)                     // 客户端 30s 超时
        switch:
          err != nil:      log warn; sleep(PollInterval); continue   // 继续轮询同一 taskID
          st == succeeded: write synced; return nil
          st == failed:    goto RETRY_COPY                     // 真实失败才重试 Copy
          st == pending:   sleep(PollInterval); continue
RETRY_COPY:
    if attempt < maxAttempts: backoff; continue
    return lastErr
```

### 7.4 失败落盘与恢复

- 超时写 `failed`（含 `OpenListTaskID`，便于排查），与 §8.4 一致。
- 用户手动重试：`httpapi.handleRetry` 先 `st.Delete(key)` 再 `ProcessOne`（`api_actions.go:76,96`），配合 §7.1 的 `Exists` 去重，避免已传完的文件重复传。

## 8. 测试计划（待实现时先写失败测试）

在 `internal/pipeline` 增加：

1. **轮询瞬时错误不重新 Copy**：`TaskDone` 连续返回错误 K 次后返回 `succeeded` → 断言 `Copy` 只被调用 1 次且最终 `synced`。（当前实现会失败：要么重 Copy，要么 3 次后写 failed）
2. **总超时后不重复 Copy**：`TaskDone` 一直 `pending`，`TaskTimeout` 到期 → 断言 `Copy` 调用 1 次、返回超时、写 `failed`。
3. **Copy 失败且目标已存在 → 成功**：`Copy` 返回错误、`Exists` 返回 true → 断言不重试、写 `synced`。
4. **父 ctx 取消不写 failed**：沿用/扩展 `TestPipeline_NoFailedRecordOnParentCancel`。
5. **真实 TaskFailed 仍会重试 Copy**：保持现有重试覆盖。

测试替身：需扩展 `internal/mockopenlist` 以支持"`TaskDone` 返回指定错误""按次数返回错误"等能力（新增字段，不改变现有语义）。

## 9. 开放问题

1. `TaskTimeout` 是否应继续代表"单任务总预算"？若要区分，是否新增 `poll_timeout_seconds`（属方案 B）？
2. 整体超时但服务端仍在传输时：写 `failed`（现状/规范）还是继续轮询到终态（更省事但可能长时间占用）？
3. OpenList 是否提供取消任务端点（如 `/api/admin/task/copy/cancel`）？若有，可在放弃时取消服务端任务，从根本避免"仍在传"。
4. `Exists` 判定为成功是否足够可靠（例如目标为目录、或驱动写入临时文件后改名）？需要针对 139yun 驱动验证。

## 10. 文档更新（实现时同步）

- `ARCHITECTURE.md` §7.2：补充"轮询传输错误继续轮询同一任务"。
- `ARCHITECTURE.md` §8.4：明确"任务超时→FAILED，且不重复发起 Copy"。
- `config.example.yaml` / `README`：说明 `task_timeout_seconds` 的确切含义（单任务总预算）。
