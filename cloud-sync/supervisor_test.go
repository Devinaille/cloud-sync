package main

import (
	"cloud-sync/internal/config"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func supervisorTestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func mockUploaderFactory() func(cfg *config.Config, log *slog.Logger) Uploader {
	return func(cfg *config.Config, log *slog.Logger) Uploader { return newMockUploader() }
}

// supervisorTestCfg builds a Config whose SyncStatusDir and WatchDirs exist.
func supervisorTestCfg(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &config.Config{
		OpenListURL: "http://x", SrcStorage: "/local_media", DstStorage: "/139yun_media",
		WatchDirs: []string{watch}, SyncStatusDir: syncDir,
		CleanupAfter: 72 * time.Hour, UploadConcurrency: 2,
		StabilizeWait: 50 * time.Millisecond, PollInterval: 10 * time.Millisecond,
		TaskTimeout: 5 * time.Second, MinFileSize: 1024,
		AllowedPrefixes: []string{watch}, LogLevel: "info",
		TasksEnabled: true,
	}
}

// writeSupervisorYAML writes a complete config file (all required keys) so Load
// succeeds regardless of any environment leakage from other tests.
func writeSupervisorYAML(t *testing.T, path, watchDir, syncDir string, concurrency int) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "openlist_url: %q\n", "http://openlist:5244")
	fmt.Fprintf(&b, "openlist_token: %q\n", "tok")
	fmt.Fprintf(&b, "openlist_src_storage: %q\n", "/local_media")
	fmt.Fprintf(&b, "openlist_dst_storage: %q\n", "/139yun_media")
	fmt.Fprintf(&b, "watch_dirs:\n  - %q\n", watchDir)
	fmt.Fprintf(&b, "sync_status_dir: %q\n", syncDir)
	fmt.Fprintf(&b, "cleanup_after_hours: 72\n")
	fmt.Fprintf(&b, "upload_concurrency: %d\n", concurrency)
	fmt.Fprintf(&b, "stabilize_wait_seconds: 1\n")
	fmt.Fprintf(&b, "poll_interval_seconds: 1\n")
	fmt.Fprintf(&b, "task_timeout_seconds: 30\n")
	fmt.Fprintf(&b, "log_level: %q\n", "info")
	fmt.Fprintf(&b, "ui_listen: %q\n", ":8099")
	fmt.Fprintf(&b, "tasks_enabled: true\n")
	fmt.Fprintf(&b, "allowed_source_prefixes:\n  - %q\n", watchDir)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// preserveLoadEnv registers the env keys that Config.Load mutates via os.Setenv
// (bypassing testing's t.Setenv bookkeeping) so they are restored at test end.
func preserveLoadEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENLIST_URL", "OPENLIST_TOKEN", "OPENLIST_SRC_STORAGE",
		"OPENLIST_DST_STORAGE", "WATCH_DIRS", "SYNC_STATUS_DIR",
		"CLEANUP_AFTER_HOURS", "UPLOAD_CONCURRENCY", "STABILIZE_WAIT_SECONDS",
		"POLL_INTERVAL_SECONDS", "TASK_TIMEOUT_SECONDS", "LOG_LEVEL", "LOG_FILE",
		"UI_LISTEN", "CLEANUP_DRY_RUN", "OPENLIST_OVERWRITE",
		"TASKS_ENABLED", "ALLOWED_SOURCE_PREFIXES",
	} {
		t.Setenv(k, os.Getenv(k))
	}
}

