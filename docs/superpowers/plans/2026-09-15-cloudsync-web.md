# cloud-sync Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed an HTTP UI + JSON API in the cloud-sync binary for status, file listing, retry, and config save-with-hot-reload.

**Spec:** `docs/superpowers/specs/2026-09-15-cloudsync-web-design.md` — this plan argues from the spec; executors read both.

**Tech Stack:** Go 1.22 (stdlib net/http, embed, html/template not needed), vanilla JS. Existing deps only (fsnotify, yaml.v3).

---

## Global Constraints

| Constraint | Value | Source |
|---|---|---|
| Language | Go 1.22, package `main` (flat layout) | spec §2.2 |
| UI listen | `UI_LISTEN` / `ui_listen`, default `":8099"`; empty/`-` disables | spec §4.4 |
| Auth | none | spec §1.3 |
| Frontend | vanilla HTML/CSS/JS, `//go:embed web` | spec §2.2, §5 |
| Config save | validate → atomic write → hot reload | spec §3.4, §2.3 |
| Retry | delete records → re-enqueue via existing pipeline state machine | spec §3.5 |
| Unsynced | video ext + size > MinFileSize + no record | spec §3.2, §1.3 |
| No behavior change | pipeline upload/cleanup semantics unchanged | spec §1.2 |
| Existing files | `config.go`, `state.go`, `pipeline.go`, `cleanup.go`, `main.go`, `web/` | — |

---

## Task 1: StateManager ListAll + Delete + mutex

**Files:**
- Modify: `cloud-sync/state.go`
- Modify: `cloud-sync/state_test.go`

**Interfaces produced:**

```go
// ListAll returns every record across all date buckets and FAILED/<date>/.
// Each returned record's Key is its path relative to the bucket root.
func (s *StateManager) ListAll() ([]*StatusRecord, error)

// Delete removes key's record from every date bucket and FAILED/<date>/.
// Missing records are not an error. Empty parent dirs are removed best-effort.
func (s *StateManager) Delete(key string) error
```

- Add `mu sync.Mutex` to `StateManager`; guard `Write` and `Update` (atomic writes already safe per-file; the mutex protects Delete racing a write for the same file).
- `ListAll`: walk `s.root`; for each entry that is a date dir (or `FAILED/<date>`), walk `.json` files, unmarshal, set `rec.Key = rel path minus .json`, append. Skip `FAILED` itself as a record (descend into it), skip unparseable (log warn).
- `Delete`: compute `<root>/<date>/<filepath.FromSlash(key)>.json` for every date dir present and `<root>/FAILED/<date>/...`; `os.Remove` each (ignore not-exist); then best-effort remove empty parent dirs up to the bucket root.

**Tests (`state_test.go`):**
1. `TestState_ListAll_MixedBuckets` — write 1 synced (today), 1 failed (today), 1 synced (yesterday) → ListAll returns 3 with correct Keys/Status.
2. `TestState_Delete_RemovesAcrossBuckets` — write synced + failed for same key → Delete(key) → both gone, AlreadySynced false.
3. `TestState_Delete_MissingIsNoError` — Delete("nope.mkv") returns nil.

---

## Task 2: Config ui_listen + Pipeline.Process + Cleanup.TickNow

**Files:**
- Modify: `cloud-sync/config.go`
- Modify: `cloud-sync/config_test.go`
- Modify: `cloud-sync/pipeline.go`
- Modify: `cloud-sync/cleanup.go`

**Interfaces produced:**

```go
// Config
UIListen string  // default ":8099"; "" or "-" disables

// fileConfig
UIListen *string `yaml:"ui_listen"`

// Pipeline
func (p *Pipeline) Process(ctx context.Context, ev FileEvent)   // wraps process()

// Cleanup
func (c *Cleanup) TickNow(ctx context.Context) (int, error)     // one tick, returns processed count
```

- `loadFromEnv`: `if v, ok := os.LookupEnv("UI_LISTEN"); ok { cfg.UIListen = v } else { cfg.UIListen = ":8099" }`.
- `loadWithFile`: `if f.UIListen != nil { os.Setenv("UI_LISTEN", *f.UIListen) }`.
- `Pipeline.Process` → `p.process(ctx, ev)`.
- `Cleanup`: change `Tick` to count processed records and return it. Options: keep `Tick(ctx, now) error` for backward tests and add an internal `tick(ctx, now) (int, error)`; `Tick` calls `tick` discarding count; `TickNow(ctx)` calls `tick(ctx, time.Now())`. Provide `TickNow` returning `(int, error)`.

