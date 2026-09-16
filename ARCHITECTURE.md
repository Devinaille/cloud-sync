# 云盘化媒体库 · 架构设计文档

> 目标：BT 下载 → 本地整理/刮削 → 自动上传到移动云盘 → 本地清理 → Emby 通过 STRM 播放云盘文件
> 版本：v3.1（解耦版 · 增量迁移 · OpenList 同主机）
> 最后更新：2025-09-14

---

## 0. 关键事实

- **全部组件统一部署在 TrueNAS 的 docker compose**（包括 OpenList）
- `/mnt/basic/media` 是 **TrueNAS 上的 ZFS 数据集**，被 docker compose 原生挂载为本地路径（**不涉及 NFS**）
- **完全不使用 MP 自定义插件**，cloud-sync 是独立容器，通过 inotify 监听文件系统
- **139strm 独立 cron 扫描云盘**，不接收任何推送通知
- ani-rss 路径**不经过 MP**，由 cloud-sync 统一处理上传
- 迁移策略：**增量迁移**，只管新下载，存量保持原状
- 同步状态标记写入 `/mnt/basic/media/.sync_status/`（在数据集内）
- 本地源文件保留 **72 小时**过渡期后由 cloud-sync 清理
- **所有云盘操作一律走 OpenList API**（读/写/列/删/查），禁止 rclone / sftp / scp / 直连云盘 API 等旁路；判断成功的唯一标准是 OpenList API 返回值

---

## 1. 资产清单与拓扑

```
┌─────────────────────────────────────────────────────────────┐
│                     TrueNAS (host)                          │
│                                                             │
│  /mnt/basic/media/  (ZFS dataset)                           │
│                                                             │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Docker Compose 栈 (单 host · 同网络)                │  │
│  │                                                      │  │
│  │  · qBittorrent      ──写入──► downloads/             │  │
│  │  · Transmission     ──写入──► downloads/             │  │
│  │  · MoviePilot v2    ──读取── downloads/              │  │
│  │  ·  (内置资源转移)   ──写入── media/                 │  │
│  │  · ani-rss          ──写入── ani-rss/                │  │
│  │  · cloud-sync       ◄──监听── media/ + ani-rss/      │  │
│  │  ·  (含 cleanup)    ──写入── .sync_status/           │  │
│  │  ·                  ──删除── 72h 后源视频            │  │
│  │  · OpenList         ◄──API──  cloud-sync             │  │
│  │  ├─ /local_media  (local → /mnt/basic/media)        │  │
│  │  └─ /139yun_media (139yun → 移动云盘)                │  │
│  │  · 139strm          ──写入── strm_media/             │  │
│  │  · Emby             ◄──读取── strm_media/            │  │
│  │    + MediaInfoKeeper  (plugin)                        │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                             │
└────────────────────────────┬────────────────────────────────┘
                             │
                  ┌──────────▼──────────┐
                  │       移动云盘       │
                  └─────────────────────┘
```

### 1.1 关键路径

| 路径 | 归属 | 用途 |
|---|---|---|
| `/mnt/basic/media/downloads/` | TrueNAS 数据集 | qB / Transmission 下载输出 |
| `/mnt/basic/media/ani-rss/` | TrueNAS 数据集 | ani-rss 下载输出 |
| `/mnt/basic/media/media/` | TrueNAS 数据集 | MP 资源转移 + 刮削后输出 |
| `/mnt/basic/media/strm_media/` | TrueNAS 数据集 | 139strm 输出，Emby 库根 |
| `/mnt/basic/media/.sync_status/` | TrueNAS 数据集 | cloud-sync 同步状态标记 |
| `/d/139yun/media/` | 移动云盘 | 最终存储 |

---

## 2. 完整数据流

### 2.1 路径 A：qB / Transmission → MP → cloud-sync → 云盘

