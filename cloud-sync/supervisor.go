package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/logging"
)

// SupervisorDeps are the injectable factories the Supervisor uses to build a
// generation. Tests substitute NewUploader with a mock.
type SupervisorDeps struct {
	NewUploader func(cfg *config.Config, log *slog.Logger) Uploader
}

// Generation is a running set of components built from one config. Its
// goroutines are tied to a derived context that Stop cancels.
type Generation struct {
	cfg      *config.Config
	uploader Uploader
	state    *StateManager
	watcher  *Watcher
	pipeline *Pipeline
	cleanup  *Cleanup
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	// mu guards stopped; it makes wg.Add in Enqueue safe against the wg.Wait in
	// Stop, which would otherwise be a "WaitGroup misuse" panic/race.
	mu      sync.Mutex
	stopped bool
}

// Supervisor owns the current Generation and can swap it on config reload.
type Supervisor struct {
	cfgPath string
	log     *slog.Logger
	deps    SupervisorDeps
	mu      sync.RWMutex
	cfg     *config.Config
	gen     *Generation
	lastErr string
	started time.Time

	// enabled is the desired task state (watcher/pipeline/cleanup). It starts
	// from cfg.TasksEnabled and is flipped at runtime by Pause/Resume. State is
	// kept even when paused so the Web UI can browse and precheck.
	enabled bool
	// state is created even while paused and rebuilt when SyncStatusDir changes.
	state *StateManager

	// one-off work (e.g. a single-file retry or cleanup) run while no
	// generation is active. oneOffMu guards shuttingDown against oneOffWG.Add so
	// Add never races the Wait in Stop. shuttingDown is set only by Stop (final
	// shutdown), not by Pause, so retries still work while paused.
	oneOffMu     sync.Mutex
	oneOffWG     sync.WaitGroup
	shuttingDown bool

	// baseCtx is the long-lived application context captured on the first
	// Start. Generations derive from it (not from per-call contexts) so a
	// Reload triggered by an HTTP handler cannot cancel the new generation when
	// the request returns.
	baseCtx context.Context
	// reloadMu serializes Reload so overlapping config PUTs cannot interleave
	// their generation teardown/swap.
	reloadMu sync.Mutex
}

// NewSupervisor builds a Supervisor. If deps.NewUploader is nil it defaults to
// the real OpenList client factory.
func NewSupervisor(cfgPath string, cfg *config.Config, log *slog.Logger, deps SupervisorDeps) *Supervisor {
	if deps.NewUploader == nil {
		deps.NewUploader = func(cfg *config.Config, log *slog.Logger) Uploader {
			return NewClient(cfg.OpenListURL, cfg.OpenListToken, log)
		}
	}
	return &Supervisor{cfgPath: cfgPath, cfg: cfg, log: log, deps: deps, enabled: cfg.TasksEnabled}
}

