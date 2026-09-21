# Web UI 表单化配置 —— 设计文档

- 日期：2026-09-21
- 状态：已批准，实施中
- 关联：`AGENTS.md`（Frontend / backend boundary）、`internal/config`、`internal/httpapi`

## 1. 背景

当前 Config 选项卡只有原始 YAML 编辑器：用户必须直接改 YAML。目标：新增强表单化（白屏化）编辑，作为默认；原始 YAML 保留为"高级配置"。

## 2. 目标 / 非目标

**目标**
- Web UI 表单填写配置并保存，服务端合并、校验、原子写、热重载。
- token 掩码：表单不回显 `openlist_token`，留空=保留原值，填写才覆盖。
- 表单保存默认**保留原文件注释/顺序**（`yaml.v3` Node 就地合并）。
- 提供"重新生成 YAML"按钮（前端二次确认），服务端重写为带注释的完整 YAML。
- 严格遵守前后端分离：所有配置文件 I/O 在 `internal/config`，`httpapi` 只解析调用，前端只发 JSON。

**非目标**
- 不引入鉴权。
- `min_file_size` 仍硬编码（表单只读展示）。
- 不新增配置字段。

## 3. 分层

- `internal/config`：`ReadFile`、`SaveRaw`、`Values`、`MergeAndSave`、`Render`、`RegenerateAndSave`。依赖仅为标准库 + `yaml.v3`，**不导入** `supervisor`/`httpapi`。
- `internal/httpapi`：transport 层。解析请求 → 调 `config` → 调 `supervisor.Reload` → 响应。
- `internal/httpapi/web/`：渲染与收集，不发文件操作。

## 4. 接口

- `GET /api/config` → `{yaml}`（`config.ReadFile`）。
- `PUT /api/config` → `config.SaveRaw` + `Reload`。
- `GET /api/config/form` → `{values, token_set, config_path, min_file_size_bytes, restart_fields}`。
- `PUT /api/config/form` → `{values}` → `config.MergeAndSave` + `Reload`。
- `POST /api/config/regenerate` → `config.RegenerateAndSave` + `Reload`。

`FormValues` 覆盖全部可编辑字段；`OpenListToken` 空=不改。

## 5. 校验 / 生效

- 唯一校验来源 `config.Load`（严格 YAML、必填、正整数、watch 目录存在）。
- `ui_listen` 标记为需重启（监听在启动时绑定）；其余经 `Reload` 热生效。

## 6. 取舍

- 表单保存把生效值落盘，文件优先于 env（既有语义）。
- 高级 YAML 仍回显明文 token，仅表单掩码。
- Node 合并只为**已存在的键**保留注释；新增键无注释。