```
[qB / Transmission]
   │  下载完成
   ▼
[/mnt/basic/media/downloads/]
   │  MP v2 内置"资源转移"功能自动接管（无需任何插件）
   │  · 定时扫描 downloads/ 新文件
   │  · 刮削 (TMDB)
   │  · 按模板重命名
   │  · 移动到 /mnt/basic/media/media/
   ▼
[/mnt/basic/media/media/]
   │  inotify IN_CLOSE_WRITE / IN_MOVED_TO 事件
   ▼
[cloud-sync 容器]
   │  1. 等文件静止 30s（inotify 持续监控 size/mtime）
   │  2. 校验 .nfo 存在（路径 A 强制要求）
   │  3. 校验 .sync_status 不存在
   │  4. 计算相对源目录的路径，保留完整目录结构
   │  5. 调 OpenList: copy /local_media/media/<rel> → /139yun_media/media/<rel>
   │  6. 轮询 OpenList task 状态 = succeeded（**唯一成功标准**）
   │  7. 写 .sync_status 标记
   ▼
[72h cleanup（cloud-sync 内置子任务）]
   │  删除 .mkv/.mp4/.ts 源文件
   │  保留 .nfo / .jpg / 字幕
   ▼
[139strm 容器 (独立 cron)]
   │  每 5-10 分钟扫云盘
   │  生成 STRM 到 /mnt/basic/media/strm_media/
   ▼
[Emby + MediaInfoKeeper]
   │  实时监控 strm_media/ 变化
   │  首次扫：MediaInfoKeeper 缓存元数据
   │  后续扫：直接读缓存（不访问云盘）
```

### 2.2 路径 B：ani-rss → cloud-sync → 云盘（不经过 MP）

```
[ani-rss]
   │  下载 + 基础重命名
   ▼
[/mnt/basic/media/ani-rss/]
   │  inotify 事件触发
   ▼
[cloud-sync 容器]
   │  1. 等文件静止 30s
   │  2. 不要求 .nfo（ani-rss 路径不刮削）
   │  3. 调 OpenList: copy /local_media/ani-rss/X → /139yun_media/ani-rss/X
   │  4. 写 .sync_status
   │  5. 72h 后清理源
   ▼
[139strm]
   │  生成 STRM
   ▼
[Emby]
   │  自行刮削（无 NFO）；MediaInfoKeeper 缓存
```

### 2.3 两路径差异点

| 维度 | 路径 A (MP) | 路径 B (ani-rss) |
|---|---|---|
| 源目录 | `media/` | `ani-rss/` |
| 是否需要 .nfo | **强制要求** | 不强制 |
| 重命名模板 | MP 模板统一处理 | ani-rss 自带模板 |
| 刮削器 | MP (TMDB/Bangumi) | Emby 自行刮削（首次） |
| Emby 元数据来源 | 直接读 NFO | Emby 内置刮削器 → MediaInfoKeeper 缓存 |

---

## 3. MediaInfoKeeper 在架构中的角色

### 3.1 解决什么问题

STRM 文件本质是**指针**（内容是一行 URL）。每次 Emby 扫库：
- 要么从 URL 抓元数据（云盘限速、慢）
- 要么每次重抓 metadata（带宽浪费）

**MediaInfoKeeper** 的作用：第一次扫描时持久化元数据到 Emby 数据库；后续扫描直接读缓存，**不再访问云盘**。

### 3.2 集成位置

```
Emby 扫库扫描 /mnt/basic/media/strm_media/
   │
   ▼
发现 Interstellar.strm (含移动云盘 URL)
   │
   ├─► MediaInfoKeeper 拦截 ◄── 缓存命中？ ──► 直接用本地缓存
   │                                            │
   │                                            └─ 否 ──► 原生 Emby 刮削器
   │                                                       │
   │                                                       ├─ 有 NFO ──► 用 NFO
   │                                                       │
   │                                                       └─ 无 NFO ──► 抓 STRM 头部探测
   │
   └─► 元数据持久化到 Emby DB
```

### 3.3 核心收益

1. STRM 模式下 Emby 库扫速度**从分钟级降到秒级**
2. 减少对云盘的元数据 API 请求（防风控）
3. 即使云盘限速/账号异常，Emby 库依然完整可用

### 3.4 配置要点

- 安装：把 `MediaInfoKeeper.dll` 放进 Emby 的 `plugins/`
- 重命名严格为 `MediaInfoKeeper.dll`（避免自动更新出两份）
- 适配 Emby 4.10.0.40（最新）
- 关闭 `Save artwork into media folders`
- 开启 `Extract chapter images during library scan`

---

## 4. 139strm 独立运行

### 4.1 与其他组件的关系

139strm 在 v3 中**完全独立**，不接收任何事件通知：
- **不监听** `strm_media/` 目录变化
- **不订阅** cloud-sync 通知
- **不依赖** MP / qB / Transmission

它**自跑 cron 任务**（每 5-10 分钟）扫描移动云盘的指定目录，生成 STRM 到 `/mnt/basic/media/strm_media/`。

### 4.2 延迟容忍

