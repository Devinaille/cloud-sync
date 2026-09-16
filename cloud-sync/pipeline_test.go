package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

func (m *mockUploader) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error) {
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
		WatchDirs: []string{mediaDir, aniDir}, SyncStatusDir: syncDir,
		CleanupAfter: 72 * time.Hour, UploadConcurrency: 2,
		StabilizeWait: 50 * time.Millisecond, PollInterval: 10 * time.Millisecond,
		TaskTimeout: 5 * time.Second, MinFileSize: 1024,
		AllowedPrefixes: []string{mediaDir, aniDir},
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	up := newMockUploader()
	return NewPipeline(cfg, log, up, st), up, st, mediaDir
}

func writeVideo(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
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
	writeVideo(t, mediaDir, "Movies/X.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan FileEvent, 1)
	events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/X.mkv"), Size: 4096, Detected: time.Now()}
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
	if call.SrcDir != "/local_media/media/Movies" || call.DstDir != "/139yun_media/media/Movies" {
		t.Errorf("Copy dirs wrong: %+v", call)
	}
	if call.SrcName != "X.mkv" || call.DstName != "X.mkv" {
		t.Errorf("Copy names wrong: src=%q dst=%q", call.SrcName, call.DstName)
	}

	ok, _ := st.AlreadySynced("Movies/X.mkv")
	if !ok {
		t.Errorf("AlreadySynced = false after happy path")
	}
}

func TestPipeline_WritesAbsoluteSrcPath(t *testing.T) {
	p, _, st, mediaDir := newTestPipeline(t)
	writeVideo(t, mediaDir, "Movies/Z.mkv")

	src := filepath.Join(mediaDir, "Movies/Z.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan FileEvent, 1)
	events <- FileEvent{Path: src, Size: 4096, Detected: time.Now()}
	close(events)

	done := make(chan struct{})
	go func() { p.Run(ctx, events); close(done) }()
	<-done

	rec := readRecord(t, st, "Movies/Z.mkv")
	if rec.SrcPath != src {
		t.Errorf("SrcPath = %q, want %q", rec.SrcPath, src)
	}
	wantCloud := "/139yun_media/media/Movies/Z.mkv"
	if rec.CloudPath != wantCloud {
		t.Errorf("CloudPath = %q, want %q", rec.CloudPath, wantCloud)
	}
}

type flakyUploader struct {
	mockUploader
	failFirstN int
}

func (f *flakyUploader) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error) {
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
		WatchDirs: []string{mediaDir}, SyncStatusDir: syncDir,
		UploadConcurrency: 1, StabilizeWait: 50 * time.Millisecond,
		PollInterval: 10 * time.Millisecond, TaskTimeout: 5 * time.Second,
		MinFileSize: 1024, AllowedPrefixes: []string{mediaDir},
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	up := &flakyUploader{mockUploader: mockUploader{taskStatuses: map[string]TaskStatus{}}, failFirstN: 1}
	p := NewPipeline(cfg, log, up, st)
	writeVideo(t, mediaDir, "X.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := make(chan FileEvent, 1)
	events <- FileEvent{Path: filepath.Join(mediaDir, "X.mkv"), Size: 4096, Detected: time.Now()}
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
	events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/Y.mkv"), Size: 4096, Detected: time.Now()}
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
	writeVideo(t, mediaDir, "Movies/A.mkv")
	writeVideo(t, mediaDir, "Movies/B.mkv")
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
	if len(up.copyCalls) >= 1 {
		call := up.copyCalls[0]
		if call.SrcName != "A.mkv" {
			t.Errorf("Copy SrcName = %q, want A.mkv", call.SrcName)
		}
		if call.SrcDir != "/local_media/media/Movies" {
			t.Errorf("Copy SrcDir = %q, want /local_media/media/Movies", call.SrcDir)
		}
		if call.DstName != "A.mkv" {
			t.Errorf("Copy DstName = %q, want A.mkv", call.DstName)
		}
		if call.DstDir != "/139yun_media/media/Movies" {
			t.Errorf("Copy DstDir = %q, want /139yun_media/media/Movies", call.DstDir)
		}
	}
}

// blockingUploader records Copy calls and lets the test pause the first Copy
// (via releaseCopy) so a parent-context cancel can race against it.
type blockingUploader struct {
	mockUploader
	enterCopy   chan struct{}
	releaseCopy chan struct{}
}

func (b *blockingUploader) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error) {
	b.mu.Lock()
	b.copyCalls = append(b.copyCalls, copyCall{srcDir, srcName, dstDir, dstName})
	b.mu.Unlock()
	select {
	case <-b.enterCopy:
	default:
		close(b.enterCopy)
	}
	select {
	case <-b.releaseCopy:
		return "", context.Canceled
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestPipeline_NoFailedRecordOnParentCancel asserts that when the parent
// context is cancelled while an upload is in flight, the pipeline does NOT
// write a permanent "failed" record. A "failed" record would be treated as
// processed by AlreadySynced and prevent StartupScan from re-processing the
// file after a crash (spec §5).
func TestPipeline_NoFailedRecordOnParentCancel(t *testing.T) {
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	_ = os.MkdirAll(mediaDir, 0o755)
	_ = os.MkdirAll(syncDir, 0o755)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &Config{
		SrcStorage: "/local_media", DstStorage: "/139yun_media",
		WatchDirs: []string{mediaDir}, SyncStatusDir: syncDir,
		UploadConcurrency: 1, StabilizeWait: 50 * time.Millisecond,
		PollInterval: 10 * time.Millisecond, TaskTimeout: 5 * time.Second,
		MinFileSize: 1024, AllowedPrefixes: []string{mediaDir},
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	up := &blockingUploader{
		mockUploader: mockUploader{taskStatuses: map[string]TaskStatus{}},
		enterCopy:    make(chan struct{}),
		releaseCopy:  make(chan struct{}),
	}
	p := NewPipeline(cfg, log, up, st)
	writeVideo(t, mediaDir, "Movies/Cancel.mkv")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan FileEvent, 1)
	events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/Cancel.mkv"), Size: 4096, Detected: time.Now()}
	close(events)
	done := make(chan struct{})
	go func() { p.Run(ctx, events); close(done) }()

	select {
	case <-up.enterCopy:
	case <-time.After(3 * time.Second):
		t.Fatal("Copy was never entered")
	}
	cancel()
	close(up.releaseCopy)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after parent cancel")
	}
	synced, _ := st.AlreadySynced("Movies/Cancel.mkv")
	if synced {
		t.Errorf("AlreadySynced = true after parent ctx cancel; expected no record (must not block crash recovery)")
	}
}

// alwaysFailUploader fails Copy on every attempt and lets the test verify the
// retry-exhaustion path without exercising the poll loop.
type alwaysFailUploader struct {
	mockUploader
}

func (a *alwaysFailUploader) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.copyCalls = append(a.copyCalls, copyCall{srcDir, srcName, dstDir, dstName})
	return "", fmt.Errorf("simulated: copy always fails")
}

// TestPipeline_RetryExhaustionNoTrailingSleep asserts that after the third
// (final) attempt fails, uploadWithRetry returns immediately without an extra
// backoff sleep. With the bug present the backoff sequence is 1s+2s+4s = 7s
// before returning; with the fix it is 1s+2s = 3s.
func TestPipeline_RetryExhaustionNoTrailingSleep(t *testing.T) {
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	_ = os.MkdirAll(mediaDir, 0o755)
	_ = os.MkdirAll(syncDir, 0o755)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &Config{
		SrcStorage: "/local_media", DstStorage: "/139yun_media",
		WatchDirs: []string{mediaDir}, SyncStatusDir: syncDir,
		UploadConcurrency: 1, StabilizeWait: 50 * time.Millisecond,
		PollInterval: 10 * time.Millisecond, TaskTimeout: 5 * time.Second,
		MinFileSize: 1024, AllowedPrefixes: []string{mediaDir},
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	up := &alwaysFailUploader{mockUploader: mockUploader{taskStatuses: map[string]TaskStatus{}}}
	p := NewPipeline(cfg, log, up, st)
	writeVideo(t, mediaDir, "Movies/Exhaust.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := make(chan FileEvent, 1)
	events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/Exhaust.mkv"), Size: 4096, Detected: time.Now()}
	close(events)

	start := time.Now()
	done := make(chan struct{})
	go func() { p.Run(ctx, events); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("Run did not return: %v", ctx.Err())
	}
	elapsed := time.Since(start)

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.copyCalls) != 3 {
		t.Errorf("Copy calls = %d, want 3 (exhausted retries)", len(up.copyCalls))
	}
	// Bug: total backoff = 1s+2s+4s = 7s. Fix: 1s+2s = 3s.
	// Threshold of 5s catches the bug and gives the fix generous headroom.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, want < 5s (trailing 4s sleep indicates bug)", elapsed)
	}
	synced, _ := st.AlreadySynced("Movies/Exhaust.mkv")
	if !synced {
		t.Errorf("AlreadySynced = false; want true (failed record must be written on exhaustion)")
	}
}

// firstCopyBlocker blocks the FIRST Copy call so the test can ensure the
// second event's process() goroutine actually runs while the first is in
// flight — without that, the race never happens and dedup is untested.
// Subsequent Copy calls return immediately so any duplicate would race past
// the blocking point and be counted.
type firstCopyBlocker struct {
	mockUploader
	firstCopyEntered chan struct{}
	releaseFirstCopy chan struct{}
}

func (b *firstCopyBlocker) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error) {
	b.mu.Lock()
	n := len(b.copyCalls)
	b.copyCalls = append(b.copyCalls, copyCall{srcDir, srcName, dstDir, dstName})
	b.mu.Unlock()

	if n == 0 {
		select {
		case <-b.firstCopyEntered:
		default:
			close(b.firstCopyEntered)
		}
		<-b.releaseFirstCopy
	}
	b.mu.Lock()
	id := fmt.Sprintf("task-%s-%d", srcName, n)
	b.taskStatuses[id] = TaskPending
	b.mu.Unlock()
	return id, nil
}

