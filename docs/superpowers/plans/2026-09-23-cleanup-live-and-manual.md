# 清理实时到期 + 单文件手动清理 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让清理到期时间按“同步时间 + 当前清理周期”实时计算，并新增可配置的清理间隔与单文件手动清理。

**Architecture:** `state` 只做存储与选取（到期用调用方传入的 duration 计算）；`config` 负责新增 `cleanup_interval_seconds`（默认 3600，最小 300）与全部校验；`cleanup` 用配置间隔 tick，并新增 `CleanKey`；`httpapi` 暴露 `POST /api/cleanup/file` 与实时 `cleanup_at`；`web` 加“清理”按钮与配置字段。遵守 `AGENTS.md` 的前后端分离：文件 I/O 在 `config`/`state`，`httpapi` 只编排。

**Tech Stack:** Go 1.22，`yaml.v3`，内嵌 Web UI（原生 JS）。

**Spec:** `docs/superpowers/specs/2026-09-23-cleanup-live-and-manual-design.md`

## Global Constraints

- 所有 Go 命令在 `cloud-sync/` 下执行；每任务一次提交；提交前 `gofmt -l .`（空）、`go vet ./...`、`go test -race -count=1 ./...` 全绿。
- 不新增依赖；镜像 < 30 MiB。
- `CLEANUP_INTERVAL_SECONDS`：默认 `3600`，**最小 `300`**，必须为正整数；缺失走默认，非法（非整数 / ≤0 / <300）报错。
- 到期规则：`Status == "synced" && now >= SyncedAt + <当前 CleanupAfter>`。
- 手动清理仅对 `synced` 生效，并遵守 `cleanup_dry_run`。
- Conventional Commits。

---

### Task 1: config —— 新增 `cleanup_interval_seconds`

**Files:**
- Modify: `cloud-sync/internal/config/config.go`
- Modify: `config.example.yaml`
- Test: `cloud-sync/internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.CleanupInterval time.Duration`；`cleanupInterval(env string) (time.Duration, error)`；`const minCleanupInterval = 300 * time.Second`。

- [ ] **Step 1: Write the failing test**（追加到 `config_test.go`）

```go
func TestCleanupInterval(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", time.Hour, false},
		{"3600", time.Hour, false},
		{"300", 300 * time.Second, false},
		{"299", 0, true},
		{"0", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		t.Setenv("CLEANUP_INTERVAL_SECONDS", tc.in)
		got, err := cleanupInterval("CLEANUP_INTERVAL_SECONDS")
		if tc.wantErr {
			if err == nil {
				t.Errorf("in=%q: expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("in=%q: got %v err %v, want %v", tc.in, got, err, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCleanupInterval ./internal/config`
Expected: FAIL（`undefined: cleanupInterval`）

- [ ] **Step 3: Implement**

`config.go` 的 `Config` 结构在 `TaskTimeout` 后加：

```go
	TaskTimeout       time.Duration
	CleanupInterval   time.Duration
```

`fileConfig` 在 `TaskTimeoutSeconds` 后加：

```go
	TaskTimeoutSeconds     int      `yaml:"task_timeout_seconds"`
	CleanupIntervalSeconds int      `yaml:"cleanup_interval_seconds"`
```

`loadWithFile` 在 `TaskTimeoutSeconds` 的 setenv 之后加：

```go
	if f.CleanupIntervalSeconds != 0 {
		os.Setenv("CLEANUP_INTERVAL_SECONDS", strconv.Itoa(f.CleanupIntervalSeconds))
	}
```

`loadFromEnv` 在 `TaskTimeout` 解析之后加：

```go
	if cfg.CleanupInterval, err = cleanupInterval("CLEANUP_INTERVAL_SECONDS"); err != nil {
		return nil, err
	}
```

在 `secondsDuration` 附近加：

```go
// minCleanupInterval is the floor for CLEANUP_INTERVAL_SECONDS: too-frequent
// ticks would re-walk the whole state directory.
const minCleanupInterval = 300 * time.Second

// cleanupInterval reads env seconds, defaulting to one hour when unset, and
// rejects values below the floor.
func cleanupInterval(env string) (time.Duration, error) {
	v := os.Getenv(env)
	if v == "" {
		return time.Hour, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive int, got %q", env, v)
	}
	d := time.Duration(n) * time.Second
	if d < minCleanupInterval {
		return 0, fmt.Errorf("config: %s must be >= %d seconds, got %d", env, int(minCleanupInterval/time.Second), n)
	}
	return d, nil
}
```