- 上传完成 → cloud-sync 标记同步 → 139strm 下次扫描发现新文件 → 生成 STRM
- **最大延迟**：cron 周期 + 139strm 单次扫描耗时
- 建议 cron 周期 5 分钟，单次扫描 1-3 分钟（取决于云盘文件数）
- **总延迟通常 < 10 分钟**，对播放体验无影响

### 4.3 配置文件要点

- 同步源：移动云盘 `/media/` 路径（与 OpenList 上传目标一致）
- STRM 输出：`/mnt/basic/media/strm_media/`
- **保留 NFO 和海报**一并下载到本地
- cron 周期：300 秒
- 启动后立即执行一次（避免冷启动延迟）

### 4.4 文件操作分层原则

本系统对**云盘**的所有操作（读、写、列、删、查）**一律走 OpenList API**，禁止任何 rclone / sftp / scp / 直连云盘 API 等旁路。

| 操作类型 | 云盘侧 | 本地侧 |
|---|---|---|
| 读 | OpenList `POST /api/fs/list`（Ping）/ `/api/fs/get` | 直接本地 FS（inotify/stat/read） |
| 写 | OpenList `POST /api/fs/copy` | 直接本地 FS（仅 cleanup 写 `.sync_status/`） |
| 删 | （本架构不删云盘文件，保留历史） | 直接本地 FS（cleanup 子任务） |
| 状态查询 | OpenList `POST /api/admin/task/copy/info?tid={id}`（`state==2` 为成功） | 本地 FS（`.sync_status/`） |

**判断成功的唯一标准**：OpenList API 返回值。
- 云盘文件是否存在：`/api/fs/list` 或 `/api/fs/get`
- 上传是否完成：`/api/fs/copy` 返回的 task 经 `/api/admin/task/copy/info?tid=...` 查询 `state==2`
- **绝不**通过本地文件存在性推断云盘状态

> 本原则贯穿所有章节：cloud-sync 上传、cleanup 删除判定、人工排查都遵守此约束。

---

## 6. OpenList 配置（同主机本地存储）

### 6.1 存储配置

OpenList 与 cloud-sync 在同一 docker compose 内，通过 `media` 网络互通。**OpenList 不需要 NFS 中转**，直接挂载 TrueNAS 数据集。

| 存储类型 | OpenList 挂载路径 | 后端实际路径 |
|---|---|---|
| 本地存储 (local) | `/local_media` | `/mnt/basic/media` |
| 139yun | `/139yun_media` | 移动云盘账号根目录 |

**不勾选**：WebDAV 启用、本地挂载（fusermount）

### 6.2 路径一致性（同主机天然一致）

所有容器在同一 TrueNAS host，路径无需映射：

```
cloud-sync 看到的：/mnt/basic/media/media/Movies/X.mkv
OpenList 看到的：  /mnt/basic/media/media/Movies/X.mkv  (同一 host)
                  → 映射为 /local_media/media/Movies/X.mkv
```

### 6.3 启动顺序

同主机内由 docker compose 统一编排，无需手工干预启动顺序。

---

## 7. OpenList 存储间复制方案

### 7.1 上传调用方式

**`POST /api/fs/copy`（推荐）**：OpenList v4 的 `copy` 接受「源目录 + 条目名列表 + 目标目录」。**没有** `src_name` / `dst_name` 字段（早期设计文档描述有误，已修正）。

```
POST /api/fs/copy
Headers:
  Authorization: <openlist_admin_token>
  Content-Type: application/json
Body:
  {
    "src_dir": "/local_media/media/Movies/Interstellar (2014)",
    "dst_dir": "/139yun_media/media/Movies/Interstellar (2014)",
    "names": ["Interstellar.mkv"],
    "overwrite": false,
    "skip_existing": true,
    "merge": false
  }
```

- `names` 是**源目录下**的条目名（文件 basename 或子目录名）。
- 单文件上传：`src_dir` 指到文件所在目录，`names` 只放 basename。
- `skip_existing=true`（默认）：目标已存在同名文件 → 静默跳过该条目，**不建任务**。
- `overwrite=true`：跳过存在性检查，任务执行时覆盖目标（`mergo` 会 `SetExist`）。
- `merge=true` 仅用于目录合并。
- 目标目录随任务执行自动递归创建，无需预先 `mkdir`。

**响应**（异步任务数组；同存储同步完成时 `tasks` 为空）：

```json
{
  "code": 200,
  "message": "success",
  "data": {
    "message": "created 1 task(s)",
    "tasks": [
      { "id": "abc123", "state": 2, "status": "succeeded", "progress": 100.0, "error": "" }
    ]
  }
}
```

