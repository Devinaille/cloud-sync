package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setFullEnv(t *testing.T) {
	t.Helper()
	full := map[string]string{
		"OPENLIST_URL": "http://x", "OPENLIST_TOKEN": "t",
		"OPENLIST_SRC_STORAGE": "/a", "OPENLIST_DST_STORAGE": "/b",
		"WATCH_DIRS":      "/tmp",
		"SYNC_STATUS_DIR": "/tmp", "ALLOWED_SOURCE_PREFIXES": "/tmp",
		"CLEANUP_AFTER_HOURS": "72", "UPLOAD_CONCURRENCY": "2",
		"STABILIZE_WAIT_SECONDS": "30", "POLL_INTERVAL_SECONDS": "3",
		"TASK_TIMEOUT_SECONDS": "1800",
		"LOG_LEVEL":            "info",
	}
	for k, v := range full {
		t.Setenv(k, v)
	}
}

func TestLoad_ReadsOpenListURL(t *testing.T) {
	t.Setenv("OPENLIST_URL", "http://openlist:5244")
	t.Setenv("OPENLIST_TOKEN", "tok")
	t.Setenv("OPENLIST_SRC_STORAGE", "/local_media")
	t.Setenv("OPENLIST_DST_STORAGE", "/139yun_media")
	t.Setenv("WATCH_DIRS", "/tmp")
	t.Setenv("SYNC_STATUS_DIR", "/tmp")
	t.Setenv("CLEANUP_AFTER_HOURS", "72")
	t.Setenv("UPLOAD_CONCURRENCY", "2")
	t.Setenv("STABILIZE_WAIT_SECONDS", "30")
	t.Setenv("POLL_INTERVAL_SECONDS", "3")
	t.Setenv("TASK_TIMEOUT_SECONDS", "1800")
	t.Setenv("ALLOWED_SOURCE_PREFIXES", "/tmp")
	t.Setenv("LOG_LEVEL", "info")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.OpenListURL != "http://openlist:5244" {
		t.Errorf("OpenListURL = %q, want %q", cfg.OpenListURL, "http://openlist:5244")
	}
}

func TestLoad_RejectsMissingRequiredEnv(t *testing.T) {
	t.Setenv("OPENLIST_URL", "")
	_, err := Load("")
	if err == nil {
		t.Fatal("Load() expected error for missing required envs, got nil")
	}
}

func TestLoad_ParsesWatchDirsCommaList(t *testing.T) {
	setFullEnv(t)
	t.Setenv("WATCH_DIRS", "/tmp,/var,/usr/local")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := []string{"/tmp", "/var", "/usr/local"}
	if len(cfg.WatchDirs) != 3 {
		t.Fatalf("WatchDirs len = %d, want 3 (%v)", len(cfg.WatchDirs), cfg.WatchDirs)
	}
	for i, w := range want {
		if cfg.WatchDirs[i] != w {
			t.Errorf("WatchDirs[%d] = %q, want %q", i, cfg.WatchDirs[i], w)
		}
	}
}

func TestLoad_RejectsEmptyWatchDirs(t *testing.T) {
	setFullEnv(t)
	t.Setenv("WATCH_DIRS", ", , ,")
	_, err := Load("")
	if err == nil {
		t.Fatal("Load() expected error for empty WATCH_DIRS, got nil")
	}
}

func TestLoad_FileWatchDirsOverrideEnv(t *testing.T) {
	// Env says /tmp; YAML says the two temp dirs — file wins.
	setFullEnv(t)
	t.Setenv("WATCH_DIRS", "/tmp")
	d1 := t.TempDir()
	d2 := t.TempDir()
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-sync.yaml")
	yaml := "watch_dirs:\n  - " + d1 + "\n  - " + d2 + "\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.WatchDirs) != 2 || cfg.WatchDirs[0] != d1 || cfg.WatchDirs[1] != d2 {
		t.Errorf("WatchDirs = %v, want [%s %s]", cfg.WatchDirs, d1, d2)
	}
}

func TestLoad_RejectsNonPositiveInt(t *testing.T) {
	setFullEnv(t)
	t.Setenv("UPLOAD_CONCURRENCY", "0")
	_, err := Load("")
	if err == nil {
		t.Fatal("Load() expected error for UPLOAD_CONCURRENCY=0, got nil")
	}
}

func TestLoad_RejectsEmptyPrefixList(t *testing.T) {
	setFullEnv(t)
	t.Setenv("ALLOWED_SOURCE_PREFIXES", ", , ,")
	_, err := Load("")
	if err == nil {
		t.Fatal("Load() expected error for empty prefix list, got nil")
	}
}

func TestLoad_DefaultsMinFileSize(t *testing.T) {
	setFullEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	const want = int64(100 * 1024 * 1024)
	if cfg.MinFileSize != want {
		t.Errorf("MinFileSize = %d, want %d", cfg.MinFileSize, want)
	}
}

func TestLoad_ParsesBoolFlags(t *testing.T) {
	setFullEnv(t)
	t.Setenv("CLEANUP_DRY_RUN", "true")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.CleanupDryRun {
		t.Errorf("CleanupDryRun = false, want true")
	}
}

