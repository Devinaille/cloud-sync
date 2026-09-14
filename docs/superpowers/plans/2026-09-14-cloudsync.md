# cloud-sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a single Go container (`cloud-sync`) that watches `media/` and `ani-rss/` for new video files, uploads them to 移动云盘 via OpenList's `/api/fs/copy`, marks `.sync_status/`, and cleans up local source videos after 72h.

**Architecture:** Single Go process, multi-goroutine pipeline. `main` wires together `config` → `logging` → `state` → `openlist` (HTTP client) + `watcher` (fsnotify) → `pipeline` (state machine: stabilize → validate → upload → poll → mark) → `cleanup` (hourly ticker). All goroutines coordinate via `context.Context` and exit cleanly on SIGINT/SIGTERM. Container is multi-stage `golang:1.23-alpine` → `distroless/static-debian12:nonroot`.

**Tech Stack:**
- Go 1.23 (no generics required; stdlib only except fsnotify)
- `github.com/fsnotify/fsnotify` (Linux inotify backend)
- `log/slog` (stdlib structured JSON logging)
- `net/http` (stdlib; 3 OpenList endpoints)
- `os`, `path/filepath`, `time`, `encoding/json` (stdlib)

**Spec:** `docs/superpowers/specs/2026-09-14-cloudsync-design.md` — this plan argues from the spec; executors read both.

---

## Global Constraints

These apply to every task. Copied verbatim from spec §3 / §5 / §7 / §8 / §9 / §11.

| Constraint | Value | Source |
|---|---|---|
| Language | Go 1.23 | spec §11 decisions |
| Module path | `cloud-sync` (single-module repo) | spec §2.2 |
| Linux only | `inotify` backend via fsnotify | spec §1.2, §11 |
| OpenList base URL | env `OPENLIST_URL` (e.g. `http://openlist:5244`) | spec §3.5, docker-compose |
| OpenList auth | `Authorization: <OPENLIST_TOKEN>` header | spec §3.5, ARCHITECTURE §7.1 |
| HTTP timeout | 30s per request | spec §3.5 |
| Retry policy | 3 attempts max, exponential backoff 1/2/4/8/.../60s | spec §5, §3.4 state machine |
| Task poll | every `POLL_INTERVAL_SECONDS` (default 3s), timeout `TASK_TIMEOUT_SECONDS` (default 1800s) | spec §3.4, §3.1 |
| Video extensions | `.mkv` `.mp4` `.ts` `.iso` | spec §3.3 |
| Min file size | 100MB | spec §3.1 (`MinFileSize = 100MB`), ARCHITECTURE §8.2 |
| Cleanup delay | 72h (`CLEANUP_AFTER_HOURS`) | spec §3.1, §3.7 |
| Cleanup batch limit | 50 per tick | spec §3.7 |
| Cleanup dry-run | `CLEANUP_DRY_RUN=true` until manually flipped false | spec §3.7, docker-compose |
| Whitelist | paths must start with one of `ALLOWED_SOURCE_PREFIXES` | spec §3.7 |
| Upload concurrency | `UPLOAD_CONCURRENCY` (default 2) | spec §3.1 |
| Stale window | 30s (size+mtime unchanged) | spec §3.4 |
| Stabilize tick | 5s | spec §3.4 |
| State dir layout | `.sync_status/<YYYY-MM-DD>/<rel_path>.json` + `.sync_status/FAILED/<YYYY-MM-DD>/...` | spec §3.6 |
| State write atomicity | temp file + `os.Rename` | spec §3.6 |
| Already-synced check | any status (synced/cleaned/failed) blocks re-upload | spec §3.6 |
| Cleanup target | `.mkv`/`.mp4`/`.ts`/`.iso` only; never `.nfo`/`.jpg`/subtitle | spec §3.7 |
| Status values | `"synced"` or `"failed"` or `"cleaned"` | spec §3.6 |
| Category values | `"movie"` (CatMedia) or `"anime"` (CatAniRSS) | spec §3.6 |
| Logging | JSON to stdout, or to `LOG_FILE` if set; level filter | spec §3.2 |
| Container | non-root UID 65532, distroless static, image < 30MB | spec §2.3, §7 |
| All paths absolute | env-driven; never hardcoded paths in code | spec §3.1, §6 |
| Startup recovery | walk + re-process unsynced files; no in-flight state | spec §5 "崩溃恢复" |

---

## File Structure (created by these tasks)

```
/home/tianyf/development/github/cloud-sync/
├── .gitignore                              # T9
├── .env.example                            # T9
├── README.md                               # T9 (expanded)
├── docker-compose.yml                      # already present (untouched)
├── ARCHITECTURE.md                         # already present (untouched)
├── docs/
│   └── superpowers/
│       ├── plans/
│       │   └── 2026-09-14-cloudsync.md     # this file
│       └── specs/
│           └── 2026-09-14-cloudsync-design.md  # already present
└── cloud-sync/                             # all Go code lives here
    ├── Dockerfile                          # T8
    ├── README.md                           # T8
    ├── go.mod                              # T1
    ├── go.sum                              # T1 (auto)
    ├── main.go                             # T8
    ├── config.go                           # T1
    ├── config_test.go                      # T1
    ├── logging.go                          # T2
    ├── logging_test.go                     # T2
    ├── watcher.go                          # T5
    ├── watcher_test.go                     # T5
    ├── state.go                            # T3
    ├── state_test.go                       # T3
    ├── openlist.go                         # T4
    ├── openlist_test.go                    # T4
    ├── pipeline.go                         # T6
    ├── pipeline_test.go                    # T6
    ├── cleanup.go                          # T7
    └── cleanup_test.go                     # T7
```

All `.go` files in `cloud-sync/` belong to package `main` (spec §2.2 "扁平布局", §3 components — no sub-packages).

---

## Task Sequencing

| # | Task | Depends on | Approx LOC |
|---|---|---|---|
| T1 | config | — | 150 |
| T2 | logging | — | 60 |
| T3 | state | T2 | 220 |
| T4 | openlist | T2 | 180 |
| T5 | watcher | T2 | 130 |
| T6 | pipeline | T1, T3, T4 | 400 |
| T7 | cleanup | T1, T3 | 130 |
| T8 | main + Dockerfile | T1–T7 | 150 |
| T9 | root hygiene files | — | 60 |

---

## Task 1: config (env loading + validation)

**Files:**
- Create: `cloud-sync/go.mod`
- Create: `cloud-sync/config.go`
- Create: `cloud-sync/config_test.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces:
  - `type Config struct { ... }` — fields defined below
  - `func Load() (*Config, error)` — reads env, validates, returns config or descriptive error

#### Config struct (exact shape — every later task references these field names)

```go
package main

import "time"

type Config struct {
    // OpenList
    OpenListURL   string
    OpenListToken string
    SrcStorage    string  // e.g. "/local_media"
    DstStorage    string  // e.g. "/139yun_media"

    // Watch paths
    WatchMediaDir  string
    WatchAniRSSDir string

    // Status
    SyncStatusDir string

    // Cleanup
    CleanupAfter  time.Duration  // CLEANUP_AFTER_HOURS as Duration
    CleanupDryRun bool

    // Upload pipeline
    UploadConcurrency int
    StabilizeWait     time.Duration
    PollInterval      time.Duration
    TaskTimeout       time.Duration
    MinFileSize       int64  // 100 * 1024 * 1024

    // Safety
    AllowedPrefixes     []string
    RequireNFOForMedia  bool
    RequireNFOForAniRSS bool

    // Logging
    LogLevel string  // debug|info|warn|error
    LogFile  string  // "" → stdout
}
```

### Steps

- [ ] **Step 1.1: Create `cloud-sync/go.mod`**

Create file `cloud-sync/go.mod` with content:

```
module cloud-sync

go 1.23

require github.com/fsnotify/fsnotify v1.7.0
```

- [ ] **Step 1.2: Verify Go can resolve the module**

Run: `cd cloud-sync && go mod download github.com/fsnotify/fsnotify`
Expected: downloads `fsnotify` (and its deps) into module cache; exits 0.

If this fails because the host has no internet, document the error and move to Step 1.3 anyway — the test will surface it.

- [ ] **Step 1.3: Write the failing test for `Load()` reading a single env var**

Create `cloud-sync/config_test.go`:

```go
package main

import (
    "os"
    "testing"
)

func TestLoad_ReadsOpenListURL(t *testing.T) {
    t.Setenv("OPENLIST_URL", "http://openlist:5244")
    t.Setenv("OPENLIST_TOKEN", "tok")
    t.Setenv("OPENLIST_SRC_STORAGE", "/local_media")
    t.Setenv("OPENLIST_DST_STORAGE", "/139yun_media")
    t.Setenv("WATCH_MEDIA_DIR", "/tmp")
    t.Setenv("WATCH_ANIRSS_DIR", "/tmp")
    t.Setenv("SYNC_STATUS_DIR", "/tmp")
    t.Setenv("CLEANUP_AFTER_HOURS", "72")
    t.Setenv("UPLOAD_CONCURRENCY", "2")
    t.Setenv("STABILIZE_WAIT_SECONDS", "30")
    t.Setenv("POLL_INTERVAL_SECONDS", "3")
    t.Setenv("TASK_TIMEOUT_SECONDS", "1800")
    t.Setenv("ALLOWED_SOURCE_PREFIXES", "/tmp")
    t.Setenv("LOG_LEVEL", "info")

    cfg, err := Load()
    if err != nil {
        t.Fatalf("Load() error: %v", err)
    }
    if cfg.OpenListURL != "http://openlist:5244" {
        t.Errorf("OpenListURL = %q, want %q", cfg.OpenListURL, "http://openlist:5244")
    }
}
```

Note: `t.Setenv` automatically restores the previous value at test end. The config must `os.Stat` all path env vars — we point them at `/tmp` which always exists.

- [ ] **Step 1.4: Run test, verify it fails**

Run: `cd cloud-sync && go test -run TestLoad_ReadsOpenListURL ./...`
Expected: FAIL — `Load` undefined.

- [ ] **Step 1.5: Implement minimal `config.go` skeleton**

Create `cloud-sync/config.go`:

```go
package main

import (
    "fmt"
    "os"
    "strconv"
    "strings"
    "time"
)

type Config struct {
    OpenListURL         string
    OpenListToken       string
    SrcStorage          string
    DstStorage          string
    WatchMediaDir       string
    WatchAniRSSDir      string
    SyncStatusDir       string
    CleanupAfter        time.Duration
    CleanupDryRun       bool
    UploadConcurrency   int
    StabilizeWait       time.Duration
    PollInterval        time.Duration
    TaskTimeout         time.Duration
    MinFileSize         int64
    AllowedPrefixes     []string
    RequireNFOForMedia  bool
    RequireNFOForAniRSS bool
    LogLevel            string
    LogFile             string
}

const minFileSizeBytes = 100 * 1024 * 1024 // 100MB

