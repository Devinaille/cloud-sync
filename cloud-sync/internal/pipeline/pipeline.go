package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/media"
	"cloud-sync/internal/openlist"
	"cloud-sync/internal/state"
	"cloud-sync/internal/watcher"
)

type Uploader interface {
	Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error)
	TaskPoll(ctx context.Context, taskID string) (openlist.TaskProgress, error)
	Exists(ctx context.Context, path string) (bool, error)
}

// ProgressTracker is a concurrency-safe set of in-flight uploads shared between
// the supervisor's generation pipeline and any one-off pipelines (e.g. a single
// retry while tasks are paused). It doubles as the cross-pipeline dedup claim:
// a key is claimed when an upload starts and released when it ends, so a
// StartupScan triggered by Resume cannot start a second copy of a file a paused
// one-off is still uploading.
type ProgressTracker struct {
	mu       sync.Mutex
	claimed  map[string]struct{}
	progress map[string]float64
}

func NewProgressTracker() *ProgressTracker {
	return &ProgressTracker{
		claimed:  make(map[string]struct{}),
		progress: make(map[string]float64),
	}
}

// TryClaim marks key in-flight, returning false if it already is.
func (t *ProgressTracker) TryClaim(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.claimed[key]; ok {
		return false
	}
	t.claimed[key] = struct{}{}
	return true
}

// Release clears a key's claim and progress.
func (t *ProgressTracker) Release(key string) {
	t.mu.Lock()
	delete(t.claimed, key)
	delete(t.progress, key)
	t.mu.Unlock()
}

// Has reports whether key is currently claimed.
func (t *ProgressTracker) Has(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.claimed[key]
	return ok
}

func (t *ProgressTracker) set(key string, pct float64) {
	t.mu.Lock()
	t.progress[key] = pct
	t.mu.Unlock()
}

// Snapshot returns claimed keys mapped to their percent (0 when not yet known).
func (t *ProgressTracker) Snapshot() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]float64, len(t.claimed))
	for k := range t.claimed {
		out[k] = t.progress[k]
	}
	return out
}

type Pipeline struct {
	cfg *config.Config
	log *slog.Logger
	up  Uploader
	st  *state.StateManager
	sem chan struct{}

	progress *ProgressTracker
}

func NewPipeline(cfg *config.Config, log *slog.Logger, up Uploader, st *state.StateManager) *Pipeline {
	return NewPipelineWithTracker(cfg, log, up, st, NewProgressTracker())
}

// NewPipelineWithTracker is like NewPipeline but shares the given tracker so the
// caller (the supervisor) can observe progress and dedup across pipelines.
func NewPipelineWithTracker(cfg *config.Config, log *slog.Logger, up Uploader, st *state.StateManager, tracker *ProgressTracker) *Pipeline {
	if tracker == nil {
		tracker = NewProgressTracker()
	}
	return &Pipeline{
		cfg:      cfg,
		log:      log,
		up:       up,
		st:       st,
		sem:      make(chan struct{}, cfg.UploadConcurrency),
		progress: tracker,
	}
}

func (p *Pipeline) setProgress(key string, pct float64) { p.progress.set(key, pct) }

// Inflight returns a snapshot of in-progress uploads mapped to their percent
// complete (0-100). Keys are present only while an upload is running.
func (p *Pipeline) Inflight() map[string]float64 { return p.progress.Snapshot() }

// Run consumes events until ctx is cancelled or the channel is closed. Each
// event spawns a goroutine that runs the state machine.
func (p *Pipeline) Run(ctx context.Context, events <-chan watcher.FileEvent) {
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
			go func(ev watcher.FileEvent) {
				defer wg.Done()
				p.process(ctx, ev)
			}(ev)
		}
	}
}

// Process sends one file through the same state machine used by Run.
func (p *Pipeline) Process(ctx context.Context, ev watcher.FileEvent) { p.process(ctx, ev) }