`config.example.yaml` 在 `cleanup_dry_run` 之后加：

```yaml
# Cleanup tick interval (seconds). Default 3600, minimum 300.
# Due time is computed live as synced_at + cleanup_after_hours, so changing
# cleanup_after_hours affects already-synced files at the next tick.
cleanup_interval_seconds:  3600
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/config`
Expected: PASS（含既有 `TestLoad_ExampleConfigParses`）

- [ ] **Step 5: Commit**

```bash
git add cloud-sync/internal/config/config.go cloud-sync/internal/config/config_test.go config.example.yaml
git commit -m "feat(config): add cleanup_interval_seconds (default 3600, min 300)"
```

---

### Task 2: config 表单支持新字段

**Files:**
- Modify: `cloud-sync/internal/config/form.go`
- Test: `cloud-sync/internal/config/form_test.go`

**Interfaces:**
- Consumes: `Config.CleanupInterval`（Task 1）。
- Produces: `FormValues.CleanupIntervalSeconds int`（JSON `cleanup_interval_seconds`）；`MergeAndSave`/`Render` 写入该键。

- [ ] **Step 1: Write the failing test**（追加到 `form_test.go`）

```go
func TestMergeAndSave_CleanupInterval(t *testing.T) {
	preserveEnv(t)
	path := filepath.Join(t.TempDir(), "cloud-sync.yaml")
	v := fullForm(t)
	v.CleanupIntervalSeconds = 600
	if err := MergeAndSave(path, v); err != nil {
		t.Fatalf("MergeAndSave: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CleanupInterval != 600*time.Second {
		t.Errorf("CleanupInterval = %v, want 600s", got.CleanupInterval)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestMergeAndSave_CleanupInterval ./internal/config`
Expected: FAIL（`unknown field CleanupIntervalSeconds` / 编译错误）

- [ ] **Step 3: Implement**

`form.go` 的 `FormValues` 在 `CleanupDryRun` 后加：

```go
	CleanupDryRun         bool     `json:"cleanup_dry_run"`
	CleanupIntervalSeconds int     `json:"cleanup_interval_seconds"`
```

`Values` 加：

```go
		CleanupIntervalSeconds: int(cfg.CleanupInterval / time.Second),
```

`MergeAndSave` 的 `cleanup_dry_run` 之后加：

```go
	setBool(root, "cleanup_dry_run", v.CleanupDryRun)
	setInt(root, "cleanup_interval_seconds", v.CleanupIntervalSeconds)
```

`Render` 的 `cleanup_dry_run` 之后加：

```go
	fmt.Fprintf(&b, "cleanup_interval_seconds: %d\n", int(cfg.CleanupInterval/time.Second))
```

`form_test.go` 的 `fullForm` 返回值加 `CleanupIntervalSeconds: 3600,`；`TestRender_ParsesAndHasComments` 的 `cfg` 加 `CleanupInterval: 3600 * time.Second,`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/config`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cloud-sync/internal/config/form.go cloud-sync/internal/config/form_test.go
git commit -m "feat(config): expose cleanup_interval_seconds in the config form"
```

---

### Task 3: state —— 实时到期 + `Get`

**Files:**
- Modify: `cloud-sync/internal/state/state.go`
- Test: `cloud-sync/internal/state/state_test.go`

**Interfaces:**
- Produces: `func (s *StateManager) ListForCleanup(now time.Time, after time.Duration) ([]*StatusRecord, error)`；`func (s *StateManager) Get(key string) (*StatusRecord, error)`（未命中返回 `(nil, nil)`）。

- [ ] **Step 1: Write the failing tests**（追加到 `state_test.go`）

```go
func TestState_ListForCleanup_LiveDelay(t *testing.T) {
	st := newTestState(t)
	now := time.Now().UTC()
	if err := st.Write(&StatusRecord{
		Key: "A.mkv", SrcPath: "/x/A.mkv", SyncedAt: now.Add(-2 * time.Hour), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListForCleanup(now, time.Hour)
	if err != nil {
		t.Fatalf("ListForCleanup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after 1h: got %d records, want 1", len(got))
	}
	got, err = st.ListForCleanup(now, 3*time.Hour)
	if err != nil {
		t.Fatalf("ListForCleanup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("after 3h: got %d records, want 0", len(got))
	}
}

func TestState_Get(t *testing.T) {
	st := newTestState(t)
	if err := st.Write(&StatusRecord{
		Key: "Movies/X.mkv", SrcPath: "/x/Movies/X.mkv", SyncedAt: time.Now().UTC(), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("Movies/X.mkv")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.SrcPath != "/x/Movies/X.mkv" || got.Key != "Movies/X.mkv" {
		t.Fatalf("Get = %+v, want SrcPath /x/Movies/X.mkv and Key set", got)
	}
	missing, err := st.Get("nope.mkv")
	if err != nil || missing != nil {
		t.Fatalf("Get(missing) = (%v, %v), want (nil, nil)", missing, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestState_(ListForCleanup_LiveDelay|Get)' ./internal/state`
