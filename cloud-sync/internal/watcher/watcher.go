package watcher

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"cloud-sync/internal/media"

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
			// A newly created (or moved-in) directory must be watched too,
			// otherwise files landing in a folder created after startup are
			// never seen. addRecursive covers nested trees moved in at once;
			// emitExisting picks up files already inside a moved-in tree.
			if info.IsDir() {
				if ev.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
					if err := w.addRecursive(ev.Name); err != nil {
						w.log.Warn("watcher: add new dir failed", "path", ev.Name, "err", err)
					}
					w.emitExisting(ev.Name)
				}
				continue
			}
			if !media.ShouldEmit(ev.Name, info.Size(), w.minSize) {
				continue
			}
			w.pushEvent(ev.Name, info.Size())
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

// pushEvent non-blockingly enqueues an event, dropping it (with a warning) if
// the buffer is full so the fsnotify loop never blocks.
func (w *Watcher) pushEvent(path string, size int64) {
	select {
	case w.events <- FileEvent{Path: path, Size: size, Detected: time.Now()}:
	default:
		w.log.Warn("watcher: events channel full, dropping", "path", path)
	}
}

// emitExisting walks root and emits events for eligible video files already
// present, so a directory moved in with content is processed without a restart.
func (w *Watcher) emitExisting(root string) {
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if media.ShouldEmit(p, fi.Size(), w.minSize) {
			w.pushEvent(p, fi.Size())
		}
		return nil
	})
}

func (w *Watcher) Events() <-chan FileEvent { return w.events }
func (w *Watcher) Errors() <-chan error     { return w.errors }

func (w *Watcher) Close() error {
	close(w.done)
	return w.fsw.Close()
}
