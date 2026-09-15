package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Uploader interface {
	Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error)
	TaskDone(ctx context.Context, taskID string) (TaskStatus, error)
}

type Pipeline struct {
	cfg *Config
	log *slog.Logger
	up  Uploader
	st  *StateManager
	sem chan struct{}

	inflightMu sync.Mutex
	inflight   map[string]struct{}
}

func NewPipeline(cfg *Config, log *slog.Logger, up Uploader, st *StateManager) *Pipeline {
	return &Pipeline{
		cfg:      cfg,
		log:      log,
		up:       up,
		st:       st,
		sem:      make(chan struct{}, cfg.UploadConcurrency),
		inflight: make(map[string]struct{}),
	}
}

// Run consumes events until ctx is cancelled or the channel is closed. Each
// event spawns a goroutine that runs the state machine.
func (p *Pipeline) Run(ctx context.Context, events <-chan FileEvent) {
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case ev, ok := <-events:
			if !ok {
				wg.Wait()
				return
			}
			wg.Add(1)
			go func(ev FileEvent) {
				defer wg.Done()
				p.process(ctx, ev)
			}(ev)
		}
	}
}

func (p *Pipeline) process(ctx context.Context, ev FileEvent) {
	// 1. stabilize: wait until the file stops changing.
	if err := stabilize(ctx, ev.Path, p.cfg.StabilizeWait); err != nil {
		p.log.Info("pipeline: stabilize timeout, skipping", "path", ev.Path, "err", err)
		return
	}
	info, err := os.Stat(ev.Path)
	if err != nil {
		p.log.Warn("pipeline: stat failed", "path", ev.Path, "err", err)
		return
	}

	// 2. validate: whitelist + minimum size.
	if !whitelisted(ev.Path, p.cfg.AllowedPrefixes) {
		p.log.Warn("pipeline: path not whitelisted", "path", ev.Path)
		return
	}
	if info.Size() <= p.cfg.MinFileSize {
		p.log.Info("pipeline: too small, skipping", "path", ev.Path, "size", info.Size())
		return
	}

	// 3. compute key (rel-path under source root) and src/dst names for OpenList.
	key, srcName, dstName := p.computeKey(ev.Path)
	if key == "" {
		return
	}
	// Per-key in-flight guard: prevent duplicate concurrent uploads for the
	// same file. AlreadySynced below is racy when concurrency > 1 because
	// both events can pass the check before either writes a record. The
	// inflight set is the race-free dedup primitive.
	p.inflightMu.Lock()
	if _, dup := p.inflight[key]; dup {
		p.inflightMu.Unlock()
		p.log.Debug("pipeline: key already in flight, skipping duplicate", "key", key)
		return
	}
	p.inflight[key] = struct{}{}
	p.inflightMu.Unlock()
	defer func() {
		p.inflightMu.Lock()
		delete(p.inflight, key)
		p.inflightMu.Unlock()
	}()

	synced, err := p.st.AlreadySynced(key)
	if err != nil {
		p.log.Warn("pipeline: AlreadySynced check failed", "key", key, "err", err)
	}
	if synced {
		p.log.Debug("pipeline: already synced, skipping", "key", key)
		return
	}

	// 4. acquire semaphore.
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-p.sem }()

	// 5. upload with retry.
	if err := p.uploadWithRetry(ctx, key, ev.Path, srcName, dstName, info); err != nil {
		p.log.Error("pipeline: upload failed", "key", key, "err", err)
		// Only persist a "failed" record when the parent context is still
		// active. If the parent is cancelled (shutdown / restart), the
		// in-flight upload is an interruption, not a real failure, and a
		// permanent failed record would block StartupScan from re-processing
		// the file on next boot (spec §5). A task timeout uses a derived
		// context so the parent ctx stays live and the record is still
		// written — that is intentional and required by spec §8.4.
		if ctx.Err() == nil {
			p.writeFailed(key, ev.Path, info, err)
		}
	}
}

// computeKey maps an absolute source path to the state key and the OpenList
// src/dst names. The key is the path relative to whichever watch root the file
// lives under; src/dst names prefix that relative path with the single
// "media" storage subdir (all watch dirs share the same cloud-side layout).
func (p *Pipeline) computeKey(absPath string) (key, srcName, dstName string) {
	const sub = "media"
	var root string
	for _, r := range p.cfg.WatchDirs {
		if hasPrefix(absPath, r) {
			root = r
			break
		}
	}
	if root == "" {
		p.log.Warn("pipeline: file outside watch roots", "path", absPath)
		return "", "", ""
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		p.log.Warn("pipeline: rel path", "path", absPath, "err", err)
		return "", "", ""
	}
	key = filepath.ToSlash(rel)
	srcName = filepath.ToSlash(filepath.Join(sub, rel))
	dstName = srcName // 1:1 under cloud root
	return
}