**优势**：
- 文件**不被流式传输**，OpenList 内部走"本地读 → 云盘写"
- 一次本地读 + 一次云盘写，无中间网络拷贝
- 支持大文件，驱动层自动分片
- 支持断点续传

### 7.2 任务轮询

`/api/fs/copy` 对**跨存储**复制返回异步任务（`data.tasks[0].id`）。轮询：

```
POST /api/admin/task/copy/info?tid=<task_id>
  → 200 { "code": 200, "data": { "id": "abc123", "state": 2, "status": "succeeded", "error": "" } }
```

`state` 是 OpenList 内部 `tache.State`：

| state | 含义 | cloud-sync 动作 |
|---|---|---|
| 0 | pending | 继续轮询 |
| 1 | running | 继续轮询 |
| 2 | succeeded | **成功** |
| 3 | canceling | 继续轮询 |
| 4 | canceled | **失败** |
| 5 | errored（将重试） | 继续轮询 |
| 6 | failing | 继续轮询 |
| 7 | failed（重试用尽） | **失败** |
| 8 | waiting_retry | 继续轮询 |
| 9 | before_retry | 继续轮询 |

cloud-sync 间隔 `POLL_INTERVAL_SECONDS`（默认 3s）轮询，单任务超时 `TASK_TIMEOUT_SECONDS`（默认 1800s）。

> 同存储复制可能**同步完成**（响应 `tasks` 为空），此时直接视为成功。

### 7.3 上传成功判定（OpenList API 为唯一标准）

**成功标志**（任一）：
1. `/api/fs/copy` 响应 `data.tasks` 为空（同存储同步完成，或 `skip_existing` 命中已有文件）；
2. `/api/fs/copy` 返回 `data.tasks[0].id` 后，轮询 `POST /api/admin/task/copy/info?tid=` 得到 `state == 2`。

**不做二次校验**：不再调 `/api/fs/list` 或 `/api/fs/get` 校验云盘文件存在性。OpenList 是云盘交互的唯一权威入口，**其 API 声明成功即视为成功**。

如需"云盘文件存在性"作为二次确认（如人工排查），单独调：
```
POST /api/fs/list   body: { "path": "/139yun_media/media/Movies/Interstellar (2014)" }
```
但**不纳入自动化逻辑**。

---

## 8. cloud-sync 容器设计

### 8.1 职责

- 监听 `/mnt/basic/media/media/` 和 `/mnt/basic/media/ani-rss/`
- 检测到新视频文件 → **计算相对源目录的完整路径**（如 `Movies/Interstellar (2014)/Interstellar.mkv`）→ 调 OpenList 上传（**保留完整目录结构**到 `/d/139yun/media/...`）→ 标记已同步
- 72h 后清理本地源视频（保留 nfo/jpg）
- **不删除** NFO / 海报 / 字幕 / 目录

### 8.2 监听策略

```
inotify 监听:
  /mnt/basic/media/media/   (递归)
  /mnt/basic/media/ani-rss/ (递归)

关注事件:
  IN_CLOSE_WRITE  (文件写完关闭)
  IN_MOVED_TO     (MP 转移过来)

过滤:
  *.mkv / *.mp4 / *.ts / *.iso
  文件大小 > 100MB (过滤下载中的临时文件)

去重:
  .sync_status/<date>/<relpath>.json 存在则跳过
```

### 8.3 上传状态机

```
[DETECTED]   inotify 触发
   │
   ▼
[STABILIZING]  每 5s 检查 size/mtime，30s 内无变化
   │
   ▼
[VALIDATE]
  ├─ 路径白名单 (ALLOWED_SOURCE_PREFIXES)
  ├─ .sync_status 不存在
  └─ 文件大小 > MinFileSize (100MB)
   │
   ▼
[COMPUTE_REL_PATH]  计算相对源根的完整目录结构
  rel = <rel_path>     e.g. "Movies/Interstellar (2014)/Interstellar.mkv"
  parent = path.Dir(rel)
   │
   ▼
[ENQUEUE]  本地任务队列（防止并发太多，默认并发 2）
   │
   ▼
[UPLOADING]  POST /api/fs/copy
  src_dir=<SrcStorage>/media/<parent>, names=[<basename>]
  dst_dir=<DstStorage>/media/<parent>
  skip_existing=true（默认；OPENLIST_OVERWRITE=true 时改发 overwrite=true）
  → 拿到 tasks[0].id（响应 tasks 为空 = 同步完成 / 已跳过）
   │
   ▼
[POLLING]   POST /api/admin/task/copy/info?tid=<id>  (默认 3s 间隔)
  state==2        → 进入下一步
  state==4 || 7   → 走错误处理
  其他             → 继续轮询
   │
   ▼
[MARK_SYNCED]  写 .sync_status
  (OpenList API 返回值是唯一成功标准，不再做 list/get 二次校验)
   │
   ▼
[DONE]      72h 后由 cleanup 子任务删除本地源
```

