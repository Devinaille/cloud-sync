# cloud-sync

TrueNAS docker compose 下的自动化协调器：监听本地 `media/` + `ani-rss/` 新视频，通过 OpenList 上传到 139yun 移动云盘，72h 后清理本地源视频。

详细架构见 [ARCHITECTURE.md](./ARCHITECTURE.md)。组件实现规格见 [docs/superpowers/specs/2026-09-14-cloudsync-design.md](./docs/superpowers/specs/2026-09-14-cloudsync-design.md)。

## 子模块

- [`cloud-sync/`](./cloud-sync/) — Go 实现的 cloud-sync 容器（监听 / 上传 / 清理）
- [`config.example.yaml`](./config.example.yaml) — 配置文件模板（复制为本地副本后修改）
- [`scripts/`](./scripts/) — 仓库级开发/调试脚本（本地 smoke test、OpenList mock）
- [`docker-compose.yml`](./docker-compose.yml) — 部署 compose（配置文件模式，默认）
- [`docker-compose.env.yml`](./docker-compose.env.yml) / [`docker-compose.inline.yml`](./docker-compose.inline.yml) — 分别用 `.env` / 内联环境变量的 compose
- [`docker-compose.build.yml`](./docker-compose.build.yml) — 叠加此文件可从本地源码构建镜像

## 部署

cloud-sync 有两种部署方式。两种都支持配置文件模式（推荐）和纯环境变量模式。

### 方式 A：Docker Compose（推荐）

适合已经有 Docker / TrueNAS 的环境；提供容器隔离、自动重启、统一日志（`docker compose logs`）。

提供三份 compose，按配置来源选一份（都共享同样的端口/挂载/网络）：

| 文件 | 配置来源 | 用途 |
|---|---|---|
| `docker-compose.yml` | **配置文件** `config/cloud-sync.yaml`（默认，推荐） | 集中管理、可在 Web UI 里改并热重载 |
| `docker-compose.env.yml` | `.env`（`env_file`） | 用 `.env` 注入环境变量 |
| `docker-compose.inline.yml` | compose 内直接写 `environment:` | 单文件自包含（密钥落在文件里） |

任意一份都可叠加 `docker-compose.build.yml` 从本地源码构建镜像（`-f <base> -f docker-compose.build.yml up -d --build`）。

**运行用户（PUID/PGID）**：三份 compose 都用 `user: "${PUID:-1000}:${PGID:-1000}"` 按环境变量切换运行用户（**默认 1000:1000**；镜像自身默认 `nonroot`=65532，被 compose 覆盖）；把 `PUID`/`PGID` 设为宿主上拥有 `media`/`config` 目录的用户（可在 `.env` 或 shell 里设置）。该用户必须能写这些挂载目录。

#### 1. 准备配置

```bash
# 复制配置模板到 config/ 目录（compose 挂载的是 ./config 目录）
mkdir -p config
cp config.example.yaml config/cloud-sync.yaml

# 编辑真实值
$EDITOR config/cloud-sync.yaml
# 必改项：
#   openlist_token        (OpenList 后台获取)
#   watch_dirs            (默认 /mnt/basic/media/media + /mnt/basic/media/ani-rss)
#   sync_status_dir       (默认 /config/.sync_status)
#   allowed_source_prefixes (必须包含两个 WATCH_*_DIR)
```

`config.example.yaml` 里每个字段都有详细注释。

#### 2. 创建 host 端目录

```bash
mkdir -p /mnt/basic/media/media /mnt/basic/media/ani-rss
```

`media/` 和 `ani-rss/` 至少要存在（cloud-sync 用 fsnotify 监听）。`strm_media/` 是 139strm + Emby 的事，不需要 cloud-sync 创建。

#### 3. 启动

默认拉取发布镜像 `ghcr.io/devinaille/cloud-sync`。用 `CLOUD_SYNC_TAG` 选版本（默认 `latest`，仅正式版发布；目前只有 rc，请显式指定）：

