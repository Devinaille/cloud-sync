package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/mockopenlist"
	"cloud-sync/internal/openlist"
	"cloud-sync/internal/pipeline"
	"cloud-sync/internal/testutil"
	"cloud-sync/internal/watcher"
)

func mockUploaderFactory() func(cfg *config.Config, log *slog.Logger) pipeline.Uploader {
	return func(cfg *config.Config, log *slog.Logger) pipeline.Uploader { return mockopenlist.New() }
}

func TestSupervisor_StartStop(t *testing.T) {
	cfg := testutil.TestConfig(t)
	sup := NewSupervisor("", cfg, testutil.TestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

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
	cfg := testutil.TestConfig(t)
	cfg.TasksEnabled = false
	sup := NewSupervisor("", cfg, testutil.TestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

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
	cfg := testutil.TestConfig(t)
	sup := NewSupervisor("", cfg, testutil.TestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

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
	testutil.PreserveLoadEnv(t)
	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "cloud-sync.yaml")
	testutil.WriteConfigYAML(t, cfgPath, watch, syncDir, 2)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sup := NewSupervisor(cfgPath, cfg, testutil.TestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})
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
	testutil.PreserveLoadEnv(t)
	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "cloud-sync.yaml")
	testutil.WriteConfigYAML(t, cfgPath, watch, syncDir, 2)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UploadConcurrency != 2 {
		t.Fatalf("initial UploadConcurrency = %d, want 2", cfg.UploadConcurrency)
	}
	sup := NewSupervisor(cfgPath, cfg, testutil.TestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sup.Stop()

	testutil.WriteConfigYAML(t, cfgPath, watch, syncDir, 3)
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
	testutil.WriteConfigYAML(t, cfgPath, watch, syncDir, 4)
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
	cfg := testutil.TestConfig(t)
	cfg.WatchDirs = []string{filepath.Join(t.TempDir(), "does-not-exist")}
	sup := NewSupervisor("", cfg, testutil.TestLogger(), SupervisorDeps{NewUploader: mockUploaderFactory()})

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
	mockopenlist.Uploader
	pinged bool
}

func (p *pingFailUploader) Ping(ctx context.Context) error {
	p.pinged = true
	return fmt.Errorf("simulated ping failure")
}

func TestSupervisor_PingFailureStillStarts(t *testing.T) {
	cfg := testutil.TestConfig(t)
	up := &pingFailUploader{Uploader: mockopenlist.Uploader{TaskStatuses: map[string]openlist.TaskStatus{}}}
	sup := NewSupervisor("", cfg, testutil.TestLogger(), SupervisorDeps{
		NewUploader: func(c *config.Config, l *slog.Logger) pipeline.Uploader { return up },
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
	testutil.PreserveLoadEnv(t)
	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "cloud-sync.yaml")
	testutil.WriteConfigYAML(t, cfgPath, watch, syncDir, 2)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.MinFileSize = 1024
	cfg.StabilizeWait = 20 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	up := mockopenlist.New()
	sup := NewSupervisor(cfgPath, cfg, testutil.TestLogger(), SupervisorDeps{
		NewUploader: func(c *config.Config, l *slog.Logger) pipeline.Uploader { return up },
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
	if err := sup.Enqueue(watcher.FileEvent{Path: src, Size: config.MinFileSizeBytes + 1, Detected: time.Now()}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		up.Mu.Lock()
		n := len(up.CopyCalls)
		up.Mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no Copy call: reloaded generation context appears canceled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// While tasks are paused, a one-off retry runs on a throwaway pipeline. Its
// progress must still be visible so the Web UI can show "syncing" instead of
// falling back to "unsynced".
func TestSupervisor_ProcessOneProgressVisibleWhilePaused(t *testing.T) {
	up := mockopenlist.New()
	up.Mu.Lock()
	up.TaskPendingCount = 1000
	up.TaskProgress = 55
	up.Mu.Unlock()

	cfg := testutil.TestConfig(t)
	cfg.TasksEnabled = false
	cfg.PollInterval = 10 * time.Millisecond

	sup := NewSupervisor("", cfg, testutil.TestLogger(), SupervisorDeps{
		NewUploader: func(c *config.Config, l *slog.Logger) pipeline.Uploader { return up },
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sup.Stop)

	dir := cfg.WatchDirs[0]
	src := filepath.Join(dir, "Movies", "One.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sup.ProcessOne(context.Background(), watcher.FileEvent{Path: src, Size: 4096, Detected: time.Now()}); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		infl := sup.Inflight()
		if pct, ok := infl["Movies/One.mkv"]; ok && pct == 55 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Inflight() = %v, want Movies/One.mkv=55", sup.Inflight())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Let it finish and confirm the entry is cleared.
	up.Mu.Lock()
	up.TaskPendingCount = 0
	up.Mu.Unlock()
	deadline = time.Now().Add(3 * time.Second)
	for len(sup.Inflight()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Inflight() not cleared: %v", sup.Inflight())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Resuming tasks must not re-upload a file a paused one-off retry is still
// uploading. The shared tracker's claim makes StartupScan skip it.
func TestSupervisor_ResumeDoesNotReuploadInFlightOneOff(t *testing.T) {
	up := mockopenlist.New()
	up.Mu.Lock()
	up.TaskPendingCount = 100000
	up.TaskProgress = 30
	up.Mu.Unlock()

	cfg := testutil.TestConfig(t)
	cfg.TasksEnabled = false
	cfg.PollInterval = 10 * time.Millisecond

	sup := NewSupervisor("", cfg, testutil.TestLogger(), SupervisorDeps{
		NewUploader: func(c *config.Config, l *slog.Logger) pipeline.Uploader { return up },
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sup.Stop)

	dir := cfg.WatchDirs[0]
	src := filepath.Join(dir, "Movies", "Dup.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sup.ProcessOne(context.Background(), watcher.FileEvent{Path: src, Size: 4096, Detected: time.Now()}); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for !sup.progress.Has("Movies/Dup.mkv") {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the one-off to claim the key")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := sup.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // let the watcher/startup scan run

	up.Mu.Lock()
	n := len(up.CopyCalls)
	up.Mu.Unlock()
	if n != 1 {
		t.Fatalf("Copy calls = %d, want 1 (resume must not re-upload an in-flight one-off)", n)
	}

	// Let the one-off finish so t.Cleanup(Stop) is quick.
	up.Mu.Lock()
	up.TaskPendingCount = 0
	up.Mu.Unlock()
}