### 8.4 错误处理

| 错误 | 重试策略 |
|---|---|
| OpenList 连接失败 | 指数退避 1s, 2s, 4s, 8s, ... 最大 60s |
| OpenList 返回 5xx | 同上 |
| 移动云盘限流 (139yun 内部) | OpenList 自动重试（驱动层），cloud-sync 等待 |
| 任务超时 (>30 min) | 标记为失败，进重试队列 |
| 累计失败 3 次 | 写 `.sync_status/FAILED/` + 告警 |

### 8.5 同步状态记录

`/mnt/basic/media/.sync_status/<YYYY-MM-DD>/<rel_path>.json`：

```json
{
  "src_path": "/mnt/basic/media/media/Movies/Interstellar (2014)/Interstellar.mkv",
  "src_size": 8589934592,
  "src_mtime": "2025-09-14T10:30:00Z",
  "cloud_path": "/139yun_media/media/Movies/Interstellar (2014)/Interstellar.mkv",
  "openlist_task_id": "abc123",
  "synced_at": "2025-09-14T10:32:15Z",
  "cleanup_at": "2025-09-17T10:32:15Z",
  "status": "synced",
  "retry_count": 0
}
```

---

## 9. 72h 清理任务设计

### 9.1 触发方式

cloud-sync 容器内启动一个**独立协程/线程**的 cron 子任务（与主监听逻辑隔离）。不建议拆成独立容器——清理与上传是同一业务的"收尾"。

### 9.2 清理逻辑

```python
for record in scan(.sync_status/<today_minus_3d>):
    if record['status'] != 'synced': skip
    if (now - record['synced_at']) < 72h: skip

    # 不再做云盘存在性校验：record['status']=='synced'
    # 已是 OpenList API 报告的成功（task_status=succeeded），直接信任

    src = record['src_path']
    # 只删视频，绝不删 nfo / jpg / 字幕
    for ext in ['.mkv', '.mp4', '.ts', '.iso']:
        f = src.removesuffix(Path(src).suffix) + ext
        if exists(f): unlink(f)

    record['cleaned_at'] = now
    record['status'] = 'cleaned'
    write(record)
```

### 9.3 安全保护

- **白名单路径**：只删 `/mnt/basic/media/media/` 和 `/mnt/basic/media/ani-rss/` 下的文件
- **状态驱动**：仅在 `.sync_status` 中 `status == 'synced'`（即 OpenList API 报告成功）的记录才进入清理
- **dry-run 模式**：上线前先跑 24h dry-run 日志
- **删除限制**：单次最多清理 50 个文件，避免误删风暴
- **告警**：每次清理动作都写日志，异常立即推送

---

## 10. 关键技术选型总结

| 问题 | 选择 | 理由 |
|---|---|---|
| 触发机制 | **inotify 文件系统事件** | 完全脱离应用层事件，**最解耦** |
| 上传 API | **OpenList `/api/fs/copy`（存储间复制）** | 文件不被流式传输，省一次网络拷贝 |
| 状态存储 | **本地数据集 `.sync_status/`** | 跨容器可读，单一真相源，零网络依赖 |
| 清理机制 | **同步状态驱动 + 时间窗** | 简单可靠，不依赖外部信号 |
| 139strm 协作 | **独立 cron 扫描** | 容忍 5-10 分钟延迟，组件完全解耦 |
| 媒体元数据缓存 | **MediaInfoKeeper** | STRM 模式下不重复访问云盘 |
| 容器部署 | **TrueNAS docker compose（同主机）** | 原生路径访问，无跨主机网络开销 |

---

## 11. 待解决/待验证的点

### 11.1 必查

1. **OpenList 139yun 驱动分片大小上限** —— 确认大文件（>5GB）能正常上传
2. **MP v2 内置资源转移的扫描间隔** —— 调整到合理值（如 5 分钟）
3. **ani-rss 重命名模板** —— 与 MP 模板不冲突的简化命名
4. **139strm 容器镜像** —— 确认官方/社区镜像可用
5. **OpenList copy API 的大文件超时** —— 大于 5GB 的单文件可能需要更长的轮询时间

