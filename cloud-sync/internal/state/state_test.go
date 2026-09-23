package state

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
	entries, _ := os.ReadDir(filepath.Dir(st.RecordPath(rec)))
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
	past := &StatusRecord{Key: "old.mkv", SrcPath: "/x/old.mkv", SyncedAt: base, Status: "synced"}
	future := &StatusRecord{Key: "new.mkv", SrcPath: "/x/new.mkv", SyncedAt: base.Add(48 * time.Hour), Status: "synced"}
	failed := &StatusRecord{Key: "fail.mkv", SrcPath: "/x/fail.mkv", SyncedAt: base, Status: "failed"}
	for _, r := range []*StatusRecord{past, future, failed} {
		if err := st.Write(r); err != nil {
			t.Fatalf("Write %s: %v", r.Key, err)
		}
	}
	now := base.Add(48 * time.Hour)
	got, err := st.ListForCleanup(now, time.Hour)
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
		Status: "synced",
	}
	if err := st.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	now := base.Add(48 * time.Hour)
	got, err := st.ListForCleanup(now, time.Hour)
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

func TestState_ListAll_MixedBuckets(t *testing.T) {
	st := newTestState(t)
	_ = st.EnsureDirs()
	now := time.Now().UTC().Truncate(time.Second)
	recs := []*StatusRecord{
		{Key: "Movies/A.mkv", SrcPath: "/x/Movies/A.mkv", SyncedAt: now, CleanupAt: now.Add(time.Hour), Status: "synced"},
		{Key: "Movies/B.mkv", SrcPath: "/x/Movies/B.mkv", SyncedAt: now, CleanupAt: now.Add(time.Hour), Status: "failed", Error: "boom"},
		{Key: "Movies/C.mkv", SrcPath: "/x/Movies/C.mkv", SyncedAt: now.Add(-24 * time.Hour), CleanupAt: now.Add(time.Hour), Status: "synced"},
	}
	for _, r := range recs {
		if err := st.Write(r); err != nil {
			t.Fatalf("Write %s: %v", r.Key, err)
		}
	}
	got, err := st.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListAll returned %d records, want 3", len(got))
	}
	byKey := map[string]string{}
	for _, r := range got {
		byKey[r.Key] = r.Status
	}
	for _, want := range recs {
		status, ok := byKey[want.Key]
		if !ok {
			t.Errorf("ListAll missing key %q", want.Key)
			continue
		}
		if status != want.Status {
			t.Errorf("ListAll[%q].Status = %q, want %q", want.Key, status, want.Status)
		}
	}
}

func TestState_Delete_RemovesAcrossBuckets(t *testing.T) {
	st := newTestState(t)
	_ = st.EnsureDirs()
	now := time.Now().UTC().Truncate(time.Second)
	for _, r := range []*StatusRecord{
		{Key: "Movies/D.mkv", SrcPath: "/x/Movies/D.mkv", SyncedAt: now, CleanupAt: now.Add(time.Hour), Status: "synced"},
		{Key: "Movies/D.mkv", SrcPath: "/x/Movies/D.mkv", SyncedAt: now, CleanupAt: now.Add(time.Hour), Status: "failed", Error: "boom"},
	} {
		if err := st.Write(r); err != nil {
			t.Fatalf("Write %s: %v", r.Key, err)
		}
	}
	if err := st.Delete("Movies/D.mkv"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, err := st.AlreadySynced("Movies/D.mkv")
	if err != nil {
		t.Fatalf("AlreadySynced: %v", err)
	}
	if ok {
		t.Error("AlreadySynced = true after Delete, want false")
	}
	got, err := st.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	for _, r := range got {
		if r.Key == "Movies/D.mkv" {
			t.Errorf("ListAll still contains deleted key %q", r.Key)
		}
	}
}

func TestState_Delete_MissingIsNoError(t *testing.T) {
	st := newTestState(t)
	_ = st.EnsureDirs()
	if err := st.Delete("nope.mkv"); err != nil {
		t.Errorf("Delete of missing key = %v, want nil", err)
	}
}

func TestState_Delete_RejectsTraversal(t *testing.T) {
	st := newTestState(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := &StatusRecord{
		Key: "Movies/E.mkv", SrcPath: "/x/Movies/E.mkv", SyncedAt: now,
		CleanupAt: now.Add(time.Hour), Status: "synced",
	}
	if err := st.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	decoy := filepath.Join(st.root, "..", "decoy.json")
	if err := os.WriteFile(decoy, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	for _, key := range []string{"../decoy", "../../decoy", "/abs/path", ""} {
		if err := st.Delete(key); err == nil {
			t.Errorf("Delete(%q) = nil, want error", key)
		}
	}
	if _, err := os.Stat(decoy); err != nil {
		t.Errorf("decoy removed by traversal Delete: %v", err)
	}
}

func TestState_ListForCleanup_LiveDelay(t *testing.T) {
	st := newTestState(t)
	now := time.Now().UTC()
	if err := st.Write(&StatusRecord{
		Key: "A.mkv", SrcPath: "/x/A.mkv", SyncedAt: now.Add(-2 * time.Hour), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListForCleanup(now, time.Hour)
	if err != nil {
		t.Fatalf("ListForCleanup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after 1h: got %d records, want 1", len(got))
	}
	got, err = st.ListForCleanup(now, 3*time.Hour)
	if err != nil {
		t.Fatalf("ListForCleanup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("after 3h: got %d records, want 0", len(got))
	}
}

func TestState_Get(t *testing.T) {
	st := newTestState(t)
	if err := st.Write(&StatusRecord{
		Key: "Movies/X.mkv", SrcPath: "/x/Movies/X.mkv", SyncedAt: time.Now().UTC(), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("Movies/X.mkv")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.SrcPath != "/x/Movies/X.mkv" || got.Key != "Movies/X.mkv" {
		t.Fatalf("Get = %+v, want SrcPath /x/Movies/X.mkv and Key set", got)
	}
	missing, err := st.Get("nope.mkv")
	if err != nil || missing != nil {
		t.Fatalf("Get(missing) = (%v, %v), want (nil, nil)", missing, err)
	}
}