```bash
# 拉发布镜像并启动
CLOUD_SYNC_TAG=v0.1.0-rc2 docker compose up -d cloud-sync
docker compose logs -f cloud-sync

# 或从本地源码构建（本地开发 / 无 amd64 发布镜像时）
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

预期首条日志（`LOG_FILE` 默认空 → stdout，由 compose 收集）：

```json
{"level":"INFO","msg":"cloud-sync starting","log_level":"INFO","openlist_url":"http://openlist:5244",...}
{"level":"INFO","msg":"openlist ping ok"}
```

#### 4. 验证

丢一个视频到 `/mnt/basic/media/media/Movies/Test.mkv`（>100 MB，MinFileSize 是硬编码的），几秒后应该看到：

```bash
docker compose exec cloud-sync ls /config/.sync_status/2026-09-15/Movies/
# Test.mkv.json
```

#### 5. 配合 sibling services

cloud-sync 只依赖同 `media` Docker 网络下能解析到的 OpenList HTTP endpoint。OpenList / qBittorrent / Emby / MoviePilot 等 sibling service 由各自的 compose 文件管理——它们必须共享同一个名为 `media` 的网络：

```yaml
networks:
  media:
    external: true
    name: media
```

放 `sibling-compose.yml` 里与 `docker-compose.yml` 一起 `up`。

#### 6. 升级

发布镜像：

```bash
CLOUD_SYNC_TAG=v0.2.0 docker compose pull cloud-sync
CLOUD_SYNC_TAG=v0.2.0 docker compose up -d cloud-sync
```

本地构建：

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

配置文件不会被打进镜像（挂载的），升级不丢设置。

---

### 方式 B：直接跑二进制

适合不想用 Docker 的环境（裸 Linux server / LXC 容器 / NAS 主机 OS 直接跑）。需要 Go 1.22+ 在构建机上（或者用 release tarball）。

#### 1. 构建

在构建机上：

```bash
git clone https://github.com/Devinaille/cloud-sync.git
cd cloud-sync/cloud-sync
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o cloud-sync .
```

产出单一二进制 `cloud-sync`（约 8 MB，静态链接）。把它 scp / rsync 到部署机（比如 `/usr/local/bin/cloud-sync`）。

#### 2. 配置

跟 Docker 方式用同一份 `config.example.yaml`：

```bash
mkdir -p /etc/cloud-sync
cp config.example.yaml /etc/cloud-sync/cloud-sync.yaml
$EDITOR /etc/cloud-sync/cloud-sync.yaml
```

确保 `/etc/cloud-sync/cloud-sync.yaml` 里的 `watch_*_dir` / `sync_status_dir` 绝对路径在部署机存在并可写。

#### 3. 跑

```bash
/usr/local/bin/cloud-sync /etc/cloud-sync/cloud-sync.yaml
```

第一个位置参数是配置文件路径（默认 `/config/cloud-sync.yaml`——适合容器内；裸机部署显式传）。日志写 stdout。

#### 4. systemd unit

推荐用 systemd 管生命周期：

```ini
# /etc/systemd/system/cloud-sync.service
[Unit]
Description=cloud-sync (inotify -> OpenList -> 139yun)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cloud-sync /etc/cloud-sync/cloud-sync.yaml
Restart=on-failure
RestartSec=5
User=cloud-sync
Group=cloud-sync
# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/config/.sync_status /mnt/basic/media/media /mnt/basic/media/ani-rss
PrivateTmp=true
LimitNOFILE=65536
# inotify 需要 fsnotify 看到目录
# (默认 kernel fs.inotify.max_user_watches=8192 已够)

[Install]
WantedBy=multi-user.target
```

启用：

```bash
sudo useradd -r -s /usr/sbin/nologin cloud-sync
sudo systemctl daemon-reload
sudo systemctl enable --now cloud-sync
sudo journalctl -u cloud-sync -f
```

`ReadWritePaths` 是关键——`ProtectSystem=strict` 默认把 `/usr`、`/boot` 设 read-only，其余仍可写；显式列路径让 cloud-sync 只能动 `/mnt/basic/media` 下三个目录。

#### 5. 升级

```bash
# 构建机
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o cloud-sync .
rsync cloud-sync deploy-host:/usr/local/bin/cloud-sync.new