// Start builds and launches a generation from the current config. It returns
// an error (leaving gen nil and recording lastErr) if any component fails to
// initialize. An OpenList ping failure is logged but does not abort Start.
func (s *Supervisor) Start(ctx context.Context) error {
	// Capture the long-lived app context once (the first Start from main). All
	// generations derive from it so a Reload invoked with a short-lived caller
	// context (e.g. an HTTP request) cannot cancel the new generation.
	s.mu.Lock()
	if s.baseCtx == nil {
		s.baseCtx = ctx
	}
	baseCtx := s.baseCtx
	s.mu.Unlock()

	s.oneOffMu.Lock()
	s.shuttingDown = false
	s.oneOffMu.Unlock()

	s.mu.RLock()
	cfg := s.cfg
	log := s.log
	enabled := s.enabled
	s.mu.RUnlock()

	// The state manager is created regardless of the task switch so the Web UI
	// can browse files and run a precheck while tasks are paused.
	st := NewStateManager(cfg.SyncStatusDir, log)
	if err := st.EnsureDirs(); err != nil {
		s.setLastErr(err)
		return err
	}

	if !enabled {
		s.mu.Lock()
		s.state = st
		s.lastErr = ""
		if s.started.IsZero() {
			s.started = time.Now()
		}
		s.mu.Unlock()
		log.Info("tasks disabled; watcher/pipeline/cleanup not started (enable from the Web UI or TASKS_ENABLED)")
		return nil
	}

	// Build the uploader and ping outside the write lock: Ping can block on the
	// network (30s client timeout) and must not stall Snapshot/Reload.
	up := s.deps.NewUploader(cfg, log)
	if p, ok := up.(interface{ Ping(context.Context) error }); ok {
		if err := p.Ping(ctx); err != nil {
			log.Warn("openlist ping failed; starting anyway", "err", err)
		}
	}
	w, err := NewWatcher(cfg.WatchDirs, cfg.MinFileSize, log)
	if err != nil {
		s.setLastErr(err)
		return err
	}
	pl := NewPipeline(cfg, log, up, st)
	cl := NewCleanup(cfg, log, st)

	genCtx, cancel := context.WithCancel(baseCtx)
	g := &Generation{
		cfg:      cfg,
		uploader: up,
		state:    st,
		watcher:  w,
		pipeline: pl,
		cleanup:  cl,
		ctx:      genCtx,
		cancel:   cancel,
	}

	s.mu.Lock()
	if s.gen != nil {
		s.mu.Unlock()
		cancel()
		w.Close()
		return errors.New("supervisor: already started")
	}
	g.wg.Add(4)
	go func() {
		defer g.wg.Done()
		if err := pl.StartupScan(genCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("startup scan error", "err", err)
		}
	}()
	go func() {
		defer g.wg.Done()
		pl.Run(genCtx, w.Events())
	}()
	go func() {
		defer g.wg.Done()
		cl.Run(genCtx)
	}()
	go func() {
		defer g.wg.Done()
		for {
			select {
			case err := <-w.Errors():
				log.Warn("watcher error", "err", err)
			case <-genCtx.Done():
				return
			}
		}
	}()
	s.gen = g
	s.state = st
	if s.started.IsZero() {
		s.started = time.Now()
	}
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) setLastErr(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// Stop cancels the current generation, waits for its goroutines to finish, and
// closes its watcher. It is a no-op when nothing is running.
func (s *Supervisor) Stop() {
	// Mark shutdown before waiting so a concurrent ProcessOne cannot Add after
	// Wait begins; the same lock makes the Add/Wait pair race-free.
	s.oneOffMu.Lock()
	s.shuttingDown = true
	s.oneOffMu.Unlock()

	s.stopGeneration()
	// Wait for one-off work started while paused (bounded by upload work).
	s.oneOffWG.Wait()
}

// stopGeneration detaches and stops the current generation without touching
// one-off tracking. Pause uses it so in-flight paused retries are not blocked.
func (s *Supervisor) stopGeneration() {
	s.mu.Lock()
	g := s.gen
	s.gen = nil
	s.mu.Unlock()
	if g != nil {
		stopGeneration(g)
	}
}

// stopGeneration marks g stopped (so Enqueue stops admitting work), cancels its
// context, waits for all tracked goroutines, then closes its watcher. Setting
// stopped under g.mu before wg.Wait is what makes the wg.Add in Enqueue safe.
func stopGeneration(g *Generation) {
	g.mu.Lock()
	g.stopped = true
	g.cancel()
	g.mu.Unlock()
	g.wg.Wait()
	g.watcher.Close()
}

// Reload reparses the config file, rebuilds the logger, stops the old
// generation, and starts a new one. On a config parse error the old generation
// is preserved and the error returned.
//
// The whole body is serialized by reloadMu so two concurrent config saves
// cannot interleave teardown/swap. The new generation derives from the
// long-lived baseCtx (set on the first Start), not from ctx — a caller context
// (e.g. an HTTP request) must not be able to cancel it after Reload returns.
func (s *Supervisor) Reload(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	newCfg, err := config.Load(s.cfgPath)
	if err != nil {
		s.setLastErr(err)
		return err
	}

	// Build the new logger outside the write lock; only the pointer swap below
	// needs guarding. slog.SetDefault mutates process-global state, so keep it
	// out of the locked section too.
	log := logging.Init(newCfg.LogLevel, newCfg.LogFile)
	slog.SetDefault(log)

	s.mu.Lock()
	s.log = log
	g := s.gen
	s.gen = nil
	prevSyncDir := ""
	if s.cfg != nil {
		prevSyncDir = s.cfg.SyncStatusDir
	}
	s.cfg = newCfg
	base := s.baseCtx
	enabled := s.enabled
	if prevSyncDir != newCfg.SyncStatusDir {
		// State root moved: drop the cached manager so it is rebuilt.
		s.state = nil
	}
	s.mu.Unlock()

	if g != nil {
		stopGeneration(g)
	}

	if base == nil {
		// Reload before any Start: fall back to the caller's context.
		base = ctx
	}

	if enabled {
		return s.Start(base)
	}

	// Paused: rebuild just the state manager so browsing/precheck keep working.
	st := NewStateManager(newCfg.SyncStatusDir, log)
	if err := st.EnsureDirs(); err != nil {
		s.setLastErr(err)
		return err
	}
	s.mu.Lock()
	s.state = st
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

// Snapshot returns the current config and state manager, plus the running
// pipeline when tasks are active. ok is false only when no state has been
// initialized yet (e.g. a failed initial Start). It is true while paused, so
// the Web UI can browse files and precheck with tasks stopped.
func (s *Supervisor) Snapshot() (cfg *config.Config, st *StateManager, pl *Pipeline, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil || s.state == nil {
		return nil, nil, nil, false
	}
	var p *Pipeline
	if s.gen != nil {
		p = s.gen.pipeline
	}
	return s.cfg, s.state, p, true
}

// TasksEnabled reports the desired task state.
func (s *Supervisor) TasksEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// TasksRunning reports whether the watcher/pipeline/cleanup generation is
// currently running.
func (s *Supervisor) TasksRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gen != nil
}

// Pause stops the running tasks (watcher/pipeline/cleanup) while keeping the
// Web UI and state manager available. In-flight uploads finish or are safely
// interrupted (ctx cancel writes no failed record). One-off retries/cleanups
// started while paused keep running. It is idempotent.
func (s *Supervisor) Pause() {
	s.mu.Lock()
	s.enabled = false
	s.mu.Unlock()
	s.stopGeneration()
}

// Resume starts tasks from the current config. It re-runs StartupScan, so any
// file missed while paused is picked up.
func (s *Supervisor) Resume(ctx context.Context) error {
	s.mu.Lock()
	s.enabled = true
	s.mu.Unlock()
	return s.Start(ctx)
}

// ConfigPath returns the path the Supervisor reloads config from.
func (s *Supervisor) ConfigPath() string { return s.cfgPath }

// LastError reports the most recent Start/Reload failure (empty if healthy).
func (s *Supervisor) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// StartedAt reports when the first generation successfully started.
func (s *Supervisor) StartedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.started
}

