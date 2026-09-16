# cloud-sync OpenList API alignment

> 日期：2026-09-15
> 状态：Draft（user 已在会话中批准：跳过策略默认 + 新增 overwrite 选项；对齐 OpenList v4.2.6+）
> 关联：[ARCHITECTURE.md](../../ARCHITECTURE.md) §7 §8.4；[cloudsync-design.md](../../specs/2026-09-14-cloudsync-design.md) §3.5 §7

## 1. 目标

让 `cloud-sync` 的 OpenList 客户端对接真实的 **OpenList v4.x** HTTP API。当前实现基于一份**错误**的 API 描述（ARCHITECTURE.md §7 / spec §3.5），对真 OpenList 几乎每个上传都会拿到 HTTP 400。修复同时把 ARCHITECTURE + spec 改对。

## 2. 现状摘要

详见会话内研究记录。关键事实：

| 项目 | 旧 spec 写 | 真实 OpenList v4 |
|---|---|---|
| Copy 请求体 | `{src_dir, src_name, dst_dir, dst_name}` | `{src_dir, dst_dir, names:[]string, overwrite, skip_existing, merge?}` |
| Copy 响应 | `{data:{task_id}}` | `{data:{message, tasks:[…TaskInfo]}}`（无 task=同步完成） |
| 任务状态查询 | `GET /api/admin/task/{id}/done` | `GET /api/admin/task/copy/undone\|done` 或 `POST …/info?tid=`；状态字段是 `state` (int) |
| 目标已存在（默认） | n/a | handler 直接 403 `file [X] exists` 不建任务 |
| `skip_existing=true` | n/a | 静默跳过该项，响应无 task |
| `overwrite=true` | n/a | 跳过检查；任务执行时 `op.Put` 默认覆盖 |
| 单文件目标路径 | 假定可指定 `dst_name` | `dst_dir` + 源 basename；嵌套靠把 src_dir 指到父目录 |

## 3. 决定

| 项 | 决定 |
|---|---|
| API 版本基线 | OpenList v4.2.6+（user 确认最新） |
| Copy 调用方式 | 单文件一次：`POST /api/fs/copy` `{src_dir=<父>, names:[base], dst_dir=<父>, skip_existing\|overwrite}` |
| 目录保留 | per-file：src_dir 指到 src 文件父目录，dst_dir 指到 dst 文件父目录 |
| dst 目录预先创建 | 不预创建；OpenList 异步任务递归创建 |
| 默认存在策略 | `skip_existing=true` |
| 覆盖选项 | 新增 `OPENLIST_OVERWRITE=true` env（→ `overwrite=true`） |
| 任务完成信号 | `state==2` (tache.StateSucceeded) |
| 任务失败信号 | `state==4` (canceled) 或 `state==7` (failed) |
| 任务进行中 | 其他 state → 继续 poll |
| 同步完成（无任务） | Copy 响应 `tasks` 为空 → `(taskID="", nil)` → pipeline 当 succeeded 处理 |
| auth | `Authorization: <token>`（raw token header） |

## 4. 范围

### 改
- `cloud-sync/openlist.go` — `Copy`、`TaskDone` 重写；保留 `Ping`。
- `cloud-sync/openlist_test.go` — 改测试覆盖新 API 形状。
- `cloud-sync/config.go` — 新增 `OpenListOverwrite`（env + YAML）。
- `cloud-sync/config_test.go` — 2 个新测试。
- `cloud-sync/pipeline.go` — `computeKey` 构造 `srcDir=父目录`, `srcName=basename`, `dstDir=父目录`, `dstName=basename`。
- `cloud-sync/pipeline_test.go` — 改 mock + assertions 适配 computeKey。
- `scripts/mock-openlist.py` — 真实 API 行为。
- `scripts/smoke.sh` + `scripts/smoke-config.sh` — 加 name/skip_existing 断言。
- `cloud-sync/config.example.yaml` — `openlist_overwrite: false`。
- `.env.example` + `docker-compose.yml` — `OPENLIST_OVERWRITE=false`。
- `cloud-sync/README.md` — env vars 表更新。
- `README.md` — env vars 表更新。
- `ARCHITECTURE.md` §7 §8.4 — 改 API 描述。
- `docs/superpowers/specs/2026-09-14-cloudsync-design.md` §3.5 §7 — 改 API 描述。

### 不改
- watcher / state / cleanup / main / 其它 config 字段 / 其它 tests。
- docker 部署流程。
- spec §10 backlog（断点续传 / 多账号等）。