func (p *Pipeline) uploadWithRetry(ctx context.Context, key, absPath, srcName, dstName string, info os.FileInfo) error {
	const maxAttempts = 3
	var lastErr error
	backoff := time.Second
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		taskCtx, cancel := context.WithTimeout(ctx, p.cfg.TaskTimeout)
		taskID, err := p.up.Copy(taskCtx, p.cfg.SrcStorage, srcName, p.cfg.DstStorage, dstName)
		if err != nil {
			cancel()
			lastErr = err
			p.log.Warn("pipeline: copy error, retrying", "attempt", attempt, "err", err)
			if attempt == maxAttempts {
				return lastErr
			}
			if !sleepCtx(ctx, backoff) {
				return lastErr
			}
			backoff = nextBackoff(backoff)
			continue
		}
		// poll the async task until it reaches a terminal state.
		for {
			st, err := p.up.TaskDone(taskCtx, taskID)
			if err != nil {
				cancel()
				lastErr = err
				break // retry whole task
			}
			switch st {
			case TaskPending:
				if !sleepCtx(ctx, p.cfg.PollInterval) {
					cancel()
					return ctx.Err()
				}
				continue
			case TaskSucceeded:
				cancel()
				now := time.Now().UTC()
				rec := &StatusRecord{
					Key:            key,
					SrcPath:        absPath,
					SrcSize:        info.Size(),
					SrcMtime:       info.ModTime().UTC(),
					CloudPath:      "/" + strings.TrimPrefix(p.cfg.DstStorage, "/") + "/" + dstName,
					OpenListTaskID: taskID,
					SyncedAt:       now,
					CleanupAt:      now.Add(p.cfg.CleanupAfter),
					Status:         "synced",
				}
				if err := p.st.Write(rec); err != nil {
					return fmt.Errorf("write state: %w", err)
				}
				p.log.Info("pipeline: synced", "key", key, "task_id", taskID)
				return nil
			case TaskFailed:
				cancel()
				lastErr = fmt.Errorf("openlist task failed")
				goto RETRY
			}
		}
	RETRY:
		if attempt == maxAttempts {
			return lastErr
		}
		if !sleepCtx(ctx, backoff) {
			return lastErr
		}
		backoff = nextBackoff(backoff)
	}
	return lastErr
}

func nextBackoff(d time.Duration) time.Duration {
	n := d * 2
	if n > 60*time.Second {
		return 60 * time.Second
	}
	return n
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func stabilize(ctx context.Context, path string, wait time.Duration) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(wait)
	lastSize, lastMtime := statSafe(path)
	if lastSize < 0 {
		return fmt.Errorf("stat failed at stabilize entry")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(deadline)):
			return nil
		case <-ticker.C:
			s, m := statSafe(path)
			if s != lastSize || !m.Equal(lastMtime) {
				lastSize, lastMtime = s, m
				deadline = time.Now().Add(wait)
			}
		}
	}
}

func statSafe(p string) (int64, time.Time) {
	info, err := os.Stat(p)
	if err != nil {
		return -1, time.Time{}
	}
	return info.Size(), info.ModTime()
}

func whitelisted(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if hasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (p *Pipeline) writeFailed(key, absPath string, info os.FileInfo, cause error) {
	now := time.Now().UTC()
	rec := &StatusRecord{
		Key:       key,
		SrcPath:   absPath,
		SrcSize:   info.Size(),
		SrcMtime:  info.ModTime().UTC(),
		SyncedAt:  now,
		CleanupAt: now.Add(p.cfg.CleanupAfter),
		Status:    "failed",
		Error:     cause.Error(),
	}
	if err := p.st.Write(rec); err != nil {
		p.log.Error("pipeline: write FAILED record", "err", err)
	}
}

// StartupScan walks both watch roots and feeds unsynced video files into the
// same per-event process() used by Run(). Runs to completion before the
// pipeline has consumed the watcher channel.
func (p *Pipeline) StartupScan(ctx context.Context) error {
	roots := p.cfg.WatchDirs
	count := 0
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable
			}
			if info.IsDir() {
				return nil
			}
			if !shouldEmit(path, info.Size(), p.cfg.MinFileSize) {
				return nil
			}
			ev := FileEvent{Path: path, Size: info.Size(), Detected: time.Now()}
			p.process(ctx, ev)
			count++
			return nil
		})
		if err != nil {
			return err
		}
	}
	p.log.Info("startup scan done", "files", count)
	return nil
}