// GenerationActive reports whether a generation is currently running.
func (s *Supervisor) GenerationActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gen != nil
}

// GenerationErr reports the current generation context's error: nil when no
// generation is running or the generation is still live, non-nil once it is
// canceled. Tests use it to assert a generation started by Reload was not
// canceled by the caller's context.
func (s *Supervisor) GenerationErr() error {
	s.mu.RLock()
	g := s.gen
	s.mu.RUnlock()
	if g == nil {
		return nil
	}
	return g.ctx.Err()
}

// Ping checks OpenList connectivity if the uploader implements an optional
// Ping(ctx) error method, returning nil when it does not. While paused (no
// generation) it builds a throwaway uploader from the current config so the
// status probe still reflects connectivity.
func (s *Supervisor) Ping(ctx context.Context) error {
	s.mu.RLock()
	g := s.gen
	cfg := s.cfg
	log := s.log
	s.mu.RUnlock()

	var up Uploader
	if g != nil {
		up = g.uploader
	} else if cfg != nil {
		up = s.deps.NewUploader(cfg, log)
	}
	if up == nil {
		return errors.New("supervisor: not initialized")
	}
	if p, ok := up.(interface{ Ping(context.Context) error }); ok {
		return p.Ping(ctx)
	}
	return nil
}