### 11.2 风险点

| 风险 | 影响 | 缓解 |
|---|---|---|
| 移动云盘封号 | 上传链路全断 | OpenList 139yun 驱动支持多账号；监控上传失败率 |
| STRM 链接失效 | Emby 播放失败 | MediaInfoKeeper 缓存元数据；139strm 定期刷新链接 |
| OpenList 容器故障 | 上传链路中断 | cloud-sync 重试 + 告警；可备用 CloudDrive2 |
| cloud-sync 单点故障 | 上传完全停止 | 监控 watch 服务；考虑用 TrueNAS host cron 兜底 |
| 误删本地文件 | 需重传 | 72h 过渡期 + 仅清理 .sync_status 标记为 synced 的文件 + dry-run |
| TrueNAS 数据集损坏 | 全部组件失能 | 启用 ZFS 快照 + 定期备份 .sync_status/ |

### 11.3 监控建议

- **关键指标**：
  - 上传成功率、失败重试次数
  - STRM 生成延迟（上传完成 → STRM 出现）
  - Emby 库新入库延迟
  - 本地磁盘水位（72h 清理是否正常）
  - `.sync_status/FAILED/` 数量
- **告警渠道**：Bark / 微信 / Telegram bot
- **可视化**：Grafana + Prometheus（可选，初期先文本告警）

---

## 12. 推荐落地顺序

### 阶段 0：准备 ✅ 已完成（2025-09-14）
- TrueNAS ZFS 数据集 `/mnt/basic/media` 已创建
- 子目录 `downloads/`、`ani-rss/`、`media/`、`strm_media/`、`.sync_status/` 已就绪
- OpenList 已部署，139yun 存储 + local 存储（指向 `/mnt/basic/media`）已配置
- Emby MediaInfoKeeper 插件已安装

### 阶段 1：单文件手动验证
- 选一个老电影作为试点
- 手动复制到 `/mnt/basic/media/media/`
- 手动调 OpenList `/api/fs/copy` 复制到 139yun
- 验证 139strm cron 扫描后 STRM 出现
- 验证 Emby 播放成功
- 手动清理本地文件，验证播放不受影响

### 阶段 2：cloud-sync 容器
- 实现 cloud-sync 容器骨架（监听 → 校验 → copy → 标记）
- 实现 cleanup 子任务（dry-run 模式上线）
- docker compose 添加 cloud-sync 服务
- 跑一两个真实下载走通全链路

### 阶段 3：MP 路径接通
- 确认 MP v2 内置资源转移能正确把 downloads/ → media/
- 调整 MP 刮削源（TMDB + Bangumi）
- 跑一部电影、一部番剧、一部电视剧验证

### 阶段 4：ani-rss 收口
- 修改 ani-rss 输出目录为 `/mnt/basic/media/ani-rss/`
- 关闭 ani-rss 自身的命名模板改写（保留最小清洗）
- 验证 cloud-sync 监听 ani-rss/ 上传

### 阶段 5：清理上线
- 关闭 cloud-sync cleanup 的 dry-run 模式
- 跑一周观察，确认 72h 清理无误删
- 配置告警

### 阶段 6：优化（可选）
- 上传并发调优（默认 2，按带宽调）
- 重试策略调优
- 迁移存量（一次性脚本，扫整个 media/ 重传）

---

## 13. 一页总结

> **这套系统的本质**：
> 用 **cloud-sync**（独立容器）通过文件系统事件触发，把本地媒体通过 **OpenList 的存储间复制 API** 推到 **移动云盘**；**139strm** 独立 cron 扫描云盘生成 STRM 写回本地；**Emby + MediaInfoKeeper** 实现本地无媒体但库完整的"伪本地化"播放。
>
> **cloud-sync 是整个自动化的唯一协调器**——所有触发、上传、标记、清理逻辑都在它里面。**MP 不写插件**、**139strm 不接收推送**、**OpenList 不挂载本地**——组件之间**完全解耦**，每个组件都不知道其他组件的存在，只对外暴露自己的数据/服务。
>
> **STRM 是解耦本地与云盘的关键**——本地不存视频，Emby 仍能"看到"并播放。**MediaInfoKeeper 进一步解耦**——元数据持久化后，连云盘的元数据 API 都不再需要。