Expected: FAIL（`ListForCleanup` 参数不匹配 / `Get` undefined）

- [ ] **Step 3: Implement**

`ListForCleanup` 改为：

```go
func (s *StateManager) ListForCleanup(now time.Time, after time.Duration) ([]*StatusRecord, error) {
```

其中的选取条件（原 `state.go:183`）改为：

```go
			if rec.Status == "synced" && !rec.SyncedAt.IsZero() && !now.Before(rec.SyncedAt.Add(after)) {
```

在 `ListForCleanup` 附近新增：

```go
// Get returns the record for key, or (nil, nil) when none exists. It scans the
// date buckets and FAILED/<date>/.
func (s *StateManager) Get(key string) (*StatusRecord, error) {
	target := filepath.FromSlash(filepath.ToSlash(key) + ".json")
	var dirs []string
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && looksLikeDate(e.Name()) {
			dirs = append(dirs, filepath.Join(s.root, e.Name()))
		}
	}
	if fEntries, err := os.ReadDir(filepath.Join(s.root, "FAILED")); err == nil {
		for _, e := range fEntries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(s.root, "FAILED", e.Name()))
			}
		}
	}
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, target))
		if err != nil {
			continue
		}
		var rec StatusRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, err
		}
		rec.Key = key
		return &rec, nil
	}
	return nil, nil
}
```

同时更新 `state_test.go` 中两处既有调用：
- `TestState_ListForCleanup_FiltersByStatusAndTime`（约 `:117`）：把记录的到期改为由 `SyncedAt` 驱动，并调用 `st.ListForCleanup(now, <delay>)`。
- `TestState_ListForCleanup_NestedKeyUpdatedInPlace`（约 `:170`）：同理改为 `st.ListForCleanup(now, <delay>)`。

示例改法（对应旧用例语义）：记录写 `SyncedAt: now.Add(-2*time.Hour)`，调用 `ListForCleanup(now, time.Hour)`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -count=1 ./internal/state`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cloud-sync/internal/state/state.go cloud-sync/internal/state/state_test.go
git commit -m "feat(state): compute cleanup due live; add Get(key)"
```

---

### Task 4: cleanup —— 可配置间隔 + `CleanKey`

**Files:**
- Modify: `cloud-sync/internal/cleanup/cleanup.go`
- Test: `cloud-sync/internal/cleanup/cleanup_test.go`

**Interfaces:**
- Consumes: `cfg.CleanupInterval`（Task 1）、`st.ListForCleanup(now, after)` + `st.Get(key)`（Task 3）。
- Produces: `func (c *Cleanup) CleanKey(ctx context.Context, key string) (dryRun bool, err error)`；私有 `deleteFiles(srcPath string)`。

- [ ] **Step 1: Write the failing tests**（追加到 `cleanup_test.go`）