# 部署机
sudo systemctl stop cloud-sync
sudo install -m 0755 /usr/local/bin/cloud-sync.new /usr/local/bin/cloud-sync
sudo systemctl start cloud-sync
```

`/etc/cloud-sync/cloud-sync.yaml`，独立于二进制。

---

### Web UI（可选）

Web UI + JSON API 内嵌在二进制里，默认监听 `:8099`（配置键 `UI_LISTEN` / `ui_listen`；设为空字符串或 `-` 可完全禁用）。浏览器打开：

```
http://<host>:8099/
```

三个 Tab：

- **Dashboard**：同步计数（synced / failed / cleaned / unsynced）、OpenList 连通性、运行时长、watch 目录；「Run cleanup now」立即触发一次清理（遵循 `cleanup_dry_run`）。
- **Files**：按状态筛选 / 搜索 / 分页浏览文件；勾选行（或单行按钮）触发重试——重试会删掉该 key 的状态记录并重新入队，走既有上传状态机。
- **Config**：编辑 YAML 后「Save & Reload」；保存会原子写回配置文件并热重载（无需重启容器/进程）。右侧只读展示 `/api/status` 里的生效配置。

界面支持**中英文切换**：右上角 `EN / 中文`。默认跟随浏览器语言，选择保存在浏览器 `localStorage`。

**任务默认关闭**：`tasks_enabled`/`TASKS_ENABLED` 默认 `false`，启动后只起 UI、不监听/上传/清理。在 Dashboard 点「启动任务」运行时开启（或配置里设 true）。开启前可先跑**上传预检查**（只读扫描：列出将上传的文件，并调用 OpenList `/api/fs/get` 检测每个文件是否已在云盘上，报告写 `precheck.json`）。暂停期间重试/清理返回 `409`。

> **无鉴权**：任何能访问该端口的人都能读取配置（含 token）、修改配置、触发重试。只在可信内网暴露；也可以只映射到 `127.0.0.1`，再通过 SSH 隧道 / 反向代理访问。

注意事项：

- 从 Web UI 保存配置会写入 `/config/cloud-sync.yaml`，所以挂载进去的配置文件必须**可写**（compose 里用 `:rw`，不是 `:ro`）。
- 改 `ui_listen`（`UI_LISTEN`）需要**重启进程**生效——监听地址在启动时绑定，配置热重载不会换端口。
- 裸机/systemd 部署时，`ProtectSystem` / `ReadWritePaths` 需允许写配置文件所在目录，否则「Save & Reload」会失败。

---

## 本地开发/测试

见 [`cloud-sync/README.md`](./cloud-sync/README.md)（开发视角：build / test / 本地 smoke / 配置细节）。

一键 smoke 端到端（不需要真 OpenList）：

```bash
./scripts/smoke.sh           # 纯 env 模式
./scripts/smoke-config.sh    # YAML 配置文件模式
```

## CI

每次 push 到 `main` 或 PR，GitHub Actions 跑：

- `gofmt -l cloud-sync/` 漂移检查
- `go vet ./...`
- `go test -race -count=1 ./...`
- `CGO_ENABLED=0 GOOS=linux go build` + 上传 binary artifact
- `docker build`（`golang:1.22-alpine` → `distroless/static-debian12:nonroot`） + size 断言 `< 30 MiB`

详见 [`.github/workflows/ci.yml`](./.github/workflows/ci.yml)。

## 分支与开发流程

- **`dev`** — 开发分支。所有开发在此进行。push 到 `dev` 触发 [`dev.yml`](./.github/workflows/dev.yml)：跑 gofmt/vet/test，构建二进制（上传 artifact），并推送**带 `-dev` 后缀**的镜像：
  - `ghcr.io/devinaille/cloud-sync:dev`（滚动）
  - `ghcr.io/devinaille/cloud-sync:dev-<YYYYMMDDHHMMSS>`（带构建时间戳，便于确认是否更新）
  - `ghcr.io/devinaille/cloud-sync:sha-<short>-dev`
- **`main`** — 发布分支。只有从 `dev` **merge 回 `main`** 后，才在 `main` 上打 tag，触发 rc / release。

本地开发（示例）：

```bash
git checkout dev
# ... 开发、提交 ...
git push origin dev                 # 触发 dev 镜像