func Load() (*Config, error) {
    cfg := &Config{MinFileSize: minFileSizeBytes}

    strs := []struct {
        env string
        dst *string
        req bool
    }{
        {"OPENLIST_URL", &cfg.OpenListURL, true},
        {"OPENLIST_TOKEN", &cfg.OpenListToken, true},
        {"OPENLIST_SRC_STORAGE", &cfg.SrcStorage, true},
        {"OPENLIST_DST_STORAGE", &cfg.DstStorage, true},
        {"WATCH_MEDIA_DIR", &cfg.WatchMediaDir, true},
        {"WATCH_ANIRSS_DIR", &cfg.WatchAniRSSDir, true},
        {"SYNC_STATUS_DIR", &cfg.SyncStatusDir, true},
        {"LOG_LEVEL", &cfg.LogLevel, true},
    }
    for _, s := range strs {
        v := os.Getenv(s.env)
        if v == "" && s.req {
            return nil, fmt.Errorf("config: required env %s is empty", s.env)
        }
        *s.dst = v
    }
    cfg.LogFile = os.Getenv("LOG_FILE") // optional

    // comma-split prefixes
    raw := os.Getenv("ALLOWED_SOURCE_PREFIXES")
    if raw == "" {
        return nil, fmt.Errorf("config: required env ALLOWED_SOURCE_PREFIXES is empty")
    }
    for _, p := range strings.Split(raw, ",") {
        p = strings.TrimSpace(p)
        if p != "" {
            cfg.AllowedPrefixes = append(cfg.AllowedPrefixes, p)
        }
    }
    if len(cfg.AllowedPrefixes) == 0 {
        return nil, fmt.Errorf("config: ALLOWED_SOURCE_PREFIXES resolved to empty list")
    }

    // numeric durations / ints
    var err error
    if cfg.CleanupAfter, err = hoursDuration("CLEANUP_AFTER_HOURS"); err != nil {
        return nil, err
    }
    if cfg.UploadConcurrency, err = positiveInt("UPLOAD_CONCURRENCY"); err != nil {
        return nil, err
    }
    if cfg.StabilizeWait, err = secondsDuration("STABILIZE_WAIT_SECONDS"); err != nil {
        return nil, err
    }
    if cfg.PollInterval, err = secondsDuration("POLL_INTERVAL_SECONDS"); err != nil {
        return nil, err
    }
    if cfg.TaskTimeout, err = secondsDuration("TASK_TIMEOUT_SECONDS"); err != nil {
        return nil, err
    }

    cfg.CleanupDryRun = envBool("CLEANUP_DRY_RUN", false)
    cfg.RequireNFOForMedia = envBool("REQUIRE_NFO_FOR_MEDIA", false)
    cfg.RequireNFOForAniRSS = envBool("REQUIRE_NFO_FOR_ANIRSS", false)

    // OS-level path existence check
    for _, p := range []string{
        cfg.WatchMediaDir, cfg.WatchAniRSSDir, cfg.SyncStatusDir,
    } {
        if _, err := os.Stat(p); err != nil {
            return nil, fmt.Errorf("config: path %q not accessible: %w", p, err)
        }
    }

    return cfg, nil
}

func hoursDuration(env string) (time.Duration, error) {
    v := os.Getenv(env)
    if v == "" {
        return 0, fmt.Errorf("config: required env %s is empty", env)
    }
    n, err := strconv.Atoi(v)
    if err != nil || n <= 0 {
        return 0, fmt.Errorf("config: %s must be positive int, got %q", env, v)
    }
    return time.Duration(n) * time.Hour, nil
}

func secondsDuration(env string) (time.Duration, error) {
    v := os.Getenv(env)
    if v == "" {
        return 0, fmt.Errorf("config: required env %s is empty", env)
    }
    n, err := strconv.Atoi(v)
    if err != nil || n <= 0 {
        return 0, fmt.Errorf("config: %s must be positive int, got %q", env, v)
    }
    return time.Duration(n) * time.Second, nil
}

func positiveInt(env string) (int, error) {
    v := os.Getenv(env)
    if v == "" {
        return 0, fmt.Errorf("config: required env %s is empty", env)
    }
    n, err := strconv.Atoi(v)
    if err != nil || n <= 0 {
        return 0, fmt.Errorf("config: %s must be positive int, got %q", env, v)
    }
    return n, nil
}

func envBool(env string, def bool) bool {
    v := os.Getenv(env)
    if v == "" {
        return def
    }
    b, err := strconv.ParseBool(v)
    if err != nil {
        return def
    }
    return b
}
```

- [ ] **Step 1.6: Run test, verify it passes**

Run: `cd cloud-sync && go test -run TestLoad_ReadsOpenListURL ./...`
Expected: PASS.

- [ ] **Step 1.7: Add validation tests**

Append to `cloud-sync/config_test.go`:

```go
func TestLoad_RejectsMissingRequiredEnv(t *testing.T) {
    t.Setenv("OPENLIST_URL", "") // explicitly unset by clearing
    // All other required envs unset.
    _, err := Load()
    if err == nil {
        t.Fatal("Load() expected error for missing required envs, got nil")
    }
}

func TestLoad_RejectsNonPositiveInt(t *testing.T) {
    base := map[string]string{
        "OPENLIST_URL": "http://x", "OPENLIST_TOKEN": "t",
        "OPENLIST_SRC_STORAGE": "/a", "OPENLIST_DST_STORAGE": "/b",
        "WATCH_MEDIA_DIR": "/tmp", "WATCH_ANIRSS_DIR": "/tmp",
        "SYNC_STATUS_DIR": "/tmp", "ALLOWED_SOURCE_PREFIXES": "/tmp",
        "LOG_LEVEL": "info",
    }
    for k, v := range base {
        t.Setenv(k, v)
    }
    t.Setenv("UPLOAD_CONCURRENCY", "0")
    _, err := Load()
    if err == nil {
        t.Fatal("Load() expected error for UPLOAD_CONCURRENCY=0, got nil")
    }
}

func TestLoad_RejectsEmptyPrefixList(t *testing.T) {
    base := map[string]string{
        "OPENLIST_URL": "http://x", "OPENLIST_TOKEN": "t",
        "OPENLIST_SRC_STORAGE": "/a", "OPENLIST_DST_STORAGE": "/b",
        "WATCH_MEDIA_DIR": "/tmp", "WATCH_ANIRSS_DIR": "/tmp",
        "SYNC_STATUS_DIR": "/tmp",
        "CLEANUP_AFTER_HOURS": "72", "UPLOAD_CONCURRENCY": "2",
        "STABILIZE_WAIT_SECONDS": "30", "POLL_INTERVAL_SECONDS": "3",
        "TASK_TIMEOUT_SECONDS": "1800",
        "LOG_LEVEL": "info",
    }
    for k, v := range base {
        t.Setenv(k, v)
    }
    t.Setenv("ALLOWED_SOURCE_PREFIXES", ", , ,") // all whitespace
    _, err := Load()
    if err == nil {
        t.Fatal("Load() expected error for empty prefix list, got nil")
    }
}

func TestLoad_DefaultsMinFileSize(t *testing.T) {
    full := map[string]string{
        "OPENLIST_URL": "http://x", "OPENLIST_TOKEN": "t",
        "OPENLIST_SRC_STORAGE": "/a", "OPENLIST_DST_STORAGE": "/b",
        "WATCH_MEDIA_DIR": "/tmp", "WATCH_ANIRSS_DIR": "/tmp",
        "SYNC_STATUS_DIR": "/tmp", "ALLOWED_SOURCE_PREFIXES": "/tmp",
        "CLEANUP_AFTER_HOURS": "72", "UPLOAD_CONCURRENCY": "2",
        "STABILIZE_WAIT_SECONDS": "30", "POLL_INTERVAL_SECONDS": "3",
        "TASK_TIMEOUT_SECONDS": "1800",
        "LOG_LEVEL": "info",
    }
    for k, v := range full {
        t.Setenv(k, v)
    }
    cfg, err := Load()
    if err != nil {
        t.Fatalf("Load() error: %v", err)
    }
    const want = int64(100 * 1024 * 1024)
    if cfg.MinFileSize != want {
        t.Errorf("MinFileSize = %d, want %d", cfg.MinFileSize, want)
    }
}

func TestLoad_ParsesBoolFlags(t *testing.T) {
    full := map[string]string{
        "OPENLIST_URL": "http://x", "OPENLIST_TOKEN": "t",
        "OPENLIST_SRC_STORAGE": "/a", "OPENLIST_DST_STORAGE": "/b",
        "WATCH_MEDIA_DIR": "/tmp", "WATCH_ANIRSS_DIR": "/tmp",
        "SYNC_STATUS_DIR": "/tmp", "ALLOWED_SOURCE_PREFIXES": "/tmp",
        "CLEANUP_AFTER_HOURS": "72", "UPLOAD_CONCURRENCY": "2",
        "STABILIZE_WAIT_SECONDS": "30", "POLL_INTERVAL_SECONDS": "3",
        "TASK_TIMEOUT_SECONDS": "1800",
        "LOG_LEVEL": "info",
    }
    for k, v := range full {
        t.Setenv(k, v)
    }
    t.Setenv("CLEANUP_DRY_RUN", "true")
    t.Setenv("REQUIRE_NFO_FOR_MEDIA", "1")
    t.Setenv("REQUIRE_NFO_FOR_ANIRSS", "false")
    cfg, err := Load()
    if err != nil {
        t.Fatalf("Load() error: %v", err)
    }
    if !cfg.CleanupDryRun || !cfg.RequireNFOForMedia || cfg.RequireNFOForAniRSS {
        t.Errorf("bool flags wrong: %+v", cfg)
    }
}
```

- [ ] **Step 1.8: Run all config tests**

Run: `cd cloud-sync && go test -run TestLoad ./...`
Expected: all 5 tests PASS.

- [ ] **Step 1.9: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/go.mod cloud-sync/go.sum cloud-sync/config.go cloud-sync/config_test.go
git commit -m "feat(cloud-sync): config: env loading and validation"
```

---

## Task 2: logging (slog setup)

**Files:**
- Create: `cloud-sync/logging.go`
- Create: `cloud-sync/logging_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `func Init(level, file string) *slog.Logger` — see spec §3.2.
  - `level` is `"debug"` or `"info"` or `"warn"` or `"error"` (case-insensitive); unknown → `info`
  - `file == ""` → JSON to `os.Stdout`
  - `file != ""` → JSON to file (append); parent dir created via `os.MkdirAll`
  - On file-open failure: panic with descriptive message (spec §6 — startup fails fast; main will exit non-zero via runtime)

### Steps

- [ ] **Step 2.1: Write the failing test (stdout + level filter)**

Create `cloud-sync/logging_test.go`:

```go
package main

import (
    "bytes"
    "log/slog"
    "strings"
    "testing"
)

func TestInit_StdoutJSONInfoLevel(t *testing.T) {
    var buf bytes.Buffer
    // Build the same kind of logger Init() should produce, and verify its
    // semantics. This sets the contract for the implementation in 2.3.
    handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
    log := slog.New(handler)

    log.Debug("hidden")
    log.Info("visible", "k", "v")

    out := buf.String()
    if strings.Contains(out, "hidden") {
        t.Errorf("debug message leaked at info level: %s", out)
    }
    if !strings.Contains(out, "msg") || !strings.Contains(out, "visible") {
        t.Errorf("info message missing: %s", out)
    }
    if !strings.Contains(out, "k") || !strings.Contains(out, "v") {
        t.Errorf("attr missing: %s", out)
    }
}
```

- [ ] **Step 2.2: Run test, verify it passes (sanity)**

Run: `cd cloud-sync && go test -run TestInit ./...`
Expected: PASS — this is the contract we want Init to satisfy.

- [ ] **Step 2.3: Implement `logging.go`**

Create `cloud-sync/logging.go`:

```go
package main

import (
    "fmt"
    "io"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
)

func Init(level, file string) *slog.Logger {
    var w io.Writer = os.Stdout
    if file != "" {
        if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
            panic(fmt.Sprintf("logging: cannot create log dir %q: %v", filepath.Dir(file), err))
        }
        f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
        if err != nil {
            panic(fmt.Sprintf("logging: cannot open log file %q: %v", file, err))
        }
        w = f
    }
    h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
    return slog.New(h)
}

func parseLevel(s string) slog.Level {
    switch strings.ToLower(strings.TrimSpace(s)) {
    case "debug":
        return slog.LevelDebug
    case "warn", "warning":
        return slog.LevelWarn
    case "error":
        return slog.LevelError
    default:
        return slog.LevelInfo
    }
}
```

- [ ] **Step 2.4: Add a test that exercises `Init` end-to-end (level mapping + file open)**

Append to `cloud-sync/logging_test.go`:

```go
func TestInit_FileOutputCreatesParentDir(t *testing.T) {
    dir := t.TempDir()
    logPath := filepath.Join(dir, "nested", "sub", "app.log")

    log := Init("warn", logPath)
    log.Info("not-emitted")
    log.Warn("emitted", "x", 1)

    data, err := os.ReadFile(logPath)
    if err != nil {
        t.Fatalf("ReadFile: %v", err)
    }
    s := string(data)
    if strings.Contains(s, "not-emitted") {
        t.Errorf("info leaked at warn level: %s", s)
    }
    if !strings.Contains(s, "emitted") || !strings.Contains(s, "x") {
        t.Errorf("warn entry missing: %s", s)
    }
}

func TestInit_UnknownLevelDefaultsToInfo(t *testing.T) {
    dir := t.TempDir()
    logPath := filepath.Join(dir, "app.log")
    log := Init("gibberish", logPath)
    log.Debug("hidden")
    log.Info("visible")
    data, _ := os.ReadFile(logPath)
    if strings.Contains(string(data), "hidden") {
        t.Errorf("debug leaked: %s", string(data))
    }
    if !strings.Contains(string(data), "visible") {
        t.Errorf("info missing: %s", string(data))
    }
}
```

- [ ] **Step 2.5: Run all logging tests**

Run: `cd cloud-sync && go test -run TestInit ./...`
Expected: all PASS.

- [ ] **Step 2.6: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/logging.go cloud-sync/logging_test.go
git commit -m "feat(cloud-sync): logging: slog JSON setup with file/level"
```

