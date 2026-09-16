# local-dev — cloud-sync 本地试用环境

在本地（不需要 Docker / 真 OpenList）跑通 cloud-sync：构建二进制 → 启动内置 OpenList mock → 用本地目录启动 cloud-sync + Web UI。

## 启动

```bash
# 在仓库根目录
./local-dev/run.sh

# 换端口
UI_PORT=9099 MOCK_PORT=6244 ./local-dev/run.sh
```

启动后：

| 项 | 地址 / 路径 |
|---|---|
| Web UI | http://127.0.0.1:8099/ |
| OpenList mock | http://127.0.0.1:5244/ |
| 监听目录 | `local-dev/data/media`、`local-dev/data/ani-rss` |
| 配置 | `local-dev/config.yaml`（Web UI 的 Config 页也能改，保存后热重载） |
| 日志 | `local-dev/logs/cloud-sync.log`、`local-dev/logs/mock.log` |

## 验证同步

```bash
# 造一个 >100MB 的假视频（watcher 会拾取并“上传”到 mock 云盘）
mkdir -p local-dev/data/media/Movies
dd if=/dev/urandom of=local-dev/data/media/Movies/Demo.mkv bs=1M count=120 status=none

# 观察
tail -f local-dev/logs/cloud-sync.log          # 看到 "pipeline: synced"
ls -lh local-dev/data/cloud/media/Movies/      # “云盘”副本
ls local-dev/data/.sync_status/*/Movies/       # 状态记录
```

然后在 Web UI：

- **Dashboard**：synced/failed/unsynced 计数、OpenList 连通性、“立即清理”。
- **Files**：过滤 `unsynced` 能看到刚放进去的文件；`synced` 能看到已同步的；可单/多选重试。
- **Config**：编辑 YAML → “保存并重载”（写 `local-dev/config.yaml` 并热重载）。注意 `ui_listen` 端口改动需重启。

## 停止

```bash
./local-dev/stop.sh
```

## 说明

- 生成物（`data/ logs/ run/ bin/ config.yaml`）已在 `.gitignore` 中忽略。
- mock 把 OpenList 的 `/local_media` 映射到 `local-dev/data`、`/139yun_media` 映射到 `local-dev/data/cloud`；所以云端副本落在 `local-dev/data/cloud/media/...`。
- `cleanup_dry_run` 默认 `true`：清理只打日志不真删。要验证真删，把 `local-dev/config.yaml` 的 `cleanup_dry_run` 改成 `false`。
- 面板**无鉴权**，仅本机/内网使用。