// TestPipeline_DedupesConcurrentEventsForSameKey asserts that when two events
// for the same key are in flight concurrently, only one Copy is issued.
// Without a per-key guard the AlreadySynced pre-check races against the
// sem acquire (concurrency > 1) and duplicate uploads can occur.
func TestPipeline_DedupesConcurrentEventsForSameKey(t *testing.T) {
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	_ = os.MkdirAll(mediaDir, 0o755)
	_ = os.MkdirAll(syncDir, 0o755)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &Config{
		SrcStorage: "/local_media", DstStorage: "/139yun_media",
		WatchDirs: []string{mediaDir}, SyncStatusDir: syncDir,
		UploadConcurrency: 2,
		StabilizeWait:     50 * time.Millisecond,
		PollInterval:      10 * time.Millisecond,
		TaskTimeout:       5 * time.Second,
		MinFileSize:       1024,
		AllowedPrefixes:   []string{mediaDir},
	}
	st := NewStateManager(syncDir, log)
	_ = st.EnsureDirs()
	up := &firstCopyBlocker{
		mockUploader:     mockUploader{taskStatuses: map[string]TaskStatus{}},
		firstCopyEntered: make(chan struct{}),
		releaseFirstCopy: make(chan struct{}),
	}
	p := NewPipeline(cfg, log, up, st)
	writeVideo(t, mediaDir, "Movies/Dup.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan FileEvent, 2)
	events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/Dup.mkv"), Size: 4096, Detected: time.Now()}
	events <- FileEvent{Path: filepath.Join(mediaDir, "Movies/Dup.mkv"), Size: 4096, Detected: time.Now()}
	close(events)
	done := make(chan struct{})
	go func() { p.Run(ctx, events); close(done) }()

	select {
	case <-up.firstCopyEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first Copy never entered")
	}
	// Give the second event's process() goroutine time to reach the dedup
	// check (or, with the bug, to issue its own Copy).
	time.Sleep(200 * time.Millisecond)
	close(up.releaseFirstCopy)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("Run did not return: %v", ctx.Err())
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.copyCalls) != 1 {
		t.Errorf("Copy calls = %d, want 1 (concurrent duplicate must be deduped)", len(up.copyCalls))
	}
	synced, _ := st.AlreadySynced("Movies/Dup.mkv")
	if !synced {
		t.Errorf("AlreadySynced = false after dedup; want true")
	}
}