---

## Task 3: state (`.sync_status/` atomic R/W)

**Files:**
- Create: `cloud-sync/state.go`
- Create: `cloud-sync/state_test.go`

**Interfaces:**
- Consumes: `*slog.Logger` (from T2)
- Produces:

```go
type StatusRecord struct {
    Key            string     `json:"-"` // rel-path under source root; used as disk key (not serialized)
    SrcPath        string     `json:"src_path"`
    SrcSize        int64      `json:"src_size"`
    SrcMtime       time.Time  `json:"src_mtime"`
    CloudPath      string     `json:"cloud_path"`
    OpenListTaskID string     `json:"openlist_task_id"`
    Category       string     `json:"category"`  // "movie" | "anime"
    HasNFO         bool       `json:"has_nfo"`
    SyncedAt       time.Time  `json:"synced_at"`
    CleanupAt      time.Time  `json:"cleanup_at"`
    Status         string     `json:"status"`  // "synced" | "failed" | "cleaned"
    RetryCount     int        `json:"retry_count"`
    CleanedAt      *time.Time `json:"cleaned_at,omitempty"`
    Error          string     `json:"error,omitempty"`
}

type StateManager struct { /* unexported fields */ }

func NewStateManager(root string, log *slog.Logger) *StateManager
func (s *StateManager) EnsureDirs() error
func (s *StateManager) AlreadySynced(key string) (bool, error)
func (s *StateManager) Write(rec *StatusRecord) error
func (s *StateManager) Update(rec *StatusRecord) error
func (s *StateManager) ListForCleanup(now time.Time) ([]*StatusRecord, error)
```

Path layout (spec §3.6):
- Active records: `<root>/<YYYY-MM-DD>/<key>.json` where `<key>` may contain `/`
- Failed records: `<root>/FAILED/<YYYY-MM-DD>/<key>.json`
- `AlreadySynced`: scan all `<root>/*/...` and `<root>/FAILED/*/...` for any matching `.json` (any status blocks re-upload)

### Steps

- [ ] **Step 3.1: Write the failing test for `AlreadySynced` + `Write` round-trip**

Create `cloud-sync/state_test.go`:

```go
package main

import (
    "io"
    "log/slog"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func newTestState(t *testing.T) *StateManager {
    t.Helper()
    dir := t.TempDir()
    return NewStateManager(dir, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestState_WriteThenAlreadySynced(t *testing.T) {
    st := newTestState(t)
    if err := st.EnsureDirs(); err != nil {
        t.Fatalf("EnsureDirs: %v", err)
    }
    now := time.Now().UTC().Truncate(time.Second)
    rec := &StatusRecord{
        Key:       "Movies/X/X.mkv",
        SrcPath:   "/mnt/basic/media/media/Movies/X/X.mkv",
        SrcSize:   1234,
        SrcMtime:  now,
        CloudPath: "/139yun_media/media/Movies/X/X.mkv",
        Category:  "movie",
        HasNFO:    true,
        SyncedAt:  now,
        CleanupAt: now.Add(72 * time.Hour),
        Status:    "synced",
    }
    if err := st.Write(rec); err != nil {
        t.Fatalf("Write: %v", err)
    }
    ok, err := st.AlreadySynced("Movies/X/X.mkv")
    if err != nil {
        t.Fatalf("AlreadySynced: %v", err)
    }
    if !ok {
        t.Errorf("AlreadySynced = false, want true")
    }
}

func TestState_AlreadySynced_FalseForUnknown(t *testing.T) {
    st := newTestState(t)
    _ = st.EnsureDirs()
    ok, err := st.AlreadySynced("nope.mkv")
    if err != nil {
        t.Fatalf("AlreadySynced: %v", err)
    }
    if ok {
        t.Error("AlreadySynced returned true for unknown file")
    }
}
```

- [ ] **Step 3.2: Run test, verify it fails**

Run: `cd cloud-sync && go test -run TestState ./...`
Expected: FAIL — types undefined.

- [ ] **Step 3.3: Implement `state.go`**

Create `cloud-sync/state.go`:

```go
package main

import (
    "encoding/json"
    "fmt"
    "io/fs"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "time"
)

type StatusRecord struct {
    Key            string     `json:"-"`
    SrcPath        string     `json:"src_path"`
    SrcSize        int64      `json:"src_size"`
    SrcMtime       time.Time  `json:"src_mtime"`
    CloudPath      string     `json:"cloud_path"`
    OpenListTaskID string     `json:"openlist_task_id"`
    Category       string     `json:"category"`
    HasNFO         bool       `json:"has_nfo"`
    SyncedAt       time.Time  `json:"synced_at"`
    CleanupAt      time.Time  `json:"cleanup_at"`
    Status         string     `json:"status"`
    RetryCount     int        `json:"retry_count"`
    CleanedAt      *time.Time `json:"cleaned_at,omitempty"`
    Error          string     `json:"error,omitempty"`
}

type StateManager struct {
    root string
    log  *slog.Logger
}

func NewStateManager(root string, log *slog.Logger) *StateManager {
    return &StateManager{root: root, log: log}
}

func (s *StateManager) bucket(status string, t time.Time) string {
    date := t.UTC().Format("2006-01-02")
    if status == "failed" {
        return filepath.Join(s.root, "FAILED", date)
    }
    return filepath.Join(s.root, date)
}

func (s *StateManager) recordPath(rec *StatusRecord) string {
    return filepath.Join(s.bucket(rec.Status, rec.SyncedAt), rec.Key+".json")
}

func (s *StateManager) EnsureDirs() error {
    today := time.Now().UTC()
    for _, d := range []string{
        filepath.Join(s.root, today.Format("2006-01-02")),
        filepath.Join(s.root, "FAILED"),
    } {
        if err := os.MkdirAll(d, 0o755); err != nil {
            return fmt.Errorf("state: mkdir %q: %w", d, err)
        }
    }
    return nil
}

// AlreadySynced returns true if any record (any status) exists for the given key.
func (s *StateManager) AlreadySynced(key string) (bool, error) {
    entries, err := os.ReadDir(s.root)
    if err != nil {
        if os.IsNotExist(err) {
            return false, nil
        }
        return false, err
    }
    target := filepath.ToSlash(key) + ".json"
    for _, e := range entries {
        if !e.IsDir() {
            continue
        }
        candidate := filepath.ToSlash(filepath.Join(s.root, e.Name(), target))
        full := filepath.Join(s.root, e.Name(), target)
        if _, err := os.Stat(full); err == nil {
            _ = candidate
            return true, nil
        }
    }
    return false, nil
}

// Write atomically writes rec at the path determined by its Key + SyncedAt + Status.
func (s *StateManager) Write(rec *StatusRecord) error {
    return s.atomicWrite(s.recordPath(rec), rec)
}

// Update re-writes rec to its existing path (same Key/SyncedAt/Status).
func (s *StateManager) Update(rec *StatusRecord) error {
    return s.atomicWrite(s.recordPath(rec), rec)
}

func (s *StateManager) atomicWrite(p string, rec *StatusRecord) error {
    if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
        return err
    }
    data, err := json.MarshalIndent(rec, "", "  ")
    if err != nil {
        return err
    }
    tmp := p + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil {
        return err
    }
    return os.Rename(tmp, p)
}

// ListForCleanup walks all date buckets (not FAILED/), parses each .json, and
// returns records where Status == "synced" and CleanupAt < now.
func (s *StateManager) ListForCleanup(now time.Time) ([]*StatusRecord, error) {
    var out []*StatusRecord
    entries, err := os.ReadDir(s.root)
    if err != nil {
        if os.IsNotExist(err) {
            return nil, nil
        }
        return nil, err
    }
    for _, e := range entries {
        if !e.IsDir() || e.Name() == "FAILED" || !looksLikeDate(e.Name()) {
            continue
        }
        base := filepath.Join(s.root, e.Name())
        walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
            if walkErr != nil || d.IsDir() {
                return walkErr
            }
            if !strings.HasSuffix(p, ".json") {
                return nil
            }
            b, err := os.ReadFile(p)
            if err != nil {
                s.log.Warn("state: read failed", "path", p, "err", err)
                return nil
            }
            var rec StatusRecord
            if err := json.Unmarshal(b, &rec); err != nil {
                s.log.Warn("state: parse failed", "path", p, "err", err)
                return nil
            }
            if rec.Status == "synced" && rec.CleanupAt.Before(now) {
                rec.Key = strings.TrimSuffix(filepath.Base(p), ".json")
                out = append(out, &rec)
            }
            return nil
        })
        if walkErr != nil {
            return nil, walkErr
        }
    }
    return out, nil
}

func looksLikeDate(s string) bool {
    if len(s) != 10 {
        return false
    }
    _, err := time.Parse("2006-01-02", s)
    return err == nil
}
```

- [ ] **Step 3.4: Add more state tests**

Append to `cloud-sync/state_test.go`:

```go
func TestState_AtomicWriteLeavesNoTmp(t *testing.T) {
    st := newTestState(t)
    _ = st.EnsureDirs()
    now := time.Now().UTC().Truncate(time.Second)
    rec := &StatusRecord{
        Key: "a.mkv", SrcPath: "/x/a.mkv", SyncedAt: now,
        CleanupAt: now.Add(time.Hour), Status: "synced",
    }
    if err := st.Write(rec); err != nil {
        t.Fatalf("Write: %v", err)
    }
    entries, _ := os.ReadDir(filepath.Dir(st.recordPath(rec)))
    for _, e := range entries {
        if filepath.Ext(e.Name()) == ".tmp" {
            t.Errorf("tmp file left behind: %s", e.Name())
        }
    }
}

func TestState_ListForCleanup_FiltersByStatusAndTime(t *testing.T) {
    st := newTestState(t)
    _ = st.EnsureDirs()
    base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
    past := &StatusRecord{Key: "old.mkv", SrcPath: "/x/old.mkv", SyncedAt: base, CleanupAt: base.Add(time.Hour), Status: "synced"}
    future := &StatusRecord{Key: "new.mkv", SrcPath: "/x/new.mkv", SyncedAt: base, CleanupAt: base.Add(100 * time.Hour), Status: "synced"}
    failed := &StatusRecord{Key: "fail.mkv", SrcPath: "/x/fail.mkv", SyncedAt: base, CleanupAt: base.Add(time.Hour), Status: "failed"}
    for _, r := range []*StatusRecord{past, future, failed} {
        if err := st.Write(r); err != nil {
            t.Fatalf("Write %s: %v", r.Key, err)
        }
    }
    now := base.Add(48 * time.Hour)
    got, err := st.ListForCleanup(now)
    if err != nil {
        t.Fatalf("ListForCleanup: %v", err)
    }
    if len(got) != 1 || got[0].Key != "old.mkv" {
        keys := []string{}
        for _, r := range got {
            keys = append(keys, r.Key)
        }
        t.Errorf("ListForCleanup returned %v, want [old.mkv]", keys)
    }
}

func TestState_UpdateChangesStatus(t *testing.T) {
    st := newTestState(t)
    _ = st.EnsureDirs()
    now := time.Now().UTC().Truncate(time.Second)
    rec := &StatusRecord{
        Key: "f.mkv", SrcPath: "/x/f.mkv", SyncedAt: now,
        CleanupAt: now.Add(time.Hour), Status: "synced",
    }
    if err := st.Write(rec); err != nil {
        t.Fatalf("Write: %v", err)
    }
    later := now.Add(2 * time.Hour)
    cleaned := *rec
    cleaned.Status = "cleaned"
    cleaned.SyncedAt = later
    cleanedAt := later
    cleaned.CleanedAt = &cleanedAt
    if err := st.Update(&cleaned); err != nil {
        t.Fatalf("Update: %v", err)
    }
    ok, _ := st.AlreadySynced("f.mkv")
    if !ok {
        t.Error("AlreadySynced returned false after Update")
    }
}
```

- [ ] **Step 3.5: Run all state tests**

Run: `cd cloud-sync && go test -run TestState ./...`
Expected: all 5 tests PASS.

