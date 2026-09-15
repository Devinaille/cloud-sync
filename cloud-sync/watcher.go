package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type FileEvent struct {
	Path     string
	Size     int64
	Detected time.Time
}

type Watcher struct {
	fsw     *fsnotify.Watcher
	events  chan FileEvent
	errors  chan error
	roots   []string
	minSize int64
	log     *slog.Logger
	done    chan struct{}
}

func NewWatcher(roots []string, minSize int64, log *slog.Logger) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watcher: new: %w", err)
	}
	w := &Watcher{
		fsw:     fsw,
		events:  make(chan FileEvent, 1024),
		errors:  make(chan error, 64),
		roots:   roots,
		minSize: minSize,
		log:     log,
		done:    make(chan struct{}),
	}
	for _, r := range roots {
		if err := w.addRecursive(r); err != nil {
			_ = fsw.Close()
			return nil, fmt.Errorf("watcher: add %q: %w", r, err)
		}
	}
	go w.loop()
	return w, nil
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := w.fsw.Add(p); err != nil {
				return err
			}
		}
		return nil
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			info, err := os.Stat(ev.Name)
			if err != nil {
				continue
			}
			if !shouldEmit(ev.Name, info.Size(), w.minSize) {
				continue
			}
			select {
			case w.events <- FileEvent{
				Path:     ev.Name,
				Size:     info.Size(),
				Detected: time.Now(),
			}:
			default:
				w.log.Warn("watcher: events channel full, dropping", "path", ev.Name)
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			select {
			case w.errors <- err:
			default:
				w.log.Warn("watcher: errors channel full, dropping", "err", err)
			}
		}
	}
}

func (w *Watcher) Events() <-chan FileEvent { return w.events }
func (w *Watcher) Errors() <-chan error     { return w.errors }

func (w *Watcher) Close() error {
	close(w.done)
	return w.fsw.Close()
}

func hasPrefix(path, prefix string) bool {
	if prefix == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(prefix), path)
	if err != nil {
		return false
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".ts": true, ".iso": true,
}

func shouldEmit(path string, size, minSize int64) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if !videoExts[ext] {
		return false
	}
	return size > minSize
}