func TestSupervisor_StartStop(t *testing.T) {
	cfg := supervisorTestCfg(t)
	sup := NewSupervisor("", cfg, supervisorTestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !sup.GenerationActive() {
		t.Error("GenerationActive = false after Start")
	}
	if _, _, _, ok := sup.Snapshot(); !ok {
		t.Error("Snapshot ok = false after Start")
	}
	if sup.StartedAt().IsZero() {
		t.Error("StartedAt is zero after Start")
	}
	if got := sup.LastError(); got != "" {
		t.Errorf("LastError = %q, want empty", got)
	}

	sup.Stop()
	if sup.GenerationActive() {
		t.Error("GenerationActive = true after Stop")
	}
	// State is retained after Stop so the Web UI can still browse/precheck.
	if _, _, _, ok := sup.Snapshot(); !ok {
		t.Error("Snapshot ok = false after Stop; state should remain available")
	}
}

// TestSupervisor_TasksDisabledByDefault verifies that a config with
// TasksEnabled=false yields a supervisor whose tasks are not running but whose
// state is still available for the API.
func TestSupervisor_TasksDisabledByDefault(t *testing.T) {
	cfg := supervisorTestCfg(t)
	cfg.TasksEnabled = false
	sup := NewSupervisor("", cfg, supervisorTestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sup.TasksEnabled() {
		t.Error("TasksEnabled = true, want false")
	}
	if sup.TasksRunning() {
		t.Error("TasksRunning = true, want false")
	}
	if _, _, _, ok := sup.Snapshot(); !ok {
		t.Error("Snapshot ok = false; state should be available while paused")
	}
	// StartedAt tracks process start even when tasks are off (so the UI does
	// not show a zero timestamp).
	if sup.StartedAt().IsZero() {
		t.Error("StartedAt is zero when tasks are disabled")
	}
	sup.Stop()
}

// TestSupervisor_PauseResume verifies the runtime task switch.
func TestSupervisor_PauseResume(t *testing.T) {
	cfg := supervisorTestCfg(t)
	sup := NewSupervisor("", cfg, supervisorTestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !sup.TasksRunning() {
		t.Fatal("TasksRunning = false after Start")
	}

	sup.Pause()
	if sup.TasksRunning() {
		t.Error("TasksRunning = true after Pause")
	}
	if sup.TasksEnabled() {
		t.Error("TasksEnabled = true after Pause")
	}
	if _, _, _, ok := sup.Snapshot(); !ok {
		t.Error("Snapshot ok = false after Pause; state should remain available")
	}

	if err := sup.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !sup.TasksRunning() {
		t.Error("TasksRunning = false after Resume")
	}
	if !sup.TasksEnabled() {
		t.Error("TasksEnabled = false after Resume")
	}
	sup.Stop()
}

// TestSupervisor_ReloadPreservesPause verifies a config reload does not
// silently re-enable paused tasks (the runtime switch is authoritative).
func TestSupervisor_ReloadPreservesPause(t *testing.T) {
	preserveLoadEnv(t)
	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "cloud-sync.yaml")
	writeSupervisorYAML(t, cfgPath, watch, syncDir, 2)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sup := NewSupervisor(cfgPath, cfg, supervisorTestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sup.Stop()

	sup.Pause()
	if err := sup.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if sup.TasksRunning() {
		t.Error("TasksRunning = true after Reload while paused")
	}
	if _, _, _, ok := sup.Snapshot(); !ok {
		t.Error("Snapshot ok = false after paused Reload")
	}
}

func TestSupervisor_Reload(t *testing.T) {
	preserveLoadEnv(t)
	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "cloud-sync.yaml")
	writeSupervisorYAML(t, cfgPath, watch, syncDir, 2)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UploadConcurrency != 2 {
		t.Fatalf("initial UploadConcurrency = %d, want 2", cfg.UploadConcurrency)
	}
	sup := NewSupervisor(cfgPath, cfg, supervisorTestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sup.Stop()

	writeSupervisorYAML(t, cfgPath, watch, syncDir, 3)
	if err := sup.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got, _, _, ok := sup.Snapshot()
	if !ok {
		t.Fatal("Snapshot ok = false after Reload")
	}
	if got.UploadConcurrency != 3 {
		t.Errorf("UploadConcurrency = %d, want 3 after Reload", got.UploadConcurrency)
	}
	if !sup.GenerationActive() {
		t.Error("GenerationActive = false after Reload")
	}

	// A second Reload must succeed; if the old generation were not fully torn
	// down, Start would fail with "already started".
	writeSupervisorYAML(t, cfgPath, watch, syncDir, 4)
	if err := sup.Reload(context.Background()); err != nil {
		t.Fatalf("second Reload: %v", err)
	}
	got2, _, _, ok := sup.Snapshot()
	if !ok {
		t.Fatal("Snapshot ok = false after second Reload")
	}
	if got2.UploadConcurrency != 4 {
		t.Errorf("UploadConcurrency = %d, want 4 after second Reload", got2.UploadConcurrency)
	}
}

func TestSupervisor_StartFailure_StaysDown(t *testing.T) {
	cfg := supervisorTestCfg(t)
	cfg.WatchDirs = []string{filepath.Join(t.TempDir(), "does-not-exist")}
	sup := NewSupervisor("", cfg, supervisorTestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("Start expected error for nonexistent WatchDir, got nil")
	}
	if sup.GenerationActive() {
		t.Error("GenerationActive = true after failed Start")
	}
	if _, _, _, ok := sup.Snapshot(); ok {
		t.Error("Snapshot ok = true after failed Start")
	}
	if sup.LastError() == "" {
		t.Error("LastError empty after failed Start")
	}
	sup.Stop() // must be a no-op, not panic
}

type pingFailUploader struct {
	mockUploader
	pinged bool
}

func (p *pingFailUploader) Ping(ctx context.Context) error {
	p.pinged = true
	return fmt.Errorf("simulated ping failure")
}

func TestSupervisor_PingFailureStillStarts(t *testing.T) {
	cfg := supervisorTestCfg(t)
	up := &pingFailUploader{mockUploader: mockUploader{taskStatuses: map[string]TaskStatus{}}}
	sup := NewSupervisor("", cfg, supervisorTestLogger(), SupervisorDeps{
		NewUploader: func(c *config.Config, l *slog.Logger) Uploader { return up },
	})

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start should succeed despite ping failure, got: %v", err)
	}
	if !up.pinged {
		t.Error("Ping was not called")
	}
	if !sup.GenerationActive() {
		t.Error("GenerationActive = false after ping-failed Start")
	}
	sup.Stop()
}

// TestSupervisor_Reload_CtxNotTiedToCallerCtx guards against deriving the new
// generation's context from the ctx passed to Reload. An HTTP handler's context
// is canceled as soon as it returns, which silently killed the reloaded
// generation while s.gen stayed non-nil.
func TestSupervisor_Reload_CtxNotTiedToCallerCtx(t *testing.T) {
	preserveLoadEnv(t)
	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "cloud-sync.yaml")
	writeSupervisorYAML(t, cfgPath, watch, syncDir, 2)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.MinFileSize = 1024
	cfg.StabilizeWait = 20 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	up := newMockUploader()
	sup := NewSupervisor(cfgPath, cfg, supervisorTestLogger(), SupervisorDeps{
		NewUploader: func(c *config.Config, l *slog.Logger) Uploader { return up },
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sup.Stop()

	// Reload with a context canceled before Reload even returns.
	reloadCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sup.Reload(reloadCtx); err != nil {
		t.Fatalf("Reload with canceled caller ctx: %v", err)
	}

	if !sup.GenerationActive() {
		t.Fatal("GenerationActive = false after Reload")
	}
	if err := sup.GenerationErr(); err != nil {
		t.Fatalf("GenerationErr = %v after Reload, want nil (generation canceled by caller ctx)", err)
	}

	// Liveness: the reloaded generation still processes work. The reloaded
	// config resets MinFileSize to the 100MB default, so use a sparse file.
	src := filepath.Join(watch, "After.mkv")
	if err := os.WriteFile(src, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(src, config.MinFileSizeBytes+1); err != nil {
		t.Fatal(err)
	}
	// The watcher may also notice the file; Enqueue exercises the same pipeline
	// and its per-key dedup makes a duplicate harmless.
	if err := sup.Enqueue(FileEvent{Path: src, Size: config.MinFileSizeBytes + 1, Detected: time.Now()}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		up.mu.Lock()
		n := len(up.copyCalls)
		up.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no Copy call: reloaded generation context appears canceled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
