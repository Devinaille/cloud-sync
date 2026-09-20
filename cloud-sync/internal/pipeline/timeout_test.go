package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloud-sync/internal/mockopenlist"
	"cloud-sync/internal/openlist"
	"cloud-sync/internal/state"
	"cloud-sync/internal/watcher"
)

func copyCalls(up *mockopenlist.Uploader) int {
	up.Mu.Lock()
	defer up.Mu.Unlock()
	return len(up.CopyCalls)
}

func processFile(t *testing.T, p *Pipeline, path string) {
	t.Helper()
	p.Process(context.Background(), watcher.FileEvent{Path: path, Size: 4096, Detected: time.Now()})
}

// readRecordStatus reads the persisted record for key in the given status
// bucket ("synced" or "failed").
func readRecordStatus(t *testing.T, st *state.StateManager, key, status string) *state.StatusRecord {
	t.Helper()
	p := st.RecordPath(&state.StatusRecord{Key: key, SyncedAt: time.Now().UTC(), Status: status})
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s record %q: %v", status, key, err)
	}
	var rec state.StatusRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("unmarshal record %q: %v", key, err)
	}
	return &rec
}

// A transport error while polling the async task must not be treated as a task
// failure: the server-side copy is still running, so re-issuing Copy would
// start a duplicate transfer. The poller must keep polling the same task id.
func TestPipeline_PollErrorKeepsPollingSameTask(t *testing.T) {
	p, up, st, mediaDir := newTestPipeline(t)
	up.Mu.Lock()
	up.TaskErr = errors.New("context deadline exceeded")
	up.TaskErrCount = 1
	up.Mu.Unlock()

	writeVideo(t, mediaDir, "Movies/Poll.mkv")
	processFile(t, p, filepath.Join(mediaDir, "Movies/Poll.mkv"))

	if n := copyCalls(up); n != 1 {
		t.Errorf("Copy calls = %d, want 1 (poll error must not re-copy)", n)
	}
	if ok, _ := st.AlreadySynced("Movies/Poll.mkv"); !ok {
		t.Fatal("expected a synced record after transient poll errors")
	}
	if rec := readRecord(t, st, "Movies/Poll.mkv"); rec.Status != "synced" {
		t.Errorf("status = %q, want synced", rec.Status)
	}
}

// Once the per-task budget elapses, the pipeline must give up without issuing
// another Copy (the server task may still be transferring), and persist a
// failed record per the documented timeout semantics.
func TestPipeline_TaskTimeoutDoesNotReCopy(t *testing.T) {
	p, up, st, mediaDir := newTestPipeline(t)
	p.cfg.TaskTimeout = 100 * time.Millisecond
	p.cfg.PollInterval = 10 * time.Millisecond
	up.Mu.Lock()
	up.TaskStatusOverride = openlist.TaskPending
	up.Mu.Unlock()

	writeVideo(t, mediaDir, "Movies/Slow.mkv")
	start := time.Now()
	processFile(t, p, filepath.Join(mediaDir, "Movies/Slow.mkv"))
	if d := time.Since(start); d > time.Second {
		t.Errorf("Process took %v; timeout must return without multi-attempt backoff", d)
	}

	if n := copyCalls(up); n != 1 {
		t.Errorf("Copy calls = %d, want 1 (timeout must not re-copy)", n)
	}
	if ok, _ := st.AlreadySynced("Movies/Slow.mkv"); !ok {
		t.Fatal("expected a failed record after task timeout")
	}
	if rec := readRecordStatus(t, st, "Movies/Slow.mkv", "failed"); rec.Status != "failed" {
		t.Errorf("status = %q, want failed", rec.Status)
	}
}

// If Copy fails but the destination already exists (the first request may have
// been queued before its response was lost), treat it as synced instead of
// retrying and creating a duplicate transfer.
func TestPipeline_CopyErrorWithExistingDestIsSynced(t *testing.T) {
	p, up, st, mediaDir := newTestPipeline(t)
	up.Mu.Lock()
	up.CopyErr = errors.New("connection refused")
	up.CloudExists = map[string]bool{"/139yun_media/media/Movies/Done.mkv": true}
	up.Mu.Unlock()

	writeVideo(t, mediaDir, "Movies/Done.mkv")
	processFile(t, p, filepath.Join(mediaDir, "Movies/Done.mkv"))

	if n := copyCalls(up); n != 1 {
		t.Errorf("Copy calls = %d, want 1 (no retry when dest exists)", n)
	}
	if ok, _ := st.AlreadySynced("Movies/Done.mkv"); !ok {
		t.Fatal("expected a synced record when dest already exists")
	}
	if rec := readRecord(t, st, "Movies/Done.mkv"); rec.Status != "synced" {
		t.Errorf("status = %q, want synced", rec.Status)
	}
}

// A genuine task failure (OpenList state 4/7) is retryable: Copys are re-issued
// up to maxAttempts and the file ends up failed when they are exhausted.
func TestPipeline_TaskFailedReCopies(t *testing.T) {
	p, up, st, mediaDir := newTestPipeline(t)
	up.Mu.Lock()
	up.TaskStatusOverride = openlist.TaskFailed
	up.Mu.Unlock()

	writeVideo(t, mediaDir, "Movies/Fail.mkv")
	processFile(t, p, filepath.Join(mediaDir, "Movies/Fail.mkv"))

	if n := copyCalls(up); n != 3 {
		t.Errorf("Copy calls = %d, want 3 (real task failure retries Copy)", n)
	}
	if ok, _ := st.AlreadySynced("Movies/Fail.mkv"); !ok {
		t.Fatal("expected a failed record after retry exhaustion")
	}
}
