package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShouldEmit_FilterByExtAndSize(t *testing.T) {
	cases := []struct {
		path string
		size int64
		want bool
	}{
		{"/m/a.mkv", 200 * 1024 * 1024, true},
		{"/m/a.mp4", 200 * 1024 * 1024, true},
		{"/m/a.ts", 200 * 1024 * 1024, true},
		{"/m/a.iso", 200 * 1024 * 1024, true},
		{"/m/a.txt", 200 * 1024 * 1024, false},
		{"/m/a.jpg", 200 * 1024 * 1024, false},
		{"/m/a.nfo", 200 * 1024 * 1024, false},
		{"/m/a.mkv", 99 * 1024 * 1024, false},
		{"/m/a.mkv", 100 * 1024 * 1024, false},
		{"/m/a.mkv", 100*1024*1024 + 1, true},
	}
	for _, tc := range cases {
		if got := shouldEmit(tc.path, tc.size, 100*1024*1024); got != tc.want {
			t.Errorf("shouldEmit(%q, %d) = %v, want %v", tc.path, tc.size, got, tc.want)
		}
	}
}

func TestNewClose_Lifecycle(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	w, err := NewWatcher([]string{dir}, 1024, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestWatcher_EmitsMediaEvent(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	w, err := NewWatcher([]string{dir}, 1, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	content := []byte("video-data")
	path := filepath.Join(dir, "Sample.mkv")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-w.Events():
			if ev.Size != int64(len(content)) {
				t.Errorf("event size = %d, want %d", ev.Size, len(content))
				continue
			}
			return
		case err := <-w.Errors():
			t.Fatalf("watcher error: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for media event")
		}
	}
}

// TestWatcher_SubdirCreatedAfterStart verifies that a directory created after
// the watcher started is watched, so a file written into it is picked up.
func TestWatcher_SubdirCreatedAfterStart(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	w, err := NewWatcher([]string{dir}, 1, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	sub := filepath.Join(dir, "NewShow")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// give the loop time to process the Create event and add the watch
	time.Sleep(200 * time.Millisecond)

	want := filepath.Join(sub, "EP01.mkv")
	if err := os.WriteFile(want, []byte("video-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-w.Events():
			if ev.Path == want {
				return
			}
		case err := <-w.Errors():
			t.Fatalf("watcher error: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for event from file in subdir created after start")
		}
	}
}

// TestWatcher_EmitsFilesInMovedInDir verifies that files already present in a
// directory moved in at once are emitted (no restart needed).
func TestWatcher_EmitsFilesInMovedInDir(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	w, err := NewWatcher([]string{dir}, 1, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// Build the tree elsewhere, then move it into the watched dir in one step.
	staging := t.TempDir()
	moved := filepath.Join(staging, "Show")
	if err := os.MkdirAll(filepath.Join(moved, "S01"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(moved, "S01", "E01.mkv")
	if err := os.WriteFile(want, []byte("video-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "Show")
	if err := os.Rename(moved, dst); err != nil {
		t.Fatal(err)
	}
	wantDst := filepath.Join(dst, "S01", "E01.mkv")

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-w.Events():
			if ev.Path == wantDst {
				return
			}
		case err := <-w.Errors():
			t.Fatalf("watcher error: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for event from file in moved-in dir")
		}
	}
}
