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
		WatchDirs: []string{mediaDir}, SyncStatusDir: syncDir,
		CleanupAfter: 72 * time.Hour, CleanupDryRun: dryRun,
		AllowedPrefixes: []string{mediaDir},
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	return NewCleanup(cfg, log, st), st, mediaDir
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

func TestCleanup_TickNow_ReturnsCount(t *testing.T) {
	cu, st, mediaDir := newTestCleanup(t, true)
	seedSynced(t, st, mediaDir, "Movies/A.mkv")
	seedSynced(t, st, mediaDir, "Movies/B.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n, err := cu.TickNow(ctx)
	if err != nil {
		t.Fatalf("TickNow: %v", err)
	}
	if n < 2 {
		t.Errorf("TickNow processed = %d, want >= 2", n)
	}
}

func TestCleanup_WhitelistProtection(t *testing.T) {
	dir := t.TempDir()
	outOfScope := filepath.Join(dir, "outside")
	syncDir := filepath.Join(dir, ".sync_status")
	_ = os.MkdirAll(outOfScope, 0o755)
	_ = os.MkdirAll(syncDir, 0o755)

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &Config{
		WatchDirs: []string{outOfScope}, SyncStatusDir: syncDir,
		CleanupAfter: 72 * time.Hour, CleanupDryRun: false,
		AllowedPrefixes: []string{filepath.Join(dir, "media")}, // whitelist does NOT include outOfScope
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()

	full := filepath.Join(outOfScope, "evil.mkv")
	_ = os.WriteFile(full, []byte("x"), 0o644)
	past := time.Now().UTC().Add(-100 * time.Hour).Truncate(time.Second)
	_ = st.Write(&StatusRecord{Key: "evil.mkv", SrcPath: full, SyncedAt: past, CleanupAt: past.Add(time.Hour), Status: "synced"})

	cu := NewCleanup(cfg, log, st)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cu.Tick(ctx, time.Now()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, err := os.Stat(full); err != nil {
		t.Errorf("file outside whitelist was deleted: %v", err)
	}
}