```go
func TestCleanup_CleanKeyDeletesAndMarks(t *testing.T) {
	cl, st, mediaDir := newTestCleanup(t, false)
	src := filepath.Join(mediaDir, "Movies", "Clean.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(&state.StatusRecord{
		Key: "Movies/Clean.mkv", SrcPath: src, SyncedAt: time.Now().UTC(), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}

	dry, err := cl.CleanKey(context.Background(), "Movies/Clean.mkv")
	if err != nil || dry {
		t.Fatalf("CleanKey: dry=%v err=%v", dry, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source file still exists")
	}
	rec, err := st.Get("Movies/Clean.mkv")
	if err != nil || rec == nil || rec.Status != "cleaned" {
		t.Fatalf("record = %+v err=%v, want status cleaned", rec, err)
	}
}

func TestCleanup_CleanKeyDryRun(t *testing.T) {
	cl, st, mediaDir := newTestCleanup(t, true)
	src := filepath.Join(mediaDir, "Dry.mkv")
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(&state.StatusRecord{
		Key: "Dry.mkv", SrcPath: src, SyncedAt: time.Now().UTC(), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}
	dry, err := cl.CleanKey(context.Background(), "Dry.mkv")
	if err != nil || !dry {
		t.Fatalf("CleanKey: dry=%v err=%v, want dry=true", dry, err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("dry-run deleted the file: %v", err)
	}
	if rec, _ := st.Get("Dry.mkv"); rec == nil || rec.Status != "synced" {
		t.Errorf("dry-run must not change status: %+v", rec)
	}
}

func TestCleanup_CleanKeyRejectsNonSynced(t *testing.T) {
	cl, st, mediaDir := newTestCleanup(t, false)
	if err := st.Write(&state.StatusRecord{
		Key: "F.mkv", SrcPath: filepath.Join(mediaDir, "F.mkv"),
		SyncedAt: time.Now().UTC(), Status: "failed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CleanKey(context.Background(), "F.mkv"); err == nil {
		t.Error("CleanKey(failed) = nil, want error")
	}
	if _, err := cl.CleanKey(context.Background(), "missing.mkv"); err == nil {
		t.Error("CleanKey(missing) = nil, want error")
	}
}
```

`cleanup_test.go` 若未导入 `state`/`time`/`os`/`path/filepath`，补齐 import。

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestCleanup_CleanKey ./internal/cleanup`
Expected: FAIL（`cl.CleanKey undefined`）

- [ ] **Step 3: Implement**

`Run` 的 ticker：

```go
	t := time.NewTicker(c.cfg.CleanupInterval)
```

`tick` 的选取调用：

```go
	recs, err := c.st.ListForCleanup(now, c.cfg.CleanupAfter)
```

把删除逻辑抽成方法，并在 `tick` 中复用：

```go
// deleteFiles removes the local video variants sharing srcPath's base name.
func (c *Cleanup) deleteFiles(srcPath string) {
	base := strings.TrimSuffix(srcPath, filepath.Ext(srcPath))
	for _, ext := range []string{".mkv", ".mp4", ".ts", ".iso"} {
		candidate := base + ext
		if _, err := os.Stat(candidate); err == nil {
			if err := os.Remove(candidate); err != nil {
				c.log.Error("cleanup: delete failed", "path", candidate, "err", err)
			}
		}
	}
}

// CleanKey cleans a single record on demand. It honors cleanup_dry_run and
// returns dryRun=true when the deletion was only logged.
func (c *Cleanup) CleanKey(ctx context.Context, key string) (bool, error) {
	rec, err := c.st.Get(key)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, fmt.Errorf("cleanup: no record for %q", key)
	}
	if rec.Status != "synced" {
		return false, fmt.Errorf("cleanup: %q is %s, not synced", key, rec.Status)
	}
	if !media.Whitelisted(rec.SrcPath, c.cfg.AllowedPrefixes) {
		return false, fmt.Errorf("cleanup: %q not under allowed prefixes", rec.SrcPath)
	}
	if c.cfg.CleanupDryRun {
		c.log.Info("[DRY-RUN] would delete", "path", rec.SrcPath)
		return true, nil
	}
	c.deleteFiles(rec.SrcPath)
	cleaned := time.Now().UTC()
	rec.Status = "cleaned"
	rec.CleanedAt = &cleaned
	if err := c.st.Update(rec); err != nil {
		return false, fmt.Errorf("cleanup: state update: %w", err)
	}
	c.log.Info("cleanup: manually cleaned", "key", key, "path", rec.SrcPath)
	return false, nil
}
```

把 `tick` 里原来的删除循环替换为 `c.deleteFiles(rec.SrcPath)`；新增 `fmt` 到 import。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -count=1 ./internal/cleanup`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cloud-sync/internal/cleanup/cleanup.go cloud-sync/internal/cleanup/cleanup_test.go
git commit -m "feat(cleanup): configurable interval and manual CleanKey"
```

---

### Task 5: httpapi —— `/api/cleanup/file` + 实时 `cleanup_at`

**Files:**
- Modify: `cloud-sync/internal/httpapi/api_actions.go`
- Modify: `cloud-sync/internal/httpapi/files_query.go`
- Modify: `cloud-sync/internal/httpapi/web.go`
- Test: `cloud-sync/internal/httpapi/api_test.go`

**Interfaces:**
- Consumes: `Cleanup.CleanKey`（Task 4）。
- Produces: `POST /api/cleanup/file`（body `{"key":"..."}` → `{"ok":true,"dry_run":bool}`）；`recordToItem(rec, cleanupAfter)`。

- [ ] **Step 1: Write the failing tests**（追加到 `api_test.go`）

```go
func TestAPI_CleanupFile(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no state")
	}
	src := filepath.Join(env.watch, "Movies", "Manual.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(&state.StatusRecord{
		Key: "Movies/Manual.mkv", SrcPath: src, SyncedAt: time.Now().UTC(), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}

	srv := env.server(t)
	var resp struct {
		OK     bool `json:"ok"`
		DryRun bool `json:"dry_run"`
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/cleanup/file",
		map[string]string{"key": "Movies/Manual.mkv"}, &resp); code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if !resp.OK {
		t.Error("ok = false")
	}
}

