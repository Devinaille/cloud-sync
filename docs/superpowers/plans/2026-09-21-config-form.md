# Web UI 表单化配置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or executing-plans.

**Goal:** Web UI 表单化配置（默认），原始 YAML 作为高级模式；所有文件 I/O 在 `internal/config`。

**Architecture:** `config` 提供配置读写/合并/渲染；`httpapi` 只解析+调用+`Reload`；`web/` 只发 JSON。

**Spec:** `docs/superpowers/specs/2026-09-21-config-form-design.md`

## Global Constraints
- 命令在 `cloud-sync/` 下；`gofmt -l .` 空、`go vet ./...`、`go test -race -count=1 ./...` 全绿；无新依赖；镜像 < 30 MiB。

### Task 1: config 包（文件 I/O + 表单模型）
- [ ] `internal/config/form.go`：`FormValues`、`Values(cfg)`、`ReadFile`、`SaveRaw`、`MergeAndSave`、`Render`、`RegenerateAndSave`、私有 `atomicValidateWrite`。
- [ ] `internal/config/form_test.go`：合并保留注释/更新值/追加缺失键/token 空保留/非法报错；`Render` 可被 `Load` 解析且含注释；`SaveRaw` 原子写与校验。
- [ ] 提交 `feat(config): file I/O and form values for the web config editor`。

### Task 2: httpapi 端点
- [ ] `api_config.go`：YAML GET 用 `config.ReadFile`，PUT 用 `config.SaveRaw`；新增 form GET/PUT 与 regenerate；`configPutResponse` 复用。
- [ ] `api_test.go`：form GET `token_set`、PUT 保留注释/改值、非法 400、regenerate 写带注释内容。
- [ ] 提交 `feat(httpapi): config form endpoints; move file I/O to config`。

### Task 3: web 表单 UI
- [ ] `index.html`：Config 面板模式切换 + 表单容器 + 两个按钮（保存、重新生成）。
- [ ] `app.js`：字段布局/渲染/收集/保存/重新生成（confirm）；i18n en/zh。
- [ ] `style.css`：表单栅格/分组/控件。
- [ ] 提交 `feat(web): form-based config editor`。

### Task 4: AGENTS.md
- [ ] 新增 `## Frontend / backend boundary` 一节。提交 `docs: document frontend/backend boundary`。

### Task 5: 文档
- [ ] 根 `README.md`、`cloud-sync/README.md` 配置章节补充表单/YAML/重新生成。提交 `docs: config form editor`。

### Task 6: 验证
- [ ] `gofmt -l . && go vet ./... && go test -race -count=1 ./...`；构建；`./scripts/smoke.sh && ./scripts/smoke-config.sh`；`./local-dev/run.sh` 手测；推送 `dev`。