## Task 1: 重写 `cloud-sync/openlist.go` 对齐真实 OpenList v4 API + 改 openlist_test.go

**Files:**
- Modify: `cloud-sync/openlist.go`
- Modify: `cloud-sync/openlist_test.go`

**接口（与 plan §5.1 / §5.2 对应）：**

`Copy` 签名扩展为 `Copy(ctx, srcDir, srcName, dstDir, dstName string, overwrite bool) (taskID string, err error)`；pipeline.go 调用点与 5 处测试 mock 同步改签名（Task 2 加 `cfg.OpenListOverwrite`，Task 3 接线）。内部：
- 把 `(srcDir, srcName, dstDir, dstName, overwrite bool)` 拆成 `POST /api/fs/copy` body `{src_dir, dst_dir, names:[srcName], skip_existing=!overwrite, merge:false}`。
- `overwrite=true` 时发 `overwrite:true, skip_existing:false`；否则 `overwrite:false, skip_existing:true`。
- 响应 `{code, message, data:{message, tasks:[{id, state, status, ...}]}}`：取 `data.tasks[0].id`；无 task → `("" , nil)`。
- `code != 200` 或 HTTP 非 2xx → error。

`TaskDone` 改：`TaskDone(ctx, taskID) (TaskStatus, error)`。
- `taskID == ""` → 直接 `TaskSucceeded, nil`。
- `taskID != ""` → `POST /api/admin/task/copy/info?tid=<taskID>`（body 空）。
- 响应 `data.state`：`2` → TaskSucceeded；`4 || 7` → TaskFailed；其他 → TaskPending。

`Ping` 不变。

### 测试

删掉现有 8 个测试，按 plan §6 写 12 个 httptest：

1. `TestClient_Ping_Success` — 200 code=200
2. `TestClient_Ping_NonZeroCode` — 200 code=500 → error
3. `TestClient_Ping_ServerError` — HTTP 500 → error
4. `TestClient_Copy_Success_SkipExistingDefault` — 默认 false overwrite → 发 `skip_existing:true, overwrite:false`；响应 tasks 含 1 个 task → 返回 taskID
5. `TestClient_Copy_OverwriteTrue` — overwrite=true → 发 `overwrite:true, skip_existing:false`
6. `TestClient_Copy_SkipExistingEmptyTasks` — `skip_existing:true` + 响应 `tasks:[]`（dst 已存在静默跳过）→ `("", nil)`
7. `TestClient_Copy_FileExistsReturnsError` — overwrite=false + skip_existing=false + dst 存在 → 403
8. `TestClient_Copy_EmptyNamesReturnsError` — server 返回 400 `Empty file names` → error（验证 client 不再发 src_name/dst_name）
9. `TestClient_Copy_HTTPError` — HTTP 502 → error
10. `TestClient_Copy_NonZeroCode` — code=500 → error
11. `TestClient_TaskDone_EmptyTaskID` — taskID=="" → TaskSucceeded
12. `TestClient_TaskDone_Succeeded` — state=2 → TaskSucceeded
13. `TestClient_TaskDone_Pending` — state=1 → TaskPending
14. `TestClient_TaskDone_Failed` — state=7 → TaskFailed
15. `TestClient_TaskDone_Canceled` — state=4 → TaskFailed
16. `TestClient_TaskDone_HTTPError` — HTTP 500 → error
17. `TestClient_TaskDone_NonZeroCode` — code=500 → error

测试约定：`newTestClient(t, srv) *Client` 沿用；httptest 自包含 handler。

实施顺序：先写测试覆盖新 API shape（看到 RED），再写实现（看到 GREEN）。

## Task 2: Config 加 `OpenListOverwrite`（env + YAML） + 2 个 config_test

**Files:**
- Modify: `cloud-sync/config.go`
- Modify: `cloud-sync/config_test.go`

`Config` 加 `OpenListOverwrite bool` 字段。

`fileConfig` 加 `OpenListOverwrite *bool \`yaml:"openlist_overwrite"\``（用 *bool 维持"unset 与 false 区分"语义，参考 `CleanupDryRun`）。

`loadFromEnv` 加：`cfg.OpenListOverwrite, err = envBool("OPENLIST_OVERWRITE", false)`。

`loadWithFile` 加：`if f.OpenListOverwrite != nil { os.Setenv("OPENLIST_OVERWRITE", strconv.FormatBool(*f.OpenListOverwrite)) }`。