// CloudExists reports whether a path exists on OpenList, using the current
// generation's uploader or a throwaway one built from the config when paused.
// It returns an error if the uploader does not support existence checks.
func (s *Supervisor) CloudExists(ctx context.Context, path string) (bool, error) {
	s.mu.RLock()
	g := s.gen
	cfg := s.cfg
	log := s.log
	s.mu.RUnlock()

	var up Uploader
	if g != nil {
		up = g.uploader
	} else if cfg != nil {
		up = s.deps.NewUploader(cfg, log)
	}
	if up == nil {
		return false, errors.New("supervisor: not initialized")
	}
	if e, ok := up.(interface {
		Exists(context.Context, string) (bool, error)
	}); ok {
		return e.Exists(ctx, path)
	}
	return false, errors.New("uploader does not support cloud existence checks")
}

// Cleanup returns a Cleanup the web UI can use to trigger an on-demand tick. It
// returns the running generation's Cleanup, or a throwaway one built from the
// current config/state when paused (so cleanup can run without tasks). It
// returns nil only when the supervisor is not initialized.
func (s *Supervisor) Cleanup() *Cleanup {
	s.mu.RLock()
	g := s.gen
	cfg := s.cfg
	st := s.state
	log := s.log
	s.mu.RUnlock()

	if g != nil {
		return g.cleanup
	}
	if cfg == nil || st == nil {
		return nil
	}
	return NewCleanup(cfg, log, st)
}

// ProcessOne runs a single file through the pipeline. When tasks are running it
// is tracked by the generation (see Enqueue). When paused it builds a throwaway
// pipeline from the current config/state and runs it, tracked by a
// supervisor-level WaitGroup so Stop waits for it. This is what lets a
// single-file retry work without starting tasks. The run uses the long-lived
// base context, not the caller's, so it is not cancelled when the request ends.
func (s *Supervisor) ProcessOne(ctx context.Context, ev FileEvent) error {
	s.mu.RLock()
	g := s.gen
	cfg := s.cfg
	st := s.state
	log := s.log
	base := s.baseCtx
	s.mu.RUnlock()

	if g != nil {
		return s.Enqueue(ev)
	}
	if cfg == nil || st == nil {
		return errors.New("supervisor: not initialized")
	}
	if base == nil {
		base = ctx
	}

	s.oneOffMu.Lock()
	if s.shuttingDown {
		s.oneOffMu.Unlock()
		return errors.New("supervisor: shutting down")
	}
	s.oneOffWG.Add(1)
	s.oneOffMu.Unlock()

	pl := NewPipeline(cfg, log, s.deps.NewUploader(cfg, log), st)
	go func() {
		defer s.oneOffWG.Done()
		pl.Process(base, ev)
	}()
	return nil
}

// Enqueue runs Process for one event on the current generation, tracked by the
// generation WaitGroup and cancelled when the generation stops. Unlike a bare
// goroutine it is drained by Stop/Reload, so it cannot outlive its generation.
func (s *Supervisor) Enqueue(ev FileEvent) error {
	s.mu.RLock()
	g := s.gen
	s.mu.RUnlock()
	if g == nil {
		return fmt.Errorf("supervisor: no active generation")
	}
	// Hold g.mu across the Add so it cannot race the wg.Wait in stopGeneration:
	// once stopped is set (under the same lock) no further Add can happen.
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return fmt.Errorf("supervisor: no active generation")
	}
	g.wg.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		g.pipeline.Process(g.ctx, ev)
	}()
	return nil
}
