package main

import (
	"context"
	"encoding/json"
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
		AllowedPrefixes:    []string{mediaDir, aniDir},
		RequireNFOForMedia: false, RequireNFOForAniRSS: false,
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	up := newMockUploader()
	return NewPipeline(cfg, log, up, st), up, st, mediaDir
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

// readRecord reads back the persisted StatusRecord for key, letting tests
// assert on fields (like SrcPath) that are not part of the state key.
func readRecord(t *testing.T, st *StateManager, key string) *StatusRecord {
	t.Helper()
	p := st.recordPath(&StatusRecord{Key: key, SyncedAt: time.Now().UTC(), Status: "synced"})
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read record %q: %v", key, err)
	}
	var rec StatusRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("unmarshal record %q: %v", key, err)
	}
	return &rec
}

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
	if call.SrcName != "media/Movies/X.mkv" || call.DstName != "media/Movies/X.mkv" {
		t.Errorf("Copy names wrong: src=%q dst=%q", call.SrcName, call.DstName)
	}

	ok, _ := st.AlreadySynced("Movies/X.mkv")
	if !ok {
		t.Errorf("AlreadySynced = false after happy path")
	}
}

func TestPipeline_WritesAbsoluteSrcPath(t *testing.T) {
	p, _, st, mediaDir := newTestPipeline(t)
	writeVideo(t, mediaDir, "Movies/Z.mkv", true)

	src := filepath.Join(mediaDir, "Movies/Z.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan FileEvent, 1)
	events <- FileEvent{Path: src, Size: 4096, Detected: time.Now(), Category: CatMedia}
	close(events)

	done := make(chan struct{})
	go func() { p.Run(ctx, events); close(done) }()
	<-done

	rec := readRecord(t, st, "Movies/Z.mkv")
	if rec.SrcPath != src {
		t.Errorf("SrcPath = %q, want %q", rec.SrcPath, src)
	}
	if !rec.HasNFO {
		t.Errorf("HasNFO = false, want true (sibling Z.nfo exists)")
	}
}

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
	p := NewPipeline(cfg, log, up, st)
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