func TestAPI_CleanupFile_RejectsNonSynced(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)
	code := doJSON(t, http.MethodPost, srv.URL+"/api/cleanup/file",
		map[string]string{"key": "nope.mkv"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

func TestAPI_Files_CleanupAtLive(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no state")
	}
	synced := time.Now().UTC().Add(-time.Hour)
	if err := st.Write(&state.StatusRecord{
		Key: "Live.mkv", SrcPath: filepath.Join(env.watch, "Live.mkv"),
		SyncedAt: synced, Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}
	srv := env.server(t)
	var body filesResponse
	if code := getJSON(t, srv.URL+"/api/files?state=synced", &body); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	var want string
	for _, it := range body.Items {
		if it.Key == "Live.mkv" {
			want = it.CleanupAt
		}
	}
	if want == "" {
		t.Fatal("Live.mkv not found")
	}
	got, err := time.Parse(time.RFC3339, want)
	if err != nil {
		t.Fatalf("cleanup_at %q: %v", want, err)
	}
	// testutil.TestConfig uses CleanupAfter=72h; cleanup_at must be synced+72h.
	exp := synced.Add(72 * time.Hour)
	if got.Sub(exp).Abs() > time.Minute {
		t.Errorf("cleanup_at = %v, want ~%v", got, exp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestAPI_(CleanupFile|Files_CleanupAtLive)' ./internal/httpapi`
Expected: FAIL（404 / cleanup_at 用旧快照）

- [ ] **Step 3: Implement**

`api_actions.go` 末尾新增：

```go
func (w *WebServer) handleCleanupFile(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.Key == "" {
		writeError(rw, http.StatusBadRequest, "key is empty")
		return
	}
	cl := w.sup.Cleanup()
	if cl == nil {
		writeError(rw, http.StatusServiceUnavailable, "supervisor not running")
		return
	}
	dryRun, err := cl.CleanKey(r.Context(), req.Key)
	if err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "dry_run": dryRun})
}
```

`web.go` 在 `/api/cleanup/run` 之后注册：

```go
	mux.HandleFunc("/api/cleanup/file", w.handleCleanupFile)
```

`files_query.go`：`recordToItem` 增加时长参数并实时计算：

```go
func recordToItem(rec *state.StatusRecord, cleanupAfter time.Duration) fileItem {
	item := fileItem{
		Key:        rec.Key,
		SrcPath:    rec.SrcPath,
		Size:       rec.SrcSize,
		State:      rec.Status,
		CloudPath:  rec.CloudPath,
		RetryCount: rec.RetryCount,
		Error:      rec.Error,
	}
	if rec.Status == "synced" && !rec.SyncedAt.IsZero() {
		item.CleanupAt = rec.SyncedAt.Add(cleanupAfter).UTC().Format(time.RFC3339)
	} else if !rec.CleanupAt.IsZero() {
		item.CleanupAt = rec.CleanupAt.UTC().Format(time.RFC3339)
	}
	if !rec.SyncedAt.IsZero() {
		item.SyncedAt = rec.SyncedAt.UTC().Format(time.RFC3339)
	}
	return item
}
```

`buildFileItems` 两处调用改为 `recordToItem(rec, cfg.CleanupAfter)`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -count=1 ./internal/httpapi`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cloud-sync/internal/httpapi/api_actions.go cloud-sync/internal/httpapi/files_query.go cloud-sync/internal/httpapi/web.go cloud-sync/internal/httpapi/api_test.go
git commit -m "feat(httpapi): per-file cleanup endpoint and live cleanup_at"
```

---

### Task 6: web —— 每行“清理”按钮 + 配置字段/文案

**Files:**
- Modify: `cloud-sync/internal/httpapi/web/app.js`

**Interfaces:**
- Consumes: `POST /api/cleanup/file`（Task 5）；配置字段 `cleanup_interval_seconds`（Task 2）。

- [ ] **Step 1: Implement（前端无自动化测试；用 `node --check` 验证语法）**

`CONFIG_FIELDS` 的 `config.group.cleanup` 组，在 `cleanup_dry_run` 后加：

```js
      { key: "cleanup_interval_seconds", type: "number", min: 300 },
```

`fileRow` 的 `actionCell`（当前只有 retry）替换为下面完整实现：

```js
    var actionCell = el("td", "col-action");
    var clean = el("button", "btn small", t("files.cleanup"));
    clean.type = "button";
    clean.disabled = item.state !== "synced";
    clean.addEventListener("click", function () {
      if (!window.confirm(t("files.cleanupConfirm", { key: item.key }))) {
        return;
      }
      cleanFile(item.key);
    });
    actionCell.appendChild(clean);

    var retry = el("button", "btn small", t("files.retry"));
    retry.type = "button";
    retry.addEventListener("click", function () {
      retryKeys([item.key]);
    });
    actionCell.appendChild(retry);
    tr.appendChild(actionCell);
```

新增函数（放在 `retryKeys` 附近）：

```js
  async function cleanFile(key) {
    try {
      var data = await postJSON("/api/cleanup/file", { key: key });
      if (data && data.dry_run) {
        toast(t("files.cleanupDryRun", { key: key }), "info");
      } else {
        toast(t("files.cleanupDone", { key: key }), "success");
      }
      await loadFiles();
      loadStatus();
    } catch (e) {
      toast(t("files.cleanupFailed", { key: key, e: e.message }), "error");
    }
  }
```

i18n（en 与 zh 各加）：

```js
      "files.cleanup": "Cleanup",
      "files.cleanupConfirm": "Delete the local file for {key}?",
      "files.cleanupDone": "Cleaned: {key}",
      "files.cleanupDryRun": "Dry-run: not deleted ({key})",
      "files.cleanupFailed": "Cleanup failed: {key} \u2014 {e}",
      "config.field.cleanup_interval_seconds": "Cleanup interval (seconds)",
      "config.field.cleanup_interval_seconds.help": "Default 3600; minimum 300.",
```

```js
      "files.cleanup": "清理",
      "files.cleanupConfirm": "删除 {key} 的本地文件？",
      "files.cleanupDone": "已清理：{key}",
      "files.cleanupDryRun": "演练：未删除（{key}）",
      "files.cleanupFailed": "清理失败：{key} \u2014 {e}",
      "config.field.cleanup_interval_seconds": "清理间隔（秒）",
      "config.field.cleanup_interval_seconds.help": "默认 3600，最小 300。",
```

- [ ] **Step 2: Verify syntax**

Run: `node --check cloud-sync/internal/httpapi/web/app.js`
Expected: 无输出（通过）；若本机无 node，跳过并在最终 smoke 里验证

- [ ] **Step 3: Commit**

```bash
git add cloud-sync/internal/httpapi/web/app.js
git commit -m "feat(web): per-row cleanup button and cleanup interval field"
```

---

### Task 7: 文档 + 全量验证

**Files:**
- Modify: `cloud-sync/README.md`
- Modify: `README.md`
- Modify: `ARCHITECTURE.md`

- [ ] **Step 1: Docs**

- `cloud-sync/README.md` env 表加 `CLEANUP_INTERVAL_SECONDS`（默认 3600，最小 300）；`CLEANUP_AFTER_HOURS` 行注明“到期=synced_at+当前值，实时生效”。
- `README.md`：Files 描述加“每行可手动清理（仅 synced，遵守 dry-run）”；配置项说明加 `cleanup_interval_seconds`。
- `ARCHITECTURE.md` 清理章节：到期实时计算、间隔可配、单文件手动清理。

- [ ] **Step 2: Full verification**

```bash
cd cloud-sync
gofmt -l . && go vet ./... && go test -race -count=1 ./...
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o cloud-sync .
cd ..
./scripts/smoke.sh && ./scripts/smoke-config.sh   # 均输出 OK
```

- [ ] **Step 3: Commit**

```bash
git add README.md cloud-sync/README.md ARCHITECTURE.md
git commit -m "docs: live cleanup due time, configurable interval, manual cleanup"
```