git checkout main
git merge --no-ff dev
git push origin main                # 合并回 main
git tag v0.1.0-rc2 && git push origin v0.1.0-rc2   # rc（在 main 上打 tag）
```

> rc/release workflow 会校验 tag 的提交**必须在 `main` 上**（`git merge-base --is-ancestor`）；在 `dev` 的提交上打 tag 会直接失败。这样保证只有合并回 `main` 才发布 rc/release。

## 发布（rc / release）

在 `main` 上打 tag 触发打包与发布（两个独立 workflow）。发布物：

- **GitHub Release**：`cloud-sync_<tag>_linux_amd64.tar.gz`（含二进制 + README）+ `SHA256SUMS`。
- **容器镜像**：`ghcr.io/devinaille/cloud-sync:<tag>`（正式版额外打 `:latest`）。

| 类型 | tag 形式 | workflow | 说明 |
|---|---|---|---|
| 候选版 | `v1.0.0-rc1`（含 `-rc`） | [`rc.yml`](./.github/workflows/rc.yml) | 创建 **pre-release**；镜像打 `<tag>` 和 `<tag>-<时间戳>`，不动 `latest` |
| 正式版 | `v1.0.0` | [`release.yml`](./.github/workflows/release.yml) | 创建正式 Release；镜像打 `<tag>`、`<tag>-<时间戳>` 和 `latest` |

构建时会把 version/commit/build-time 通过 `-ldflags` 打进二进制：启动日志里有 `version/commit/build_time`，Web UI Dashboard 的「版本」也显示，`GET /api/status` 返回 `version`/`commit`/`build_time`——用来确认容器跑的是哪个构建。rc/release 的镜像额外带一个 `-<时间戳>` 后缀 tag（如 `v0.1.0-rc1-20250917023000`），dev 同理（`dev-<时间戳>`）。

发版步骤：

```bash
git tag v1.0.0-rc1 && git push origin v1.0.0-rc1   # 候选版
git tag v1.0.0     && git push origin v1.0.0       # 正式版
```

两个 workflow 都支持在 Actions 页面 **Run workflow**（`workflow_dispatch`），填入已存在的 tag 可重跑打包。先跑测试（gofmt/vet/test）再打包，失败即中止。

> ghcr 包若为 private，需先 `docker login ghcr.io` 才能拉取；改为 public 后匿名可拉。

### 部署发布产物（本地测试）

**方式 1：跑发布的镜像**（compose 默认即此路径）：

```bash
mkdir -p config && cp config.example.yaml config/cloud-sync.yaml   # 编辑成真实配置

# base compose 直接用发布镜像，用 CLOUD_SYNC_TAG 选版本
CLOUD_SYNC_TAG=v0.1.0-rc2 docker compose up -d cloud-sync
docker compose logs -f cloud-sync
```

从本地源码构建则叠加 build override：`docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`。

**方式 2：跑发布二进制**（无 Docker）：

```bash
curl -fL -o cs.tgz \
  https://github.com/Devinaille/cloud-sync/releases/download/v0.1.0-rc1/cloud-sync_v0.1.0-rc1_linux_amd64.tar.gz
tar xzf cs.tgz
./cloud-sync_v0.1.0-rc1_linux_amd64/cloud-sync /path/to/cloud-sync.yaml
```

Web UI：`http://<host>:8099/`（config 里 `ui_listen` 默认 `:8099`）。