func (p *Pipeline) process(ctx context.Context, ev watcher.FileEvent) {
	info, err := os.Stat(ev.Path)
	if err != nil {
		p.log.Warn("pipeline: stat failed", "path", ev.Path, "err", err)
		return
	}

	// validate: whitelist + minimum size.
	if !media.Whitelisted(ev.Path, p.cfg.AllowedPrefixes) {
		p.log.Warn("pipeline: path not whitelisted", "path", ev.Path)
		return
	}
	if info.Size() <= p.cfg.MinFileSize {
		p.log.Info("pipeline: too small, skipping", "path", ev.Path, "size", info.Size())
		return
	}

	// compute key (rel-path under source root) and src/dst names for OpenList.
	key, srcDir, srcName, dstDir, dstName := p.computeKey(ev.Path)
	if key == "" {
		return
	}
	// Skip already-synced files BEFORE claiming or stabilizing. On restart the
	// startup scan re-walks every file; a file with a record must not wait the
	// full stabilize window again (that made restarts look like a full re-scan),
	// and must not briefly show up as "syncing".
	synced, err := p.st.AlreadySynced(key)
	if err != nil {
		p.log.Warn("pipeline: AlreadySynced check failed", "key", key, "err", err)
	}
	if synced {
		p.log.Debug("pipeline: already synced, skipping", "key", key)
		return
	}

	// Cross-pipeline in-flight guard: the shared tracker claims the key so a
	// resumed generation's StartupScan cannot start a second copy of a file a
	// paused one-off is still uploading (and vice versa). TryClaim is atomic, so
	// this is also the race-free dedup primitive when concurrency > 1.
	if !p.progress.TryClaim(key) {
		p.log.Debug("pipeline: key already in flight, skipping duplicate", "key", key)
		return
	}
	defer p.progress.Release(key)

	// stabilize: wait until the file stops changing (only for files we upload).
	if err := stabilize(ctx, ev.Path, p.cfg.StabilizeWait); err != nil {
		p.log.Info("pipeline: stabilize timeout, skipping", "path", ev.Path, "err", err)
		return
	}
	if fi, err := os.Stat(ev.Path); err == nil {
		info = fi
	}

	// acquire semaphore.
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-p.sem }()

	// upload with retry.
	if err := p.uploadWithRetry(ctx, key, ev.Path, srcDir, srcName, dstDir, dstName, info); err != nil {
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
// src/dst dir+name. The key is the path relative to whichever watch root the
// file lives under (slash-separated). src/dst dirs prefix the parent of key
// with the configured storage root; src/dst names are the basename of key.
// Paths use forward slashes (path package) because OpenList v4 expects the
// same shape regardless of host OS.
func (p *Pipeline) computeKey(absPath string) (key, srcDir, srcName, dstDir, dstName string) {
	var root string
	for _, r := range p.cfg.WatchDirs {
		if media.HasPrefix(absPath, r) {
			root = r
			break
		}
	}
	if root == "" {
		p.log.Warn("pipeline: file outside watch roots", "path", absPath)
		return "", "", "", "", ""
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		p.log.Warn("pipeline: rel path", "path", absPath, "err", err)
		return "", "", "", "", ""
	}
	key = filepath.ToSlash(rel)
	// Both watch dirs (media/, ani-rss/) live under a single "media" prefix
	// on the OpenList storage (see ARCHITECTURE.md §7). We add it here so
	// OPENLIST_SRC_STORAGE can stay bound to the storage root (e.g.
	// /local_media) without the user having to append "/media".
	const storageSubdir = "media"
	parent := path.Dir(key)
	if parent == "." {
		parent = ""
	}
	srcName = path.Base(key)
	srcDir = p.cfg.SrcStorage + "/" + storageSubdir
	if parent != "" {
		srcDir = srcDir + "/" + parent
	}
	dstName = srcName
	dstDir = p.cfg.DstStorage + "/" + storageSubdir
	if parent != "" {
		dstDir = dstDir + "/" + parent
	}
	return
}

// writeSynced persists a successful upload record.
func (p *Pipeline) writeSynced(key, absPath, cloudPath, taskID string, info os.FileInfo) error {
	now := time.Now().UTC()
	rec := &state.StatusRecord{
		Key:            key,
		SrcPath:        absPath,
		SrcSize:        info.Size(),
		SrcMtime:       info.ModTime().UTC(),
		CloudPath:      cloudPath,
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
}

func (p *Pipeline) uploadWithRetry(ctx context.Context, key, absPath, srcDir, srcName, dstDir, dstName string, info os.FileInfo) error {
	const maxAttempts = 3
	cloudPath := dstDir + "/" + dstName
	var lastErr error
	backoff := time.Second
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		copyCtx, cancel := context.WithTimeout(ctx, p.cfg.TaskTimeout)
		taskID, err := p.up.Copy(copyCtx, srcDir, srcName, dstDir, dstName, p.cfg.OpenListOverwrite)
		cancel()
		if err != nil {
			// The response may have been lost after OpenList queued the task.
			// Check the destination before re-copying so we do not start a
			// duplicate transfer. (A pre-existing destination would have made
			// skip_existing return no task, so reaching here with it present
			// means the copy landed.)
			if exists, xerr := p.up.Exists(ctx, cloudPath); xerr == nil && exists {
				p.log.Info("pipeline: copy errored but destination exists; treating as synced", "key", key, "err", err)
				return p.writeSynced(key, absPath, cloudPath, "", info)
			}
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
		// Poll the async task until it reaches a terminal state. Poll errors do
		// NOT mean the task failed: OpenList keeps copying server-side, so keep
		// polling the same task instead of re-issuing Copy. There is no total
		// time budget; a poll that exceeds TaskTimeout just starts a new one.
		for {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			pollCtx, pcancel := context.WithTimeout(ctx, p.cfg.TaskTimeout)
			tp, perr := p.up.TaskPoll(pollCtx, taskID)
			pcancel()
			if perr != nil {
				if errors.Is(perr, openlist.ErrTaskNotFound) {
					// The server-side task is gone. If the file landed on the
					// cloud, accept it; otherwise stop: a task canceled and
					// cleared in OpenList would otherwise be resurrected by a
					// fresh Copy. Mark failed; the user can retry manually.
					if exists, xerr := p.up.Exists(ctx, cloudPath); xerr == nil && exists {
						p.log.Info("pipeline: task gone but destination exists; treating as synced", "key", key)
						return p.writeSynced(key, absPath, cloudPath, "", info)
					}
					return fmt.Errorf("openlist task %s no longer exists", taskID)
				}
				p.log.Warn("pipeline: task poll error, continuing", "task_id", taskID, "err", perr)
				if !sleepCtx(ctx, p.cfg.PollInterval) {
					return ctx.Err()
				}
				continue
			}
			switch tp.Status {
			case openlist.TaskPending:
				p.setProgress(key, tp.Progress)
				if !sleepCtx(ctx, p.cfg.PollInterval) {
					return ctx.Err()
				}
				continue
			case openlist.TaskSucceeded:
				return p.writeSynced(key, absPath, cloudPath, taskID, info)
			case openlist.TaskFailed:
				lastErr = fmt.Errorf("openlist task failed")
				goto RETRY
			case openlist.TaskCanceled:
				// Canceled deliberately (e.g. from the OpenList UI). Do not
				// re-issue Copy - that would re-add a task the user stopped.
				return fmt.Errorf("openlist task canceled for %s", key)
			default:
				// Unknown status: avoid a busy poll loop.
				p.log.Warn("pipeline: unknown task status, continuing", "task_id", taskID, "status", string(tp.Status))
				if !sleepCtx(ctx, p.cfg.PollInterval) {
					return ctx.Err()
				}
				continue
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

func (p *Pipeline) writeFailed(key, absPath string, info os.FileInfo, cause error) {
	now := time.Now().UTC()
	rec := &state.StatusRecord{
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
	var wg sync.WaitGroup
	queued := 0
	skipped := 0
	for _, root := range p.cfg.WatchDirs {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable
			}
			if info.IsDir() {
				return nil
			}
			if !media.ShouldEmit(path, info.Size(), p.cfg.MinFileSize) {
				return nil
			}
			// Skip files that already have a sync record so a restart only
			// processes what is genuinely new.
			if key, _, _, _, _ := p.computeKey(path); key != "" {
				if synced, err := p.st.AlreadySynced(key); err == nil && synced {
					skipped++
					return nil
				}
			}
			ev := watcher.FileEvent{Path: path, Size: info.Size(), Detected: time.Now()}
			queued++
			// Process concurrently like the watcher path: each file waits its
			// own stabilize window in parallel instead of one file per window.
			// Upload concurrency is still bounded by the semaphore in process().
			wg.Add(1)
			go func(ev watcher.FileEvent) {
				defer wg.Done()
				p.process(ctx, ev)
			}(ev)
			return nil
		})
		if err != nil {
			wg.Wait()
			return err
		}
	}
	wg.Wait()
	p.log.Info("startup scan done", "new", queued, "skipped", skipped)
	return nil
}