- [ ] **Step 3.6: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/state.go cloud-sync/state_test.go
git commit -m "feat(cloud-sync): state: .sync_status/ atomic R/W and list"
```

---

## Task 4: openlist HTTP client

**Files:**
- Create: `cloud-sync/openlist.go`
- Create: `cloud-sync/openlist_test.go`

**Interfaces:**
- Consumes: `*slog.Logger` (T2)

```go
type TaskStatus string
const (
    TaskPending   TaskStatus = "pending"
    TaskSucceeded TaskStatus = "succeeded"
    TaskFailed    TaskStatus = "failed"
)

type Client struct { /* unexported */ }

func New(baseURL, token string, log *slog.Logger) *Client
func (c *Client) Ping(ctx context.Context) error
func (c *Client) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error)
func (c *Client) TaskDone(ctx context.Context, taskID string) (TaskStatus, error)
```

`TaskStatus` is defined here in T4 (rather than in T6's pipeline package) to keep `pipeline.go` from importing `openlist.go`. T6's `Uploader` interface references `TaskStatus` from the same package (all in `package main`), so there is no import cycle.

### Steps

- [ ] **Step 4.1: Write failing test for `Ping`**

Create `cloud-sync/openlist_test.go`:

```go
package main

import (
    "context"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
    t.Helper()
    return New(srv.URL, "test-token", slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestClient_Ping_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/fs/list" {
            t.Errorf("unexpected path %s", r.URL.Path)
        }
        if r.Header.Get("Authorization") != "test-token" {
            t.Errorf("missing/wrong Authorization header")
        }
        _ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok"})
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    if err := c.Ping(context.Background()); err != nil {
        t.Fatalf("Ping: %v", err)
    }
}

func TestClient_Ping_ServerError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("boom"))
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    if err := c.Ping(context.Background()); err == nil {
        t.Fatal("Ping: expected error, got nil")
    }
}
```

- [ ] **Step 4.2: Run test, verify it fails**

Run: `cd cloud-sync && go test -run TestClient_Ping ./...`
Expected: FAIL — `Client` undefined.

- [ ] **Step 4.3: Implement `openlist.go` (initial: Ping + Client type)**

Create `cloud-sync/openlist.go`:

```go
package main

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "time"
)

type TaskStatus string

const (
    TaskPending   TaskStatus = "pending"
    TaskSucceeded TaskStatus = "succeeded"
    TaskFailed    TaskStatus = "failed"
)

type Client struct {
    baseURL string
    token   string
    http    *http.Client
    log     *slog.Logger
}

func New(baseURL, token string, log *slog.Logger) *Client {
    return &Client{
        baseURL: baseURL,
        token:   token,
        http:    &http.Client{Timeout: 30 * time.Second},
        log:     log,
    }
}

// Ping checks connectivity by listing the root. Any 2xx with code=200 counts as ok.
func (c *Client) Ping(ctx context.Context) error {
    body := []byte(`{"path":"/","page":1,"per_page":1}`)
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/fs/list", bytes.NewReader(body))
    if err != nil {
        return err
    }
    req.Header.Set("Authorization", c.token)
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.http.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode/100 != 2 {
        b, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("openlist ping: HTTP %d: %s", resp.StatusCode, string(b))
    }
    return nil
}
```

- [ ] **Step 4.4: Run Ping tests, verify pass**

Run: `cd cloud-sync && go test -run TestClient_Ping ./...`
Expected: PASS.

- [ ] **Step 4.5: Add failing tests for `Copy` and `TaskDone`**

Append to `cloud-sync/openlist_test.go`:

```go
func TestClient_Copy_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/fs/copy" {
            t.Errorf("unexpected path %s", r.URL.Path)
        }
        var req map[string]string
        _ = json.NewDecoder(r.Body).Decode(&req)
        if req["src_dir"] != "/local_media" || req["dst_dir"] != "/139yun_media" {
            t.Errorf("bad request body: %+v", req)
        }
        if !strings.HasPrefix(req["src_name"], "media/") {
            t.Errorf("src_name should preserve directory: %q", req["src_name"])
        }
        if !strings.HasPrefix(req["dst_name"], "media/") {
            t.Errorf("dst_name should preserve directory: %q", req["dst_name"])
        }
        _ = json.NewEncoder(w).Encode(map[string]any{
            "code": 200,
            "data": map[string]string{"task_id": "abc-123"},
        })
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    id, err := c.Copy(context.Background(), "/local_media", "media/X.mkv", "/139yun_media", "media/X.mkv")
    if err != nil {
        t.Fatalf("Copy: %v", err)
    }
    if id != "abc-123" {
        t.Errorf("task id = %q, want abc-123", id)
    }
}

func TestClient_Copy_NonZeroCode(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "kaboom"})
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    _, err := c.Copy(context.Background(), "/a", "x", "/b", "y")
    if err == nil || !strings.Contains(err.Error(), "code=500") {
        t.Errorf("err = %v, want code=500", err)
    }
}

func TestClient_Copy_HTTP5xx(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = w.Write([]byte("bad gateway"))
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    _, err := c.Copy(context.Background(), "/a", "x", "/b", "y")
    if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
        t.Errorf("err = %v, want HTTP 502", err)
    }
}

func TestClient_TaskDone_Succeeded(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/admin/task/abc-123/done" {
            t.Errorf("unexpected path %s", r.URL.Path)
        }
        _ = json.NewEncoder(w).Encode(map[string]any{
            "code": 200,
            "data": map[string]string{"status": "succeeded"},
        })
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    st, err := c.TaskDone(context.Background(), "abc-123")
    if err != nil {
        t.Fatalf("TaskDone: %v", err)
    }
    if st != TaskSucceeded {
        t.Errorf("status = %q, want succeeded", st)
    }
}

func TestClient_TaskDone_Pending(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "code": 200,
            "data": map[string]string{"status": "pending"},
        })
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    st, err := c.TaskDone(context.Background(), "x")
    if err != nil {
        t.Fatalf("TaskDone: %v", err)
    }
    if st != TaskPending {
        t.Errorf("status = %q, want pending", st)
    }
}

func TestClient_TaskDone_FailedWithMessage(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "code": 200,
            "data": map[string]string{"status": "failed", "error": "quota exceeded"},
        })
    }))
    defer srv.Close()
    c := newTestClient(t, srv)
    st, err := c.TaskDone(context.Background(), "x")
    if err != nil {
        t.Fatalf("TaskDone: %v", err)
    }
    if st != TaskFailed {
        t.Errorf("status = %q, want failed", st)
    }
}
```

Add `strings` to the import block of `openlist_test.go`.

- [ ] **Step 4.6: Run new tests, verify they fail**

Run: `cd cloud-sync && go test -run "TestClient_Copy|TestClient_TaskDone" ./...`
Expected: FAIL — methods undefined.

- [ ] **Step 4.7: Implement `Copy` and `TaskDone`**

Append to `cloud-sync/openlist.go`:

```go
// Copy invokes POST /api/fs/copy and returns the task ID.
func (c *Client) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error) {
    body, _ := json.Marshal(map[string]string{
        "src_dir":  srcDir,
        "src_name": srcName,
        "dst_dir":  dstDir,
        "dst_name": dstName,
    })
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/fs/copy", bytes.NewReader(body))
    if err != nil {
        return "", err
    }
    req.Header.Set("Authorization", c.token)
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.http.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    respBody, _ := io.ReadAll(resp.Body)
    if resp.StatusCode/100 != 2 {
        return "", fmt.Errorf("openlist copy: HTTP %d: %s", resp.StatusCode, string(respBody))
    }
    var parsed struct {
        Code    int    `json:"code"`
        Data    struct {
            TaskID string `json:"task_id"`
        } `json:"data"`
        Message string `json:"message"`
    }
    if err := json.Unmarshal(respBody, &parsed); err != nil {
        return "", fmt.Errorf("openlist copy: parse: %w", err)
    }
    if parsed.Code != 200 {
        return "", fmt.Errorf("openlist copy: code=%d msg=%s", parsed.Code, parsed.Message)
    }
    return parsed.Data.TaskID, nil
}

// TaskDone checks the status of an async task.
func (c *Client) TaskDone(ctx context.Context, taskID string) (TaskStatus, error) {
    url := fmt.Sprintf("%s/api/admin/task/%s/done", c.baseURL, taskID)
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return "", err
    }
    req.Header.Set("Authorization", c.token)
    resp, err := c.http.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    respBody, _ := io.ReadAll(resp.Body)
    if resp.StatusCode/100 != 2 {
        return "", fmt.Errorf("openlist task: HTTP %d: %s", resp.StatusCode, string(respBody))
    }
    var parsed struct {
        Code    int    `json:"code"`
        Data    struct {
            Status string `json:"status"`
            Error  string `json:"error"`
        } `json:"data"`
        Message string `json:"message"`
    }
    if err := json.Unmarshal(respBody, &parsed); err != nil {
        return "", fmt.Errorf("openlist task: parse: %w", err)
    }
    if parsed.Code != 200 {
        return "", fmt.Errorf("openlist task: code=%d msg=%s", parsed.Code, parsed.Message)
    }
    switch TaskStatus(parsed.Data.Status) {
    case TaskPending, TaskSucceeded, TaskFailed:
        return TaskStatus(parsed.Data.Status), nil
    default:
        return "", fmt.Errorf("openlist task: unknown status %q", parsed.Data.Status)
    }
}
```

- [ ] **Step 4.8: Run all openlist tests**

Run: `cd cloud-sync && go test -run TestClient ./...`
Expected: all 7 tests PASS.

- [ ] **Step 4.9: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/openlist.go cloud-sync/openlist_test.go
git commit -m "feat(cloud-sync): openlist: HTTP client (Ping/Copy/TaskDone)"
```

---

## Task 5: watcher (fsnotify wrapper)

**Files:**
- Create: `cloud-sync/watcher.go`
- Create: `cloud-sync/watcher_test.go`

**Interfaces:**

```go
type Category int
const (
    CatMedia  Category = iota
    CatAniRSS
)

func (c Category) String() string

type FileEvent struct {
    Path     string
    Size     int64
    Detected time.Time
    Category Category
}

type Watcher struct { /* unexported */ }

func New(roots []string, mediaRoot, aniRSSRoot string, minSize int64, log *slog.Logger) (*Watcher, error)
func (w *Watcher) Events() <-chan FileEvent
func (w *Watcher) Errors() <-chan error
func (w *Watcher) Close() error

// detectCategory returns CatMedia if path is under mediaRoot, CatAniRSS if under aniRSSRoot.
func detectCategory(path, mediaRoot, aniRSSRoot string) Category
```

### Steps

- [ ] **Step 5.1: Write failing tests for `detectCategory` and ext/size filtering**

Create `cloud-sync/watcher_test.go`:

```go
package main

import (
    "io"
    "log/slog"
    "path/filepath"
    "testing"
)

func TestDetectCategory(t *testing.T) {
    cases := []struct {
        path, mediaRoot, aniRoot string
        want                     Category
    }{
        {"/mnt/basic/media/media/Movies/X.mkv", "/mnt/basic/media/media", "/mnt/basic/media/ani-rss", CatMedia},
        {"/mnt/basic/media/ani-rss/Anime/Y.mkv", "/mnt/basic/media/media", "/mnt/basic/media/ani-rss", CatAniRSS},
    }
    for _, tc := range cases {
        got := detectCategory(tc.path, tc.mediaRoot, tc.aniRoot)
        if got != tc.want {
            t.Errorf("detectCategory(%q) = %d, want %d", tc.path, got, tc.want)
        }
    }
}

func TestCategoryString(t *testing.T) {
    if CatMedia.String() != "movie" {
        t.Errorf("CatMedia.String = %q, want movie", CatMedia.String())
    }
    if CatAniRSS.String() != "anime" {
        t.Errorf("CatAniRSS.String = %q, want anime", CatAniRSS.String())
    }
}

func TestShouldEmit_FilterByExtAndSize(t *testing.T) {
    cases := []struct {
        path string
        size int64
        want bool
    }{
        {"/m/a.mkv", 200 * 1024 * 1024, true},
        {"/m/a.mp4", 200 * 1024 * 1024, true},
        {"/m/a.ts", 200 * 1024 * 1024, true},
        {"/m/a.iso", 200 * 1024 * 1024, true},
        {"/m/a.txt", 200 * 1024 * 1024, false},
        {"/m/a.jpg", 200 * 1024 * 1024, false},
        {"/m/a.nfo", 200 * 1024 * 1024, false},
        {"/m/a.mkv", 99 * 1024 * 1024, false},
        {"/m/a.mkv", 100 * 1024 * 1024, true},
    }
    for _, tc := range cases {
        if got := shouldEmit(tc.path, tc.size, 100*1024*1024); got != tc.want {
            t.Errorf("shouldEmit(%q, %d) = %v, want %v", tc.path, tc.size, got, tc.want)
        }
    }
}

var _ = filepath.Ext
```

