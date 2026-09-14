package main

import (
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
		"SYNC_STATUS_DIR":     "/tmp",
		"CLEANUP_AFTER_HOURS": "72", "UPLOAD_CONCURRENCY": "2",
		"STABILIZE_WAIT_SECONDS": "30", "POLL_INTERVAL_SECONDS": "3",
		"TASK_TIMEOUT_SECONDS": "1800",
		"LOG_LEVEL":            "info",
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
		"LOG_LEVEL":            "info",
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
		"LOG_LEVEL":            "info",
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

func TestLoad_RejectsInvalidBool(t *testing.T) {
	full := map[string]string{
		"OPENLIST_URL": "http://x", "OPENLIST_TOKEN": "t",
		"OPENLIST_SRC_STORAGE": "/a", "OPENLIST_DST_STORAGE": "/b",
		"WATCH_MEDIA_DIR": "/tmp", "WATCH_ANIRSS_DIR": "/tmp",
		"SYNC_STATUS_DIR": "/tmp", "ALLOWED_SOURCE_PREFIXES": "/tmp",
		"CLEANUP_AFTER_HOURS": "72", "UPLOAD_CONCURRENCY": "2",
		"STABILIZE_WAIT_SECONDS": "30", "POLL_INTERVAL_SECONDS": "3",
		"TASK_TIMEOUT_SECONDS": "1800",
		"LOG_LEVEL":            "info",
	}
	for k, v := range full {
		t.Setenv(k, v)
	}
	t.Setenv("CLEANUP_DRY_RUN", "tru")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for CLEANUP_DRY_RUN=tru, got nil")
	}
}