func TestLoad_RejectsInvalidBool(t *testing.T) {
	setFullEnv(t)
	t.Setenv("CLEANUP_DRY_RUN", "tru")
	_, err := Load("")
	if err == nil {
		t.Fatal("Load() expected error for CLEANUP_DRY_RUN=tru, got nil")
	}
}

// --- YAML file tests ---

func TestLoad_FileNotFound_FallsBackToEnv(t *testing.T) {
	setFullEnv(t)
	cfg, err := Load("/nonexistent/path/cloud-sync.yaml")
	if err != nil {
		t.Fatalf("Load() should fall back to env when file missing, got: %v", err)
	}
	if cfg.OpenListURL != "http://x" {
		t.Errorf("OpenListURL = %q, want %q (from env)", cfg.OpenListURL, "http://x")
	}
}

func TestLoad_EmptyPath_FallsBackToEnv(t *testing.T) {
	setFullEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	if cfg.UploadConcurrency != 2 {
		t.Errorf("UploadConcurrency = %d, want 2", cfg.UploadConcurrency)
	}
}

func TestLoad_FileOverridesEnv(t *testing.T) {
	// Set conflicting env values; the file should win for keys it sets.
	t.Setenv("OPENLIST_URL", "http://from-env")
	t.Setenv("OPENLIST_TOKEN", "tok")
	t.Setenv("OPENLIST_SRC_STORAGE", "/a")
	t.Setenv("OPENLIST_DST_STORAGE", "/b")
	t.Setenv("WATCH_DIRS", "/tmp")
	t.Setenv("SYNC_STATUS_DIR", "/tmp")
	t.Setenv("ALLOWED_SOURCE_PREFIXES", "/tmp")
	t.Setenv("CLEANUP_AFTER_HOURS", "72")
	t.Setenv("UPLOAD_CONCURRENCY", "2")
	t.Setenv("STABILIZE_WAIT_SECONDS", "30")
	t.Setenv("POLL_INTERVAL_SECONDS", "3")
	t.Setenv("TASK_TIMEOUT_SECONDS", "1800")

	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-sync.yaml")
	yaml := `
openlist_url: http://from-file
upload_concurrency: 8
log_level: debug
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.OpenListURL != "http://from-file" {
		t.Errorf("OpenListURL = %q, want %q (file should override env)", cfg.OpenListURL, "http://from-file")
	}
	if cfg.UploadConcurrency != 8 {
		t.Errorf("UploadConcurrency = %d, want 8 (from file)", cfg.UploadConcurrency)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug (from file)", cfg.LogLevel)
	}
}

func TestLoad_FileOnlySetsPresentKeys_OthersComeFromEnv(t *testing.T) {
	setFullEnv(t)
	t.Setenv("UPLOAD_CONCURRENCY", "2")

	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-sync.yaml")
	// Only override openlist_url; everything else should come from env.
	yaml := `
openlist_url: http://only-from-file
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.OpenListURL != "http://only-from-file" {
		t.Errorf("OpenListURL = %q, want http://only-from-file", cfg.OpenListURL)
	}
	if cfg.UploadConcurrency != 2 {
		t.Errorf("UploadConcurrency = %d, want 2 (from env, file didn't set it)", cfg.UploadConcurrency)
	}
	if cfg.StabilizeWait != 30*time.Second {
		t.Errorf("StabilizeWait = %v, want 30s (from env)", cfg.StabilizeWait)
	}
}

func TestLoad_FileParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-sync.yaml")
	if err := os.WriteFile(path, []byte("not: valid: yaml: at all: :::\n  -bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for malformed YAML")
	}
}

func TestLoad_FileUnknownField(t *testing.T) {
	// KnownFields(true) means typos like "uploadconcurrency" (no underscore)
	// are rejected, surfacing the bug instead of silently being ignored.
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-sync.yaml")
	yaml := `
openlist_url: http://x
uploadconcurrency: 2
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for unknown YAML field (typo)")
	}
}

func TestLoad_BoolFileKeyAbsent_FallsBackToEnv(t *testing.T) {
	// Set env true; YAML file present but cleanup_dry_run omitted. The file
	// must NOT silently overwrite the env value with false.
	setFullEnv(t)
	t.Setenv("CLEANUP_DRY_RUN", "true")

	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-sync.yaml")
	yaml := `
openlist_url: http://x
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.CleanupDryRun {
		t.Errorf("CleanupDryRun = false, want true (env should win when YAML key absent)")
	}
}

func TestLoad_BoolFileKeyFalse_OverridesEnv(t *testing.T) {
	// Set env true; YAML explicitly says false. File wins.
	setFullEnv(t)
	t.Setenv("CLEANUP_DRY_RUN", "true")

	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-sync.yaml")
	yaml := `
openlist_url: http://x
cleanup_dry_run: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.CleanupDryRun {
		t.Errorf("CleanupDryRun = true, want false (explicit YAML should win)")
	}
}