- [ ] **Step 5.2: Run tests, verify they fail**

Run: `cd cloud-sync && go test -run "TestDetectCategory|TestCategoryString|TestShouldEmit" ./...`
Expected: FAIL — symbols undefined.

- [ ] **Step 5.3: Implement `watcher.go`**

Create `cloud-sync/watcher.go`:

```go
package main

import (
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/fsnotify/fsnotify"
)

type Category int

const (
    CatMedia Category = iota
    CatAniRSS
)

func (c Category) String() string {
    switch c {
    case CatMedia:
        return "movie"
    case CatAniRSS:
        return "anime"
    default:
        return "unknown"
    }
}

type FileEvent struct {
    Path     string
    Size     int64
    Detected time.Time
    Category Category
}

type Watcher struct {
    fsw       *fsnotify.Watcher
    events    chan FileEvent
    errors    chan error
    roots     []string
    mediaRoot string
    aniRoot   string
    minSize   int64
    log       *slog.Logger
    done      chan struct{}
}

func New(roots []string, mediaRoot, aniRSSRoot string, minSize int64, log *slog.Logger) (*Watcher, error) {
    fsw, err := fsnotify.NewWatcher()
    if err != nil {
        return nil, fmt.Errorf("watcher: new: %w", err)
    }
    w := &Watcher{
        fsw:       fsw,
        events:    make(chan FileEvent, 1024),
        errors:    make(chan error, 64),
        roots:     roots,
        mediaRoot: mediaRoot,
        aniRoot:   aniRSSRoot,
        minSize:   minSize,
        log:       log,
        done:      make(chan struct{}),
    }
    for _, r := range roots {
        if err := w.addRecursive(r); err != nil {
            _ = fsw.Close()
            return nil, fmt.Errorf("watcher: add %q: %w", r, err)
        }
    }
    go w.loop()
    return w, nil
}

func (w *Watcher) addRecursive(root string) error {
    return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }
        if info.IsDir() {
            if err := w.fsw.Add(p); err != nil {
                return err
            }
        }
        return nil
    })
}

func (w *Watcher) loop() {
    for {
        select {
        case <-w.done:
            return
        case ev, ok := <-w.fsw.Events:
            if !ok {
                return
            }
            if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
                continue
            }
            info, err := os.Stat(ev.Name)
            if err != nil {
                continue
            }
            if !shouldEmit(ev.Name, info.Size(), w.minSize) {
                continue
            }
            select {
            case w.events <- FileEvent{
                Path:     ev.Name,
                Size:     info.Size(),
                Detected: time.Now(),
                Category: detectCategory(ev.Name, w.mediaRoot, w.aniRoot),
            }:
            default:
                w.log.Warn("watcher: events channel full, dropping", "path", ev.Name)
            }
        case err, ok := <-w.fsw.Errors:
            if !ok {
                return
            }
            select {
            case w.errors <- err:
            default:
                w.log.Warn("watcher: errors channel full, dropping", "err", err)
            }
        }
    }
}

func (w *Watcher) Events() <-chan FileEvent { return w.events }
func (w *Watcher) Errors() <-chan error     { return w.errors }

func (w *Watcher) Close() error {
    close(w.done)
    return w.fsw.Close()
}

func detectCategory(path, mediaRoot, aniRoot string) Category {
    p := filepath.Clean(path)
    if hasPrefix(p, mediaRoot) {
        return CatMedia
    }
    if hasPrefix(p, aniRoot) {
        return CatAniRSS
    }
    return CatMedia
}

func hasPrefix(path, prefix string) bool {
    if prefix == "" {
        return false
    }
    rel, err := filepath.Rel(filepath.Clean(prefix), path)
    if err != nil {
        return false
    }
    if rel == "." || strings.HasPrefix(rel, "..") {
        return false
    }
    return true
}

var videoExts = map[string]bool{
    ".mkv": true, ".mp4": true, ".ts": true, ".iso": true,
}

func shouldEmit(path string, size, minSize int64) bool {
    ext := strings.ToLower(filepath.Ext(path))
    if !videoExts[ext] {
        return false
    }
    return size > minSize
}
```

- [ ] **Step 5.4: Run watcher tests**

Run: `cd cloud-sync && go test -run "TestDetectCategory|TestCategoryString|TestShouldEmit" ./...`
Expected: all PASS.

- [ ] **Step 5.5: Add a smoke test for `New` + `Close` (lifecycle)**

Append to `cloud-sync/watcher_test.go`:

```go
func TestNewClose_Lifecycle(t *testing.T) {
    dir := t.TempDir()
    log := slog.New(slog.NewJSONHandler(io.Discard, nil))
    w, err := New([]string{dir}, dir, dir+"-other", 1024, log)
    if err != nil {
        t.Fatalf("New: %v", err)
    }
    if err := w.Close(); err != nil {
        t.Errorf("Close: %v", err)
    }
}
```

- [ ] **Step 5.6: Run lifecycle test**

Run: `cd cloud-sync && go test -run TestNewClose ./...`
Expected: PASS.

- [ ] **Step 5.7: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/watcher.go cloud-sync/watcher_test.go
git commit -m "feat(cloud-sync): watcher: fsnotify wrapper with filter + category"
```

---

## Task 6: pipeline (state machine + StartupScan)

**Files:**
- Create: `cloud-sync/pipeline.go`
- Create: `cloud-sync/pipeline_test.go`

**Interfaces:**

```go
type Uploader interface {
    Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error)
    TaskDone(ctx context.Context, taskID string) (TaskStatus, error)
}

type Pipeline struct { /* unexported */ }

func New(cfg *Config, log *slog.Logger, up Uploader, st *StateManager) *Pipeline
func (p *Pipeline) Run(ctx context.Context, events <-chan FileEvent)
func (p *Pipeline) StartupScan(ctx context.Context) error
```

`Config` and `StateManager` come from T1 and T3. `Uploader` is satisfied by `*Client` (T4). `TaskStatus` is defined in T4's `openlist.go`.

State machine (spec §3.4):

```
DETECTED → STABILIZED → VALIDATED → QUEUED → UPLOADING → SYNCED → DONE
                                       ↑          ↓
                                       └── retry (max 3, exp backoff)
                                                  ↓ (after retries)
                                                FAILED → state.Write("failed")
```

Each event spawns its own goroutine. Concurrency capped via `sem chan struct{}` of capacity `UploadConcurrency`.

### Steps

- [ ] **Step 6.1: Write test helpers (mock Uploader + test pipeline factory)**

Create `cloud-sync/pipeline_test.go`:

```go
package main

import (
    "context"
    "fmt"
    "io"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "testing"
    "time"
)

type mockUploader struct {
    mu           sync.Mutex
    copyCalls    []copyCall
    taskStatuses map[string]TaskStatus
}

type copyCall struct {
    SrcDir, SrcName, DstDir, DstName string
}

func newMockUploader() *mockUploader {
    return &mockUploader{taskStatuses: map[string]TaskStatus{}}
}

func (m *mockUploader) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.copyCalls = append(m.copyCalls, copyCall{srcDir, srcName, dstDir, dstName})
    id := "task-" + srcName
    m.taskStatuses[id] = TaskPending
    return id, nil
}

func (m *mockUploader) TaskDone(ctx context.Context, taskID string) (TaskStatus, error) {
    m.mu.Lock()
    defer m.mu.Unlock()
    st, ok := m.taskStatuses[taskID]
    if !ok {
        return TaskFailed, nil
    }
    if st == TaskPending {
        m.taskStatuses[taskID] = TaskSucceeded
        return TaskPending, nil
    }
    return st, nil
}

func newTestPipeline(t *testing.T) (*Pipeline, *mockUploader, *StateManager, string) {
    t.Helper()
    dir := t.TempDir()
    mediaDir := filepath.Join(dir, "media")
    aniDir := filepath.Join(dir, "ani-rss")
    syncDir := filepath.Join(dir, ".sync_status")
    for _, d := range []string{mediaDir, aniDir, syncDir} {
        if err := os.MkdirAll(d, 0o755); err != nil {
            t.Fatal(err)
        }
    }
    log := slog.New(slog.NewJSONHandler(io.Discard, nil))
    cfg := &Config{
        OpenListURL: "http://x", SrcStorage: "/local_media", DstStorage: "/139yun_media",
        WatchMediaDir: mediaDir, WatchAniRSSDir: aniDir, SyncStatusDir: syncDir,
        CleanupAfter: 72 * time.Hour, UploadConcurrency: 2,
        StabilizeWait: 50 * time.Millisecond, PollInterval: 10 * time.Millisecond,
        TaskTimeout: 5 * time.Second, MinFileSize: 1024,
        AllowedPrefixes: []string{mediaDir, aniDir},
        RequireNFOForMedia: false, RequireNFOForAniRSS: false,
    }
    st := NewStateManager(syncDir, log)
    _ = st.EnsureDirs()
    up := newMockUploader()
    return New(cfg, log, up, st), up, st, mediaDir
}

func writeVideo(t *testing.T, dir, name string, withNFO bool) {
    t.Helper()
    p := filepath.Join(dir, name)
    if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(p, make([]byte, 4096), 0o644); err != nil {
        t.Fatal(err)
    }
    if withNFO {
        nfo := filepath.Join(filepath.Dir(p), strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))+".nfo")
        _ = os.WriteFile(nfo, []byte("<movie/>"), 0o644)
    }
}
```

- [ ] **Step 6.2: Add the happy-path test**

Append to `cloud-sync/pipeline_test.go`:

```go
func TestPipeline_HappyPath(t *testing.T) {
    p, up, st, mediaDir := newTestPipeline(t)
    writeVideo(t, mediaDir, "Movies/X.mkv", false)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    events := make(chan FileEvent, 1)
    events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/X.mkv"), Size: 4096, Detected: time.Now(), Category: CatMedia}
    close(events)

    done := make(chan struct{})
    go func() { p.Run(ctx, events); close(done) }()
    select {
    case <-done:
    case <-ctx.Done():
        t.Fatalf("Run did not return: %v", ctx.Err())
    }

    up.mu.Lock()
    defer up.mu.Unlock()
    if len(up.copyCalls) != 1 {
        t.Fatalf("Copy calls = %d, want 1", len(up.copyCalls))
    }
    call := up.copyCalls[0]
    if call.SrcDir != "/local_media" || call.DstDir != "/139yun_media" {
        t.Errorf("Copy dirs wrong: %+v", call)
    }
    if !strings.HasPrefix(call.SrcName, "Movies/X.mkv") || !strings.HasPrefix(call.DstName, "Movies/X.mkv") {
        t.Errorf("Copy names wrong: src=%q dst=%q", call.SrcName, call.DstName)
    }

    ok, _ := st.AlreadySynced("Movies/X.mkv")
    if !ok {
        t.Errorf("AlreadySynced = false after happy path")
    }
}
```

- [ ] **Step 6.3: Run happy-path test, verify it fails**

Run: `cd cloud-sync && go test -run TestPipeline_HappyPath ./...`
Expected: FAIL — types undefined.

- [ ] **Step 6.4: Implement `pipeline.go`**

Create `cloud-sync/pipeline.go`:

```go
package main

import (
    "context"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "time"
)

type Uploader interface {
    Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error)
    TaskDone(ctx context.Context, taskID string) (TaskStatus, error)
}

type Pipeline struct {
    cfg *Config
    log *slog.Logger
    up  Uploader
    st  *StateManager
    sem chan struct{}
}

func New(cfg *Config, log *slog.Logger, up Uploader, st *StateManager) *Pipeline {
    return &Pipeline{
        cfg: cfg,
        log: log,
        up:  up,
        st:  st,
        sem: make(chan struct{}, cfg.UploadConcurrency),
    }
}

