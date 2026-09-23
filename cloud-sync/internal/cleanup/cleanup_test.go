package cleanup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/state"
)

func newTestCleanup(t *testing.T, dryRun bool) (*Cleanup, *state.StateManager, string) {
	t.Helper()
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	_ = os.MkdirAll(mediaDir, 0o755)
	_ = os.MkdirAll(syncDir, 0o755)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{
		WatchDirs: []string{mediaDir}, SyncStatusDir: syncDir,
		CleanupAfter: 72 * time.Hour, CleanupDryRun: dryRun,
		AllowedPrefixes: []string{mediaDir},
	}
	st := state.NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	return NewCleanup(cfg, log, st), st, mediaDir
}

func seedSynced(t *testing.T, st *state.StateManager, mediaDir, key string) string {
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
	rec := &state.StatusRecord{
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
	cfg := &config.Config{
		WatchDirs: []string{outOfScope}, SyncStatusDir: syncDir,
		CleanupAfter: 72 * time.Hour, CleanupDryRun: false,
		AllowedPrefixes: []string{filepath.Join(dir, "media")}, // whitelist does NOT include outOfScope
	}
	st := state.NewStateManager(syncDir, log)
	_ = st.EnsureDirs()

	full := filepath.Join(outOfScope, "evil.mkv")
	_ = os.WriteFile(full, []byte("x"), 0o644)
	past := time.Now().UTC().Add(-100 * time.Hour).Truncate(time.Second)
	_ = st.Write(&state.StatusRecord{Key: "evil.mkv", SrcPath: full, SyncedAt: past, CleanupAt: past.Add(time.Hour), Status: "synced"})

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
