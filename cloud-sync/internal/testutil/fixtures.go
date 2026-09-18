// Package testutil holds shared fixtures for tests across the module. It is a
// non-test package so other packages' tests can import it; it must not import
// any package under test (it only depends on config) to avoid import cycles.
package testutil

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud-sync/internal/config"
)

func TestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestConfig builds a Config whose SyncStatusDir and WatchDirs exist.
func TestConfig(t *testing.T) *config.Config {
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

// WriteConfigYAML writes a complete config file (all required keys) so Load
// succeeds regardless of any environment leakage from other tests.
func WriteConfigYAML(t *testing.T, path, watchDir, syncDir string, concurrency int) {
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

// PreserveLoadEnv registers the env keys that config.Load mutates via os.Setenv
// (bypassing testing's t.Setenv bookkeeping) so they are restored at test end.
func PreserveLoadEnv(t *testing.T) {
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