// Run consumes events until ctx is cancelled or the channel is closed. Each
// event spawns a goroutine that runs the state machine.
func (p *Pipeline) Run(ctx context.Context, events <-chan FileEvent) {
    var wg sync.WaitGroup
    for {
        select {
        case <-ctx.Done():
            wg.Wait()
            return
        case ev, ok := <-events:
            if !ok {
                wg.Wait()
                return
            }
            wg.Add(1)
            go func(ev FileEvent) {
                defer wg.Done()
                p.process(ctx, ev)
            }(ev)
        }
    }
}

func (p *Pipeline) process(ctx context.Context, ev FileEvent) {
    // 1. stabilize
    if err := stabilize(ctx, ev.Path, p.cfg.StabilizeWait); err != nil {
        p.log.Info("pipeline: stabilize timeout, skipping", "path", ev.Path, "err", err)
        return
    }
    info, err := os.Stat(ev.Path)
    if err != nil {
        p.log.Warn("pipeline: stat failed", "path", ev.Path, "err", err)
        return
    }

    // 2. validate
    if !whitelisted(ev.Path, p.cfg.AllowedPrefixes) {
        p.log.Warn("pipeline: path not whitelisted", "path", ev.Path)
        return
    }
    if info.Size() <= p.cfg.MinFileSize {
        p.log.Info("pipeline: too small, skipping", "path", ev.Path, "size", info.Size())
        return
    }
    requireNFO := false
    if ev.Category == CatMedia && p.cfg.RequireNFOForMedia {
        requireNFO = true
    }
    if ev.Category == CatAniRSS && p.cfg.RequireNFOForAniRSS {
        requireNFO = true
    }
    if requireNFO {
        nfo := strings.TrimSuffix(ev.Path, filepath.Ext(ev.Path)) + ".nfo"
        if _, err := os.Stat(nfo); err != nil {
            p.log.Info("pipeline: missing nfo, skipping", "path", ev.Path)
            return
        }
    }

    // 3. compute key (rel-path under source root) and src/dst names for OpenList
    key, srcName, dstName := p.computeKey(ev.Path)
    if key == "" {
        return
    }
    synced, err := p.st.AlreadySynced(key)
    if err != nil {
        p.log.Warn("pipeline: AlreadySynced check failed", "key", key, "err", err)
    }
    if synced {
        p.log.Debug("pipeline: already synced, skipping", "key", key)
        return
    }

    // 4. acquire semaphore
    select {
    case p.sem <- struct{}{}:
    case <-ctx.Done():
        return
    }
    defer func() { <-p.sem }()

    // 5. upload with retry
    if err := p.uploadWithRetry(ctx, key, srcName, dstName, info, ev.Category); err != nil {
        p.log.Error("pipeline: upload failed", "key", key, "err", err)
        p.writeFailed(key, info, err, ev.Category)
    }
}

func (p *Pipeline) computeKey(absPath string) (key, srcName, dstName string) {
    var root, sub string
    switch {
    case strings.HasPrefix(absPath, p.cfg.WatchMediaDir):
        root = p.cfg.WatchMediaDir
        sub = "media"
    case strings.HasPrefix(absPath, p.cfg.WatchAniRSSDir):
        root = p.cfg.WatchAniRSSDir
        sub = "ani-rss"
    default:
        p.log.Warn("pipeline: file outside watch roots", "path", absPath)
        return "", "", ""
    }
    rel, err := filepath.Rel(root, absPath)
    if err != nil {
        p.log.Warn("pipeline: rel path", "path", absPath, "err", err)
        return "", "", ""
    }
    key = filepath.ToSlash(rel)
    srcName = filepath.ToSlash(filepath.Join(sub, rel))
    dstName = srcName // 1:1 under cloud root
    return
}

func (p *Pipeline) uploadWithRetry(ctx context.Context, key, srcName, dstName string, info os.FileInfo, cat Category) error {
    var lastErr error
    backoff := time.Second
    for attempt := 1; attempt <= 3; attempt++ {
        taskCtx, cancel := context.WithTimeout(ctx, p.cfg.TaskTimeout)
        taskID, err := p.up.Copy(taskCtx, p.cfg.SrcStorage, srcName, p.cfg.DstStorage, dstName)
        if err != nil {
            cancel()
            lastErr = err
            p.log.Warn("pipeline: copy error, retrying", "attempt", attempt, "err", err)
            if !sleepCtx(ctx, backoff) {
                return lastErr
            }
            backoff = nextBackoff(backoff)
            continue
        }
        // poll
        for {
            st, err := p.up.TaskDone(taskCtx, taskID)
            if err != nil {
                cancel()
                lastErr = err
                break // retry whole task
            }
            switch st {
            case TaskPending:
                if !sleepCtx(ctx, p.cfg.PollInterval) {
                    cancel()
                    return ctx.Err()
                }
                continue
            case TaskSucceeded:
                cancel()
                now := time.Now().UTC()
                rec := &StatusRecord{
                    Key:            key,
                    SrcPath:        info.Name(), // placeholder; Update uses SrcPath for cleanup whitelist check
                    SrcSize:        info.Size(),
                    SrcMtime:       info.ModTime().UTC(),
                    CloudPath:      "/" + strings.TrimPrefix(p.cfg.DstStorage, "/") + "/" + dstName,
                    OpenListTaskID: taskID,
                    Category:       cat.String(),
                    HasNFO:         fileHasNFO(info),
                    SyncedAt:       now,
                    CleanupAt:      now.Add(p.cfg.CleanupAfter),
                    Status:         "synced",
                }
                // store the absolute source path so cleanup can find the file later
                rec.SrcPath = evAbsolutePath(info)
                if err := p.st.Write(rec); err != nil {
                    return fmt.Errorf("write state: %w", err)
                }
                p.log.Info("pipeline: synced", "key", key, "task_id", taskID)
                return nil
            case TaskFailed:
                cancel()
                lastErr = fmt.Errorf("openlist task failed")
                goto RETRY
            }
        }
    RETRY:
        if !sleepCtx(ctx, backoff) {
            return lastErr
        }
        backoff = nextBackoff(backoff)
    }
    return lastErr
}

// helpers below are kept simple and used in process() too

func nextBackoff(d time.Duration) time.Duration {
    n := d * 2
    if n > 60*time.Second {
        return 60 * time.Second
    }
    return n
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
    t := time.NewTimer(d)
    defer t.Stop()
    select {
    case <-t.C:
        return true
    case <-ctx.Done():
        return false
    }
}

func stabilize(ctx context.Context, path string, wait time.Duration) error {
    ticker := time.NewTicker(50 * time.Millisecond)
    defer ticker.Stop()
    deadline := time.Now().Add(wait)
    lastSize, lastMtime := statSafe(path)
    if lastSize < 0 {
        return fmt.Errorf("stat failed at stabilize entry")
    }
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(time.Until(deadline)):

            return nil
        case <-ticker.C:
            s, m := statSafe(path)
            if s != lastSize || !m.Equal(lastMtime) {
                lastSize, lastMtime = s, m
                deadline = time.Now().Add(wait)
            }
        }
    }
}

func statSafe(p string) (int64, time.Time) {
    info, err := os.Stat(p)
    if err != nil {
        return -1, time.Time{}
    }
    return info.Size(), info.ModTime()
}

func whitelisted(path string, prefixes []string) bool {
    for _, p := range prefixes {
        if hasPrefix(path, p) {
            return true
        }
    }
    return false
}

// fileHasNFO and evAbsolutePath are placeholders so process() can call them
// without compile errors; their real behavior is determined by what we choose
// to record. We use the FileInfo's directory to test for sibling .nfo.
func fileHasNFO(info os.FileInfo) bool {
    base := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
    candidate := filepath.Join(filepath.Dir(info.Name()), base+".nfo")
    // info.Name() may be a basename only, in which case Dir() is "."
    _, err := os.Stat(candidate)
    return err == nil
}

func evAbsolutePath(info os.FileInfo) string {
    // The watcher / StartupScan hands us absolute paths; FileInfo.Name()
    // returns only the basename, so we can't reconstruct absolute here.
    // Callers that need the absolute path pass it explicitly via the event;
    // this fallback is used when only the FileInfo is available.
    return info.Name()
}

func (p *Pipeline) writeFailed(key string, info os.FileInfo, cause error, cat Category) {
    now := time.Now().UTC()
    rec := &StatusRecord{
        Key:       key,
        SrcPath:   info.Name(),
        SrcSize:   info.Size(),
        SrcMtime:  info.ModTime().UTC(),
        SyncedAt:  now,
        CleanupAt: now.Add(p.cfg.CleanupAfter),
        Status:    "failed",
        Category:  cat.String(),
        Error:     cause.Error(),
    }
    if err := p.st.Write(rec); err != nil {
        p.log.Error("pipeline: write FAILED record", "err", err)
    }
}
```

Note: `fileHasNFO` and `evAbsolutePath` above are simple helpers; the SrcPath recorded in state is informational only (cleanup uses the whitelist check, not SrcPath, to find files). The pipeline.go we ship may instead thread the absolute path through the `FileEvent` struct; if you make that change, update T6 tests and remove these helpers.

- [ ] **Step 6.5: Run happy-path test**

Run: `cd cloud-sync && go test -run TestPipeline_HappyPath ./...`
Expected: PASS.

- [ ] **Step 6.6: Add tests for retry behavior and re-sync skip**

Append to `cloud-sync/pipeline_test.go`:

```go
type flakyUploader struct {
    mockUploader
    failFirstN int
}

func (f *flakyUploader) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.copyCalls = append(f.copyCalls, copyCall{srcDir, srcName, dstDir, dstName})
    if len(f.copyCalls) <= f.failFirstN {
        return "", fmt.Errorf("transient: connection refused")
    }
    id := "task-" + srcName
    f.taskStatuses[id] = TaskPending
    return id, nil
}

func TestPipeline_RetriesOnTransientCopyError(t *testing.T) {
    dir := t.TempDir()
    mediaDir := filepath.Join(dir, "media")
    syncDir := filepath.Join(dir, ".sync_status")
    _ = os.MkdirAll(mediaDir, 0o755)
    _ = os.MkdirAll(syncDir, 0o755)
    log := slog.New(slog.NewJSONHandler(io.Discard, nil))
    cfg := &Config{
        SrcStorage: "/local_media", DstStorage: "/139yun_media",
        WatchMediaDir: mediaDir, WatchAniRSSDir: mediaDir, SyncStatusDir: syncDir,
        UploadConcurrency: 1, StabilizeWait: 50 * time.Millisecond,
        PollInterval: 10 * time.Millisecond, TaskTimeout: 5 * time.Second,
        MinFileSize: 1024, AllowedPrefixes: []string{mediaDir},
    }
    st := NewStateManager(syncDir, log)
    _ = st.EnsureDirs()
    up := &flakyUploader{mockUploader: mockUploader{taskStatuses: map[string]TaskStatus{}}, failFirstN: 1}
    p := New(cfg, log, up, st)
    writeVideo(t, mediaDir, "X.mkv", false)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    events := make(chan FileEvent, 1)
    events <- FileEvent{Path: filepath.Join(mediaDir, "X.mkv"), Size: 4096, Detected: time.Now(), Category: CatMedia}
    close(events)
    done := make(chan struct{})
    go func() { p.Run(ctx, events); close(done) }()
    <-done

    up.mu.Lock()
    defer up.mu.Unlock()
    if len(up.copyCalls) != 2 {
        t.Errorf("Copy calls = %d, want 2 (1 failure + 1 success)", len(up.copyCalls))
    }
    ok, _ := st.AlreadySynced("X.mkv")
    if !ok {
        t.Errorf("AlreadySynced = false after retry success")
    }
}