### 测试

`config_test.go` 加：

- `TestLoad_FileOpenListOverwrite_OverridesEnv`：YAML true 覆盖 env false
- `TestLoad_FileOpenListOverwriteAbsent_FallsBackToEnv`：省略 → 走 env

## Task 3: `pipeline.computeKey` 适配新 OpenList 语义 + 改 pipeline_test

**Files:**
- Modify: `cloud-sync/pipeline.go`
- Modify: `cloud-sync/pipeline_test.go`

`computeKey(absPath) (key, srcDir, srcName, dstDir, dstName string)` 语义改为：

```
key   = 相对 WatchDir 父目录的 rel path（不变）
srcDir = SrcStorage + "/" + key 的父目录
srcName = key 的 basename
dstDir = DstStorage + "/" + key 的父目录
dstName = key 的 basename（同 srcName）
```

例：absPath = `/mnt/basic/media/media/Movies/X.mkv`，WatchDir=`/mnt/basic/media/media`，key=`Movies/X.mkv`，则：
- srcDir = `/local_media/media/Movies`
- srcName = `X.mkv`
- dstDir = `/139yun_media/media/Movies`
- dstName = `X.mkv`

parentDirOf：标准库 `filepath.Dir` 处理（用 `filepath.FromSlash` 后 `filepath.Dir`，再 `filepath.ToSlash`）。

`CloudPath` 字段构造：`<dstDir>/<dstName>`（slash 形式）。注意要去掉 `<DstStorage>/` 的重复前缀——直接用 `dstDir + "/" + dstName` 即可（`dstDir` 已含 DstStorage 前缀）。

### 测试

改 `pipeline_test.go`：

- `TestPipeline_HappyPath` 期望 `call.SrcDir = "/local_media/media/Movies"`（不再是 `/local_media`），`call.SrcName = "X.mkv"`，`call.DstDir = "/139yun_media/media/Movies"`，`call.DstName = "X.mkv"`。
- `TestPipeline_StartupScan_OnlyUnsynced` 同上：`SrcName = "media/Movies/A.mkv"` → `"A.mkv"`（basename）；`SrcDir = "/local_media/media/Movies"`。

修改 mock uploader：保留不变（mock 不关心 src_dir 内部结构，只记录调用）。

## Task 4: 重写 `scripts/mock-openlist.py` + smoke 加 name/skip_existing 断言

**Files:**
- Modify: `scripts/mock-openlist.py`
- Modify: `scripts/smoke.sh`
- Modify: `scripts/smoke-config.sh`

`mock-openlist.py` 行为（按真实 OpenList 实现）：

- `POST /api/fs/list` body `{path, page, per_page}`：返回 `{code:200, message:"success", data:{content:[]}}`（空 listing；Ping 用）
- `POST /api/fs/copy` body `{src_dir, dst_dir, names[], overwrite, skip_existing, merge?}`：
  - 解析 JSON；若 `names` 为空 → `{code:400, message:"Empty file names"}`
  - 否则检查 `dst_dir/<base(names[0])>` 是否存在：
    - 存在 + `overwrite=false` + `skip_existing=false` → `{code:403, message:"file [<base>] exists"}`（handler-level 行为）
    - 存在 + `skip_existing=true` → `{code:200, message:"success", data:{message:"skipped", tasks:[]}}`（无 task，pipeline 当 succeeded）
    - 否则 → 拷贝 `src_dir/<names[0]>` 到 `dst_dir/<base(names[0])>` → `{code:200, message:"success", data:{message:"created 1 task(s)", tasks:[{id:"task-<n>", name:"copy", creator:"", creator_role:0, state:2, status:"succeeded", progress:100, start_time:..., end_time:..., total_bytes:N, error:""}]}}`
- `POST /api/admin/task/copy/info?tid=X` body（任意）：
  - tid 解析为 `task-<n>`：`n` 偶数 → state=2, status="succeeded"；奇数 → state=7, status="failed"
  - 返回完整 TaskInfo

删除旧的 `/admin/task/{id}/done` 路由。

`smoke.sh` + `smoke-config.sh`：在末尾加一个断言 — 用 grep 或 cat 看 mock stdout 的 `request log`（mock 现在记所有收到的请求），验证请求里出现 `names`、`skip_existing: true`、不出现 `src_name`。

具体断言方式（最小）：mock 启动时把每个请求的 body 追加到 `<WS>/mock.log`，smoke 脚本 grep mock.log 检查关键字。