**Tests:**
- `config_test.go`:
  1. `TestLoad_UIListen_Default` — unset → `:8099`.
  2. `TestLoad_UIListen_EnvOverridesDefault` — `UI_LISTEN=:9000` → `:9000`.
  3. `TestLoad_UIListen_ExplicitEmptyDisables` — `UI_LISTEN=` (set empty) → `""`.
  4. `TestLoad_FileUIListen_OverridesEnv` — YAML `ui_listen: ":7000"` → `:7000`.
- `pipeline_test.go`: `TestPipeline_Process_Enqueues` — call `Process` with a written video + mock uploader, assert one Copy call + synced record (mirror HappyPath but via `Process`).
- `cleanup_test.go`: `TestCleanup_TickNow_ReturnsCount` — seed 2 due records → TickNow returns ≥2 (dry-run).

---

## Task 3: Supervisor + main.go refactor

**Files:**
- Create: `cloud-sync/supervisor.go`
- Create: `cloud-sync/supervisor_test.go`
- Modify: `cloud-sync/main.go`

**Interfaces produced:**

```go
type SupervisorDeps struct {
    NewUploader func(cfg *Config, log *slog.Logger) Uploader
}

type Generation struct { /* unexported fields */ }

type Supervisor struct { /* unexported fields */ }

func NewSupervisor(cfgPath string, cfg *Config, log *slog.Logger, deps SupervisorDeps) *Supervisor
func (s *Supervisor) Start(ctx context.Context) error
func (s *Supervisor) Reload(ctx context.Context) error
func (s *Supervisor) Stop()
// Snapshot returns the current generation's config/state/pipeline (nil if not started).
func (s *Supervisor) Snapshot() (cfg *Config, st *StateManager, pl *Pipeline, ok bool)
// LastError reports the most recent Start/Reload failure (empty if healthy).
func (s *Supervisor) LastError() string
// StartedAt / GenerationCount for /api/status.
func (s *Supervisor) StartedAt() time.Time
```

Behavior:
- `Start`: build uploader via `deps.NewUploader` (default in `NewSupervisor` = real `NewClient`); if uploader implements `interface{ Ping(context.Context) error }`, call Ping (error → log, continue, still start); `NewStateManager`+`EnsureDirs` (error → return); `NewWatcher`; `NewPipeline`; `NewCleanup`; derive `genCtx, cancel := context.WithCancel(ctx)`; `wg.Add(3)`; goroutines: StartupScan, `pipeline.Run(genCtx, watcher.Events())`, `cleanup.Run(genCtx)`; store generation. On any error before goroutines start, return it and leave `gen=nil` + `lastErr`.
- `Stop`: cancel gen, `wg.Wait()`, `watcher.Close()`; `gen=nil`.
- `Reload(ctx)`: `Load(s.cfgPath)` (error → return, keep old gen); `Init(logLevel, logFile)` + `slog.SetDefault`; `Stop()`; swap cfg; `Start(ctx)`.
- Guard all with `s.mu`.

`main.go` refactor to the spec §4.5 shape; start the web server when `cfg.UIListen != "" && cfg.UIListen != "-"`.

**Tests (`supervisor_test.go`)** — inject `SupervisorDeps{NewUploader: mockFactory}`:
1. `TestSupervisor_StartStop` — temp dirs; Start succeeds; Snapshot ok; Stop clean.
2. `TestSupervisor_Reload` — Start; modify config file (e.g. upload_concurrency); Reload; Snapshot returns new cfg value; old generation stopped.
3. `TestSupervisor_StartFailure_StaysDown` — WatchDirs points at nonexistent dir → Start errors; Snapshot ok=false; LastError non-empty; no panic.

---

## Task 4: Web server + API + embed

**Files:**
- Create: `cloud-sync/web.go`
- Create: `cloud-sync/api.go`
- Create: `cloud-sync/web/index.html` (placeholder for T4; T5 replaces with the full UI)
- Create: `cloud-sync/web/app.js` (placeholder)
- Create: `cloud-sync/web/style.css` (placeholder)
- Create: `cloud-sync/web_test.go`
- Create: `cloud-sync/api_test.go`

**Interfaces:**