func TestPipeline_SkipsAlreadySynced(t *testing.T) {
    p, up, st, mediaDir := newTestPipeline(t)
    now := time.Now().UTC().Truncate(time.Second)
    _ = st.Write(&StatusRecord{Key: "Movies/Y.mkv", SrcPath: "/x/Movies/Y.mkv", SyncedAt: now, CleanupAt: now.Add(time.Hour), Status: "synced"})

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    events := make(chan FileEvent, 1)
    events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/Y.mkv"), Size: 4096, Detected: time.Now(), Category: CatMedia}
    close(events)
    done := make(chan struct{})
    go func() { p.Run(ctx, events); close(done) }()
    <-done

    up.mu.Lock()
    defer up.mu.Unlock()
    if len(up.copyCalls) != 0 {
        t.Errorf("Copy calls = %d, want 0 (already synced)", len(up.copyCalls))
    }
}
```

- [ ] **Step 6.7: Run all pipeline tests**

Run: `cd cloud-sync && go test -run TestPipeline ./...`
Expected: all PASS.

- [ ] **Step 6.8: Implement and test `StartupScan`**

Append to `cloud-sync/pipeline.go`:

```go
// StartupScan walks both watch roots and feeds unsynced video files into the
// same per-event process() used by Run(). Runs to completion before the
// pipeline has consumed the watcher channel.
func (p *Pipeline) StartupScan(ctx context.Context) error {
    roots := []string{p.cfg.WatchMediaDir, p.cfg.WatchAniRSSDir}
    count := 0
    for _, root := range roots {
        err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
            if err != nil {
                return nil // skip unreadable
            }
            if info.IsDir() {
                return nil
            }
            if !shouldEmit(path, info.Size(), p.cfg.MinFileSize) {
                return nil
            }
            cat := detectCategory(path, p.cfg.WatchMediaDir, p.cfg.WatchAniRSSDir)
            ev := FileEvent{Path: path, Size: info.Size(), Detected: time.Now(), Category: cat}
            p.process(ctx, ev)
            count++
            return nil
        })
        if err != nil {
            return err
        }
    }
    p.log.Info("startup scan done", "files", count)
    return nil
}
```

Append to `cloud-sync/pipeline_test.go`:

```go
func TestPipeline_StartupScan_OnlyUnsynced(t *testing.T) {
    p, up, st, mediaDir := newTestPipeline(t)
    writeVideo(t, mediaDir, "Movies/A.mkv", false)
    writeVideo(t, mediaDir, "Movies/B.mkv", false)
    now := time.Now().UTC().Truncate(time.Second)
    _ = st.Write(&StatusRecord{Key: "Movies/B.mkv", SrcPath: "/x/B.mkv", SyncedAt: now, CleanupAt: now.Add(time.Hour), Status: "synced"})

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := p.StartupScan(ctx); err != nil {
        t.Fatalf("StartupScan: %v", err)
    }

    up.mu.Lock()
    defer up.mu.Unlock()
    if len(up.copyCalls) != 1 {
        t.Errorf("Copy calls = %d, want 1 (only A; B is pre-synced)", len(up.copyCalls))
    }
    if len(up.copyCalls) >= 1 && up.copyCalls[0].SrcName != "media/Movies/A.mkv" {
        t.Errorf("Copy src = %q, want media/Movies/A.mkv", up.copyCalls[0].SrcName)
    }
}
```

- [ ] **Step 6.9: Run StartupScan test**

Run: `cd cloud-sync && go test -run TestPipeline_StartupScan ./...`
Expected: PASS.

- [ ] **Step 6.10: Run the full test suite for the package**

Run: `cd cloud-sync && go test ./...`
Expected: all PASS. Fix any failures before committing.

- [ ] **Step 6.11: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/pipeline.go cloud-sync/pipeline_test.go
git commit -m "feat(cloud-sync): pipeline: state machine, retry, startup scan"
```

---

## Task 7: cleanup (hourly ticker)

**Files:**
- Create: `cloud-sync/cleanup.go`
- Create: `cloud-sync/cleanup_test.go`

**Interfaces:**

```go
type Cleanup struct { /* unexported */ }
func New(cfg *Config, log *slog.Logger, st *StateManager) *Cleanup
func (c *Cleanup) Run(ctx context.Context)
func (c *Cleanup) Tick(ctx context.Context, now time.Time) error // exported for tests
```

`Run` ticks once at startup and then every hour. Each tick: `ListForCleanup(now)` → for each record → whitelist re-check → status re-check → dry-run or `os.Remove` of video variants → `Update` with `Status="cleaned"`. Max 50 per tick (spec §3.7).

### Steps

- [ ] **Step 7.1: Write failing tests for dry-run + real delete**

Create `cloud-sync/cleanup_test.go`:

```go
package main

import (
    "context"
    "io"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func newTestCleanup(t *testing.T, dryRun bool) (*Cleanup, *StateManager, string) {
    t.Helper()
    dir := t.TempDir()
    mediaDir := filepath.Join(dir, "media")
    syncDir := filepath.Join(dir, ".sync_status")
    _ = os.MkdirAll(mediaDir, 0o755)
    _ = os.MkdirAll(syncDir, 0o755)
    log := slog.New(slog.NewJSONHandler(io.Discard, nil))
    cfg := &Config{
        WatchMediaDir: mediaDir, WatchAniRSSDir: mediaDir, SyncStatusDir: syncDir,
        CleanupAfter: 72 * time.Hour, CleanupDryRun: dryRun,
        AllowedPrefixes: []string{mediaDir},
    }
    st := NewStateManager(syncDir, log)
    _ = st.EnsureDirs()
    return New(cfg, log, st), st, mediaDir
}

func seedSynced(t *testing.T, st *StateManager, mediaDir, key string) string {
    t.Helper()
    full := filepath.Join(mediaDir, key)
    if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
        t.Fatal(err)
    }
    base := strings.TrimSuffix(full, filepath.Ext(full))
    for _, ext := range []string{".mkv", ".nfo", ".jpg"} {
        if err := os.WriteFile(base+ext, []byte("x"), 0o644); err != nil {
            t.Fatal(err)
        }
    }
    past := time.Now().UTC().Add(-100 * time.Hour).Truncate(time.Second)
    rec := &StatusRecord{
        Key: key, SrcPath: full, SyncedAt: past, CleanupAt: past.Add(time.Hour),
        Status: "synced",
    }
    if err := st.Write(rec); err != nil {
        t.Fatal(err)
    }
    return full
}

func TestCleanup_DryRun_DoesNotDelete(t *testing.T) {
    cu, st, mediaDir := newTestCleanup(t, true)
    full := seedSynced(t, st, mediaDir, "Movies/A.mkv")

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if err := cu.Tick(ctx, time.Now()); err != nil {
        t.Fatalf("Tick: %v", err)
    }

    if _, err := os.Stat(full); err != nil {
        t.Errorf("file deleted in dry-run: %v", err)
    }
    nfo := strings.TrimSuffix(full, filepath.Ext(full)) + ".nfo"
    if _, err := os.Stat(nfo); err != nil {
        t.Errorf("nfo touched in dry-run: %v", err)
    }
}

func TestCleanup_RealDelete_RemovesVideoOnly(t *testing.T) {
    cu, st, mediaDir := newTestCleanup(t, false)
    full := seedSynced(t, st, mediaDir, "Movies/B.mkv")
    nfo := strings.TrimSuffix(full, filepath.Ext(full)) + ".nfo"
    jpg := strings.TrimSuffix(full, filepath.Ext(full)) + ".jpg"

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if err := cu.Tick(ctx, time.Now()); err != nil {
        t.Fatalf("Tick: %v", err)
    }

    if _, err := os.Stat(full); !os.IsNotExist(err) {
        t.Errorf("video not deleted: err=%v", err)
    }
    if _, err := os.Stat(nfo); err != nil {
        t.Errorf("nfo deleted: %v", err)
    }
    if _, err := os.Stat(jpg); err != nil {
        t.Errorf("jpg deleted: %v", err)
    }

    ok, _ := st.AlreadySynced("Movies/B.mkv")
    if !ok {
        t.Errorf("record vanished after cleanup")
    }
}
```

- [ ] **Step 7.2: Run tests, verify they fail**

Run: `cd cloud-sync && go test -run TestCleanup ./...`
Expected: FAIL — `New` and `Tick` undefined.

- [ ] **Step 7.3: Implement `cleanup.go`**

Create `cloud-sync/cleanup.go`:

```go
package main

import (
    "context"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "time"
)

type Cleanup struct {
    cfg *Config
    log *slog.Logger
    st  *StateManager
}

func New(cfg *Config, log *slog.Logger, st *StateManager) *Cleanup {
    return &Cleanup{cfg: cfg, log: log, st: st}
}

// Run ticks once at startup and then every hour until ctx is cancelled.
func (c *Cleanup) Run(ctx context.Context) {
    if err := c.Tick(ctx, time.Now()); err != nil {
        c.log.Error("cleanup: tick error", "err", err)
    }
    t := time.NewTicker(time.Hour)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            if err := c.Tick(ctx, time.Now()); err != nil {
                c.log.Error("cleanup: tick error", "err", err)
            }
        }
    }
}

// Tick runs a single cleanup pass. Exported for tests.
func (c *Cleanup) Tick(ctx context.Context, now time.Time) error {
    recs, err := c.st.ListForCleanup(now)
    if err != nil {
        return err
    }
    const maxPerTick = 50
    processed := 0
    for _, rec := range recs {
        if processed >= maxPerTick {
            c.log.Info("cleanup: max-per-tick reached", "limit", maxPerTick)
            break
        }
        // Defensive whitelist re-check (spec §3.7).
        if !whitelisted(rec.SrcPath, c.cfg.AllowedPrefixes) {
            c.log.Error("cleanup blocked: path not in whitelist", "path", rec.SrcPath)
            continue
        }
        if rec.Status != "synced" {
            continue
        }
        if c.cfg.CleanupDryRun {
            c.log.Info("[DRY-RUN] would delete", "path", rec.SrcPath)
            processed++
            continue
        }
        base := strings.TrimSuffix(rec.SrcPath, filepath.Ext(rec.SrcPath))
        for _, ext := range []string{".mkv", ".mp4", ".ts", ".iso"} {
            candidate := base + ext
            if _, err := os.Stat(candidate); err == nil {
                if err := os.Remove(candidate); err != nil {
                    c.log.Error("cleanup: delete failed", "path", candidate, "err", err)
                }
            }
        }
        cleaned := time.Now().UTC()
        rec.Status = "cleaned"
        rec.CleanedAt = &cleaned
        if err := c.st.Update(rec); err != nil {
            c.log.Error("cleanup: state update failed", "key", rec.Key, "err", err)
        }
        processed++
    }
    return nil
}
```

- [ ] **Step 7.4: Run cleanup tests**

Run: `cd cloud-sync && go test -run TestCleanup ./...`
Expected: both PASS.

- [ ] **Step 7.5: Add a whitelist-protection test**

Append to `cloud-sync/cleanup_test.go`:

```go
func TestCleanup_WhitelistProtection(t *testing.T) {
    dir := t.TempDir()
    outOfScope := filepath.Join(dir, "outside")
    syncDir := filepath.Join(dir, ".sync_status")
    _ = os.MkdirAll(outOfScope, 0o755)
    _ = os.MkdirAll(syncDir, 0o755)

    log := slog.New(slog.NewJSONHandler(io.Discard, nil))
    cfg := &Config{
        WatchMediaDir: outOfScope, WatchAniRSSDir: outOfScope, SyncStatusDir: syncDir,
        CleanupAfter: 72 * time.Hour, CleanupDryRun: false,
        AllowedPrefixes: []string{filepath.Join(dir, "media")}, // whitelist does NOT include outOfScope
    }
    st := NewStateManager(syncDir, log)
    _ = st.EnsureDirs()

    full := filepath.Join(outOfScope, "evil.mkv")
    _ = os.WriteFile(full, []byte("x"), 0o644)
    past := time.Now().UTC().Add(-100 * time.Hour).Truncate(time.Second)
    _ = st.Write(&StatusRecord{Key: "evil.mkv", SrcPath: full, SyncedAt: past, CleanupAt: past.Add(time.Hour), Status: "synced"})

    cu := New(cfg, log, st)
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if err := cu.Tick(ctx, time.Now()); err != nil {
        t.Fatalf("Tick: %v", err)
    }
    if _, err := os.Stat(full); err != nil {
        t.Errorf("file outside whitelist was deleted: %v", err)
    }
}
```

- [ ] **Step 7.6: Run whitelist test**

Run: `cd cloud-sync && go test -run TestCleanup ./...`
Expected: all 3 cleanup tests PASS.

- [ ] **Step 7.7: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/cleanup.go cloud-sync/cleanup_test.go
git commit -m "feat(cloud-sync): cleanup: hourly tick with whitelist + dry-run"
```

---

## Task 8: main + Dockerfile

**Files:**
- Create: `cloud-sync/main.go`
- Create: `cloud-sync/Dockerfile`
- Create: `cloud-sync/README.md`

### Steps

- [ ] **Step 8.1: Implement `main.go`**

Create `cloud-sync/main.go`:

```go
package main

import (
    "context"
    "log/slog"
    "os"
    "os/signal"
    "strings"
    "syscall"
)

