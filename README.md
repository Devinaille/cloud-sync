# cloud-sync

TrueNAS docker compose 下的自动化协调器：监听本地 `media/` + `ani-rss/` 新视频，通过 OpenList 上传到 139yun 移动云盘，72h 后清理本地源视频。

详细架构见 [ARCHITECTURE.md](./ARCHITECTURE.md)。组件实现规格见 [docs/superpowers/specs/2026-09-14-cloudsync-design.md](./docs/superpowers/specs/2026-09-14-cloudsync-design.md)。

## 子模块

- [`cloud-sync/`](./cloud-sync/) — Go 实现的监听 / 上传 / 清理容器
- `docs/superpowers/` — 设计文档与实施计划

## 部署

参见根目录 `docker-compose.yml` 的 `cloud-sync` service。所有配置通过环境变量注入（模板：`.env.example`）。
