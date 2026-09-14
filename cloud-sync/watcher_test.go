package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectCategory(t *testing.T) {
	cases := []struct {
		path, mediaRoot, aniRoot string
		want                     Category
	}{
		{"/mnt/basic/media/media/Movies/X.mkv", "/mnt/basic/media/media", "/mnt/basic/media/ani-rss", CatMedia},
		{"/mnt/basic/media/ani-rss/Anime/Y.mkv", "/mnt/basic/media/media", "/mnt/basic/media/ani-rss", CatAniRSS},
	}
	for _, tc := range cases {
		got := detectCategory(tc.path, tc.mediaRoot, tc.aniRoot)
		if got != tc.want {
			t.Errorf("detectCategory(%q) = %d, want %d", tc.path, got, tc.want)
		}
	}
}

func TestCategoryString(t *testing.T) {
	if CatMedia.String() != "movie" {
		t.Errorf("CatMedia.String = %q, want movie", CatMedia.String())
	}
	if CatAniRSS.String() != "anime" {
		t.Errorf("CatAniRSS.String = %q, want anime", CatAniRSS.String())
	}
}

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
	w, err := NewWatcher([]string{dir}, dir, dir+"-other", 1024, log)
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
	w, err := NewWatcher([]string{dir}, dir, dir+"-other", 1, log)
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
			if ev.Category != CatMedia {
				t.Errorf("event category = %v, want %v", ev.Category, CatMedia)
				continue
			}
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