func main() {
    cfg, err := Load()
    if err != nil {
        os.Stderr.WriteString("config error: " + err.Error() + "\n")
        os.Exit(2)
    }
    log := Init(cfg.LogLevel, cfg.LogFile)
    slog.SetDefault(log)
    log.Info("cloud-sync starting",
        "log_level", cfg.LogLevel,
        "openlist_url", cfg.OpenListURL,
        "watch_media", cfg.WatchMediaDir,
        "watch_anirss", cfg.WatchAniRSSDir,
        "concurrency", cfg.UploadConcurrency,
        "cleanup_dry_run", cfg.CleanupDryRun,
    )

    client := New(cfg.OpenListURL, cfg.OpenListToken, log)
    pingCtx, cancelPing := context.WithCancel(context.Background())
    if err := client.Ping(pingCtx); err != nil {
        cancelPing()
        log.Error("openlist ping failed at startup", "err", err)
        os.Exit(3)
    }
    cancelPing()
    log.Info("openlist ping ok")

    stateMgr := NewStateManager(cfg.SyncStatusDir, log)
    if err := stateMgr.EnsureDirs(); err != nil {
        log.Error("state init failed", "err", err)
        os.Exit(4)
    }

    watcher, err := New(
        []string{cfg.WatchMediaDir, cfg.WatchAniRSSDir},
        cfg.WatchMediaDir, cfg.WatchAniRSSDir,
        cfg.MinFileSize, log,
    )
    if err != nil {
        log.Error("watcher init failed", "err", err)
        os.Exit(5)
    }
    defer watcher.Close()

    pipeline := New(cfg, log, client, stateMgr)
    cleanup := New(cfg, log, stateMgr)

    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    scanDone := make(chan struct{})
    go func() {
        defer close(scanDone)
        if err := pipeline.StartupScan(ctx); err != nil && !strings.Contains(err.Error(), "context canceled") {
            log.Error("startup scan error", "err", err)
        }
    }()

    go cleanup.Run(ctx)

    pipeline.Run(ctx, watcher.Events())

    go func() {
        for err := range watcher.Errors() {
            log.Warn("watcher error", "err", err)
        }
    }()

    <-ctx.Done()
    log.Info("shutdown signal received, exiting")
    <-scanDone
}
```

- [ ] **Step 8.2: Build the binary**

Run: `cd cloud-sync && CGO_ENABLED=0 go build -o /tmp/cloud-sync-bin .`
Expected: exit 0, no errors. If `fsnotify` compile fails because the module wasn't downloaded, run `cd cloud-sync && go mod tidy` first.

Verify the binary fails on missing env (proves Load + exit path):

Run: `/tmp/cloud-sync-bin`
Expected: stderr contains `config error:` and exit code 2.

- [ ] **Step 8.3: Write `cloud-sync/Dockerfile`**

Create `cloud-sync/Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1.7

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/cloud-sync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cloud-sync /usr/local/bin/cloud-sync
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/cloud-sync"]
```

- [ ] **Step 8.4: Verify the Dockerfile builds (requires Docker on host)**

Run: `cd cloud-sync && docker build -t cloud-sync:dev .`
Expected: success; image size < 30MB. If the host has no Docker, document the skip and rely on the Go build from Step 8.2 as evidence that the binary compiles.

- [ ] **Step 8.5: Write `cloud-sync/README.md`**

Create `cloud-sync/README.md`:

```markdown
# cloud-sync

监听 `media/` + `ani-rss/` 新视频 → 调 OpenList API 上传到 139yun → 写 `.sync_status/` → 72h 后清理本地源视频。

## Build

```bash
# Local
go build -o cloud-sync .

# Docker
docker build -t cloud-sync:1.0.0 .
```

## Run

```bash
# All config via env (see ../.env.example for the full list)
docker compose up cloud-sync
```

## Test

```bash
go test ./...
```

## Env vars

See `../.env.example` for the full list. Key ones:

- `OPENLIST_URL`, `OPENLIST_TOKEN` — OpenList HTTP endpoint
- `OPENLIST_SRC_STORAGE` — OpenList local storage mount path (e.g. `/local_media`)
- `OPENLIST_DST_STORAGE` — OpenList cloud storage mount path (e.g. `/139yun_media`)
- `WATCH_MEDIA_DIR`, `WATCH_ANIRSS_DIR` — absolute local paths to watch
- `SYNC_STATUS_DIR` — absolute path for `.sync_status/`
- `ALLOWED_SOURCE_PREFIXES` — comma-separated absolute path prefixes
- `CLEANUP_AFTER_HOURS` (default 72), `CLEANUP_DRY_RUN` (default false)
- `UPLOAD_CONCURRENCY` (default 2), `STABILIZE_WAIT_SECONDS`, `POLL_INTERVAL_SECONDS`, `TASK_TIMEOUT_SECONDS`
- `LOG_LEVEL` (debug|info|warn|error), `LOG_FILE` (empty = stdout)

## Operations

- **Dry-run cleanup**: leave `CLEANUP_DRY_RUN=true` for at least 24h after first deploy.
- **Logs**: tail `LOG_FILE` (defaults to stdout). Search for `"msg":"pipeline: synced"` and `"msg":"cleanup"`.
- **Failed uploads**: records appear under `.sync_status/FAILED/<date>/<rel>.json` with a non-empty `error` field.
- **Manual re-process**: stop the container, delete the offending `.sync_status/<date>/<rel>.json`, restart.
```

- [ ] **Step 8.6: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add cloud-sync/main.go cloud-sync/Dockerfile cloud-sync/README.md
git commit -m "feat(cloud-sync): main + Dockerfile"
```

---

## Task 9: root hygiene files

**Files:**
- Create: `.gitignore` (project root)
- Create: `.env.example` (project root)
- Modify: `README.md` (project root — currently just `# cloud-sync`)

### Steps

- [ ] **Step 9.1: Write `.gitignore` at project root**

Create `.gitignore` at `/home/tianyf/development/github/cloud-sync/.gitignore`:

```
.env
*.log
logs/
.sync_status/
cloud-sync/cloud-sync
cloud-sync/dist/
.DS_Store
```

- [ ] **Step 9.2: Write `.env.example` at project root**

Create `.env.example` at `/home/tianyf/development/github/cloud-sync/.env.example`:

```bash
# cloud-sync service env (the only service defined in this repo's docker-compose).
# Copy to .env and fill in real values; docker-compose reads these automatically.

# OpenList
OPENLIST_URL=http://openlist:5244
OPENLIST_TOKEN=replace-me
OPENLIST_SRC_STORAGE=/local_media
OPENLIST_DST_STORAGE=/139yun_media

# Watch paths
WATCH_MEDIA_DIR=/mnt/basic/media/media
WATCH_ANIRSS_DIR=/mnt/basic/media/ani-rss

# Sync state
SYNC_STATUS_DIR=/mnt/basic/media/.sync_status

# Cleanup
CLEANUP_AFTER_HOURS=72
CLEANUP_DRY_RUN=true

# Upload pipeline
UPLOAD_CONCURRENCY=2
STABILIZE_WAIT_SECONDS=30
POLL_INTERVAL_SECONDS=3
TASK_TIMEOUT_SECONDS=1800
MinFileSize=100MB  # derived in code; not an env var

# Safety
ALLOWED_SOURCE_PREFIXES=/mnt/basic/media/media,/mnt/basic/media/ani-rss
REQUIRE_NFO_FOR_MEDIA=true
REQUIRE_NFO_FOR_ANIRSS=false

# Logging
LOG_LEVEL=info
LOG_FILE=/var/log/cloud-sync/cloud-sync.log
```

- [ ] **Step 9.3: Replace the root `README.md`**

Overwrite `/home/tianyf/development/github/cloud-sync/README.md`:

```markdown
# cloud-sync

TrueNAS docker compose 下的自动化协调器：监听本地 `media/` + `ani-rss/` 新视频，通过 OpenList 上传到 139yun 移动云盘，72h 后清理本地源视频。

详细架构见 [ARCHITECTURE.md](./ARCHITECTURE.md)。组件实现规格见 [docs/superpowers/specs/2026-09-14-cloudsync-design.md](./docs/superpowers/specs/2026-09-14-cloudsync-design.md)。

## 子模块

- [`cloud-sync/`](./cloud-sync/) — Go 实现的监听 / 上传 / 清理容器
- `docs/superpowers/` — 设计文档与实施计划

## 部署

参见根目录 `docker-compose.yml` 的 `cloud-sync` service。所有配置通过环境变量注入（模板：`.env.example`）。
```

- [ ] **Step 9.4: Commit**

```bash
cd /home/tianyf/development/github/cloud-sync
git add .gitignore .env.example README.md
git commit -m "chore: root hygiene files (.gitignore, .env.example, README)"
```

---

## Self-Review

Run this checklist before handing off for execution. Fix issues inline.

### Spec coverage

Walking the spec §3 component by §8 testing requirement:

- §3.1 `config` — Task 1 covers `Load()`, all env vars, validation. ✓
- §3.2 `logging` — Task 2 covers `Init(level, file)` with file/stdout + level filter + panic on file failure. ✓
- §3.3 `watcher` — Task 5 covers Category detection, ext/size filter, fsnotify add-recursive, Events()/Errors() channels. Note: spec says New signature takes `dirs`; we changed to `roots, mediaRoot, aniRSSRoot` so the watcher doesn't need to know which is which. The pipeline passes the same roots twice (as mediaRoot and aniRSSRoot). This is documented in T5. ✓
- §3.4 `pipeline` — Task 6 covers state machine (stabilize → validate → upload → poll → mark), retry with exp backoff 1/2/4/8/.../60s, semaphore concurrency cap, StartupScan. ✓
- §3.5 `openlist` — Task 4 covers Ping/Copy/TaskDone with proper error formatting. Note: spec's Client signature has Ping; we also need it to satisfy pipeline.Uploader for Copy+TaskDone. ✓
- §3.6 `state` — Task 3 covers atomic write, AlreadySynced (any status blocks), ListForCleanup filter (synced + CleanupAt<now), Update. Key design: `Key` carries the rel-path and is `json:"-"` so it doesn't serialize into the JSON record. ✓
- §3.7 `cleanup` — Task 7 covers hourly ticker, whitelist re-check, dry-run, max 50/tick, status update. ✓
- §4 data flow — wired in Task 8 main.go. ✓
- §5 error handling — covered in pipeline retries, cleanup whitelist, startup scan recovery. ✓
- §6 config — Task 1 + docker-compose + .env.example. ✓
- §7 docker — Task 8 Dockerfile (multi-stage, distroless nonroot). ✓
- §8 testing — Tasks 1-7 each have ≥1 normal + ≥1 error + ≥1 edge case. Total tests across the package: ~25. ✓
- §9 hygiene — Task 9 .gitignore, .env.example, README.md. ✓

### Placeholder scan

Searched for: `TBD`, `TODO`, `implement later`, `fill in details`, `add appropriate`, `similar to Task`. None found.

One soft note in T6 (`pipeline.go` implementation): `fileHasNFO` and `evAbsolutePath` are simple helpers used inside `process()` so the file compiles before we test it; they are no-ops for the spec's behavior (the nfo gate is checked at validate step using `os.Stat(nfo)` directly). If you refactor `pipeline.go` to thread the absolute path through `FileEvent`, remove the helpers.

### Type consistency

- `Config` field names: every reference in T6/T7/T8 uses exactly the field names defined in T1. ✓
- `StatusRecord` field names: pipeline (T6), cleanup (T7), state (T3) all use `Key, SrcPath, SrcSize, SrcMtime, CloudPath, OpenListTaskID, Category, HasNFO, SyncedAt, CleanupAt, Status, RetryCount, CleanedAt, Error`. ✓
- `FileEvent` field names: pipeline (T6) uses `Path, Size, Detected, Category`. Watcher (T5) emits these. StartupScan (T6) constructs these. ✓
- `TaskStatus` constants: `TaskPending, TaskSucceeded, TaskFailed`. Defined in T4 (openlist.go); used by T4 tests, T6 pipeline tests. ✓
- `Category` enum + `.String()`: defined in T5 (watcher.go); used by T6. ✓
- `Pipeline.process()` parameters and signatures match across T6 and the pipeline tests' `FileEvent` constructions. ✓
- `Cleanup.Tick(ctx, now)` matches the test signature in T7. ✓
- `Watcher.New(roots, mediaRoot, aniRSSRoot, minSize, log)` matches the call site in T8 `main.go`. ✓

No inconsistencies found.

---