```go
type WebServer struct { /* sup, log */ }
func NewWebServer(sup *Supervisor, log *slog.Logger) *WebServer
func (w *WebServer) Serve(ctx context.Context, addr string) error   // blocks until ctx done / error
func (w *WebServer) Handler() http.Handler                          // for tests
```

Routes (in `Handler()`):
- `GET /` and static → embedded FS from `//go:embed web`.
- `GET /api/status`
- `GET /api/files`
- `GET /api/config`
- `PUT /api/config`
- `POST /api/retry`
- `POST /api/cleanup/run`

`//go:embed web` in `web.go`; serve via `http.FileServer(http.FS(sub))`.

API behavior: per spec §3. Helpers in `api.go`:
- `readAllRecords(st)`, `unsyncedFiles(cfg, st)` (walk WatchDirs, `shouldEmit`, `AlreadySynced`), merge + filter + paginate.
- `statusPayload(sup)` builds the JSON; redacts token by omission.
- config PUT: temp-file validate via `Load`, atomic write to `cfgPath` (supervisor exposes `ConfigPath()`), `Reload`, return.
- retry: for each key `st.Delete(key)`, resolve src path, missing → result error, else `go pl.Process(ctx, FileEvent{...})`.
- cleanup/run: `cl.TickNow(ctx)`. Needs cleanup in Snapshot → extend `Snapshot()` to also return `*Cleanup` (or add `Cleanup()` accessor).

**Tests:**
- `api_test.go` (httptest.NewServer(w.Handler())):
  1. `TestAPI_Status_OK` — started supervisor; counts present; no `openlist_token` key.
  2. `TestAPI_Status_Degraded` — supervisor not started → `ok:false`, `error` non-empty.
  3. `TestAPI_Files_FiltersAndPaginates` — seed records; `state=synced`, `page_size=1` → total/paging correct.
  4. `TestAPI_Files_Unsynced` — temp watch dir with a >minSize `.mkv` and no record → appears in `state=unsynced`.
  5. `TestAPI_Config_PutValidReloads` — PUT valid YAML → 200; file written; reload effect visible in `/api/status`.
  6. `TestAPI_Config_PutInvalidRejected` — malformed YAML → 400; file unchanged.
  7. `TestAPI_Retry_DeletesAndEnqueues` — seed failed record; PUT retry → record deleted; mock uploader received a Copy (allow brief wait).
- `web_test.go`:
  1. `TestWeb_IndexServed` — GET `/` → 200, `text/html`.
  2. `TestWeb_UnknownAPINotFound` — GET `/api/nope` → 404.

---

## Task 5: Frontend

**Files:**
- Modify (replace placeholders): `cloud-sync/web/index.html`, `cloud-sync/web/app.js`, `cloud-sync/web/style.css`

Implement the three tabs per spec §5 (Dashboard / Files / Config). Vanilla JS, `fetch` against the API. No external CDN (offline-friendly). Requirements:
- Dashboard cards + `/api/cleanup/run` button + 5s polling of `/api/status`.
- Files table with state tabs, search, pagination, row checkboxes, retry selected / retry row; toast results.
- Config textarea + save (calls PUT, shows errors), read-only effective config.
- Minimal, clean CSS (dark/light neutral), responsive enough for desktop.

No automated JS tests (no build chain); verified via the smoke script and manual check. Keep `index.html` referencing `/app.js` and `/style.css`.

---

## Task 6: Docs + deployment + smoke

**Files:**
- Modify: `config.example.yaml` — add `ui_listen: ":8099"` with comment.
- Modify: `.env.example` — add `UI_LISTEN=:8099`.
- Modify: `docker-compose.yml` — cloud-sync `ports: ["8099:8099"]`; config mount `:ro` → `:rw`.
- Modify: `README.md` (root) — Web UI section.
- Modify: `cloud-sync/README.md` — Web UI + `ui_listen` row + no-auth warning.
- Modify: `scripts/smoke.sh` (and `smoke-config.sh`) — after the binary runs, `curl -s localhost:PORT/api/status` and `/api/files` and assert HTTP 200 + expected counts. Use a non-default port to avoid clashes.

---

## Self-review checklist

- Every spec §3 endpoint has a Task 4 test.
- `Snapshot`/accessors cover pipeline (retry) and cleanup (cleanup/run).
- No step changes pipeline/cleanup upload semantics.
- `main.go` no longer exits on ping failure (spec §2.5) — this is the only intentional behavior change; documented.
- `web/` placeholders exist before `//go:embed web` compiles (T4), replaced in T5.