## Task 5: 文档 + 配置示例同步

- `ARCHITECTURE.md` §7（请求示例 + 状态查询）+ §8.4 — 改写
- `docs/superpowers/specs/2026-09-14-cloudsync-design.md` §3.5 + §7 — 改写
- `cloud-sync/config.example.yaml` — 加 `openlist_overwrite: false`（带注释）
- `cloud-sync/README.md` — env vars 表加 `OPENLIST_OVERWRITE`；故障排查新增条目；本地测试章节如有误同步
- `README.md`（仓库根）— env vars 表加 `OPENLIST_OVERWRITE`
- `.env.example`（仓库根）— 加 `OPENLIST_OVERWRITE=false`
- `docker-compose.yml` — cloud-sync service 的 environment 加 `OPENLIST_OVERWRITE=false`

## 5. 详细设计参考

### 5.1 `OpenList.Copy` 内部

```go
func (c *Client) Copy(ctx, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error) {
    body := map[string]any{
        "src_dir":  srcDir,
        "dst_dir":  dstDir,
        "names":    []string{srcName},
        "overwrite":    overwrite,
        "skip_existing": !overwrite,
        "merge":        false,
    }
    // POST /api/fs/copy, parse data.tasks[0].id; "" if empty
}
```

`overwrite` 来自 `cfg.OpenListOverwrite`。注意：`overwrite=false, skip_existing=true` 时 dst 存在 → 静默跳过（响应 tasks=[]）；`overwrite=true, skip_existing=false` → 不检查存在 → 任务执行时覆盖。

### 5.2 `OpenList.TaskDone`

```go
func (c *Client) TaskDone(ctx, taskID string) (TaskStatus, error) {
    if taskID == "" { return TaskSucceeded, nil }
    // body 空；POST /api/admin/task/copy/info?tid=<taskID>
    // 响应 data.state int → map to TaskStatus
}
```

### 5.3 `pipeline.computeKey`

```go

func (p *Pipeline) computeKey(absPath string) (key, srcDir, srcName, dstDir, dstName string) {
    var root string
    for _, r := range p.cfg.WatchDirs {
        if hasPrefix(absPath, r) { root = r; break }
    }
    if root == "" { ... return "", "", "", "", "" }

    rel := filepath.ToSlash(filepath.Rel(root, absPath))
    key = rel

    parentSlash := path.Dir(rel)  // path.Dir on slash-separated
    if parentSlash == "." { parentSlash = "" }

    // Both watch dirs share a single "media" subdir on the OpenList storage,
    // so we append it here and keep OPENLIST_*_STORAGE as the bare root.
    const sub = "media"
    srcName = path.Base(rel)
    srcDir = p.cfg.SrcStorage + "/" + sub
    if parentSlash != "" { srcDir = srcDir + "/" + parentSlash }
    dstName = srcName
    dstDir = p.cfg.DstStorage + "/" + sub
    if parentSlash != "" { dstDir = dstDir + "/" + parentSlash }
    return
}
```

注意用 `path.Dir` / `path.Base`（slash 路径）而不是 `filepath.Dir`（系统路径）。

### 5.4 CloudPath

在 `uploadWithRetry` 写 `StatusRecord.CloudPath` 时：`<DstStorage>/<dstName>` 当 key 无父目录；否则 `<DstStorage>/<parentDir>/<dstName>`。

### 5.5 mock 请求日志

为让 smoke 脚本能断言请求内容，mock 在每个请求处理时 append 到 `<WS>/mock.log`：
```
2026-09-15T01:00:00 POST /api/fs/copy {"src_dir":"/local_media/media","dst_dir":"/139yun_media/media","names":["SmokeTest.mkv"],...}
```

## 6. 风险

| 风险 | 缓解 |
|---|---|
| OpenList v3 vs v4 差异 | user 确认 v4.2.6 最新；按 v4.2.6 源码 |
| 嵌套结构拷贝依赖 dst 目录自动创建 | OpenList `RunWithNextTaskCallback` 对 dir 递归创建子目录 |
| `Merge=true` 与 `skip_existing=true` 冲突 | 我们 Merge=false，只发 overwrite 或 skip_existing |
| `skip_existing=true` 响应无 task → `taskID=""` 路径 | mock + 单测明确覆盖 |

## 7. 不在范围

- 增量同步 / 断点续传（spec backlog）
- Webhook / 多账号
- Alist v3 兼容
