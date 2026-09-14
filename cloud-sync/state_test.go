package main

import (
	"encoding/json"
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

func TestState_AlreadySynced_FindsFailed(t *testing.T) {
	st := newTestState(t)
	if err := st.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := &StatusRecord{
		Key: "Movies/Y/Y.mkv", SrcPath: "/x/Movies/Y/Y.mkv", SyncedAt: now,
		CleanupAt: now.Add(time.Hour), Status: "failed", Error: "boom",
	}
	if err := st.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	wantPath := filepath.Join(st.bucket("failed", now), "Movies/Y/Y.mkv.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("failed record not at expected path %s: %v", wantPath, err)
	}
	ok, err := st.AlreadySynced("Movies/Y/Y.mkv")
	if err != nil {
		t.Fatalf("AlreadySynced: %v", err)
	}
	if !ok {
		t.Error("AlreadySynced = false for failed record, want true")
	}
}

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

func TestState_ListForCleanup_NestedKeyUpdatedInPlace(t *testing.T) {
	st := newTestState(t)
	if err := st.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := &StatusRecord{
		Key: "Movies/X/X.mkv", SrcPath: "/x/Movies/X/X.mkv", SyncedAt: base,
		CleanupAt: base.Add(time.Hour), Status: "synced",
	}
	if err := st.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	now := base.Add(48 * time.Hour)
	got, err := st.ListForCleanup(now)
	if err != nil {
		t.Fatalf("ListForCleanup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListForCleanup returned %d records, want 1", len(got))
	}
	if got[0].Key != "Movies/X/X.mkv" {
		t.Fatalf("ListForCleanup Key = %q, want %q", got[0].Key, "Movies/X/X.mkv")
	}

	cleanedAt := now
	got[0].Status = "cleaned"
	got[0].CleanedAt = &cleanedAt
	if err := st.Update(got[0]); err != nil {
		t.Fatalf("Update: %v", err)
	}

	orig := filepath.Join(st.bucket("synced", base), "Movies/X/X.mkv.json")
	b, err := os.ReadFile(orig)
	if err != nil {
		t.Fatalf("original nested record missing after Update: %v", err)
	}
	var back StatusRecord
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal original: %v", err)
	}
	if back.Status != "cleaned" {
		t.Errorf("original record status = %q, want %q", back.Status, "cleaned")
	}

	dup := filepath.Join(st.bucket("cleaned", base), "X.mkv.json")
	if _, err := os.Stat(dup); !os.IsNotExist(err) {
		t.Errorf("duplicate basename record created at %s", dup)
	}
}
