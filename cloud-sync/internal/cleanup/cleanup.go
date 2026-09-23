package cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/media"
	"cloud-sync/internal/state"
)

type Cleanup struct {
	cfg *config.Config
	log *slog.Logger
	st  *state.StateManager
}

func NewCleanup(cfg *config.Config, log *slog.Logger, st *state.StateManager) *Cleanup {
	return &Cleanup{cfg: cfg, log: log, st: st}
}

// Run ticks once at startup and then every hour until ctx is cancelled.
func (c *Cleanup) Run(ctx context.Context) {
	if err := c.Tick(ctx, time.Now()); err != nil {
		c.log.Error("cleanup: tick error", "err", err)
	}
	interval := c.cfg.CleanupInterval
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Tick(ctx, time.Now()); err != nil {
				c.log.Error("cleanup: tick error", "err", err)
			}
		}
	}
}

// Tick runs a single cleanup pass. Exported for tests.
func (c *Cleanup) Tick(ctx context.Context, now time.Time) error {
	_, err := c.tick(ctx, now)
	return err
}

// TickNow runs a single cleanup pass against the current time and returns the
// number of records processed. Exported for the web UI, which triggers a
// cleanup on demand.
func (c *Cleanup) TickNow(ctx context.Context) (int, error) {
	return c.tick(ctx, time.Now())
}

func (c *Cleanup) tick(ctx context.Context, now time.Time) (int, error) {
	recs, err := c.st.ListForCleanup(now, c.cfg.CleanupAfter)
	if err != nil {
		return 0, err
	}
	const maxPerTick = 50
	processed := 0
	for _, rec := range recs {
		if processed >= maxPerTick {
			c.log.Info("cleanup: max-per-tick reached", "limit", maxPerTick)
			break
		}
		// Defensive whitelist re-check (spec §3.7).
		if !media.Whitelisted(rec.SrcPath, c.cfg.AllowedPrefixes) {
			c.log.Error("cleanup blocked: path not in whitelist", "path", rec.SrcPath)
			continue
		}
		if rec.Status != "synced" {
			continue
		}
		if c.cfg.CleanupDryRun {
			c.log.Info("[DRY-RUN] would delete", "path", rec.SrcPath)
			processed++
			continue
		}
		if err := c.deleteFiles(rec.SrcPath); err != nil {
			// Do not mark cleaned while the file is still on disk; retry next tick.
			c.log.Error("cleanup: skipping record, delete failed", "key", rec.Key, "err", err)
			continue
		}
		cleaned := time.Now().UTC()
		rec.Status = "cleaned"
		rec.CleanedAt = &cleaned
		if err := c.st.Update(rec); err != nil {
			c.log.Error("cleanup: state update failed", "key", rec.Key, "err", err)
		}
		processed++
	}
	return processed, nil
}

// deleteFiles removes the local video variants sharing srcPath's base name. It
// returns the first removal error so callers do not mark a record cleaned while
// a file is still present.
func (c *Cleanup) deleteFiles(srcPath string) error {
	base := strings.TrimSuffix(srcPath, filepath.Ext(srcPath))
	var firstErr error
	for _, ext := range []string{".mkv", ".mp4", ".ts", ".iso"} {
		candidate := base + ext
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if err := os.Remove(candidate); err != nil {
			c.log.Error("cleanup: delete failed", "path", candidate, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// CleanKey cleans a single record on demand. It honors cleanup_dry_run and
// returns dryRun=true when the deletion was only logged.
func (c *Cleanup) CleanKey(ctx context.Context, key string) (bool, error) {
	rec, err := c.st.Get(key)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, fmt.Errorf("cleanup: no record for %q", key)
	}
	if rec.Status != "synced" {
		return false, fmt.Errorf("cleanup: %q is %s, not synced", key, rec.Status)
	}
	if !media.Whitelisted(rec.SrcPath, c.cfg.AllowedPrefixes) {
		return false, fmt.Errorf("cleanup: %q not under allowed prefixes", rec.SrcPath)
	}
	if c.cfg.CleanupDryRun {
		c.log.Info("[DRY-RUN] would delete", "path", rec.SrcPath)
		return true, nil
	}
	if err := c.deleteFiles(rec.SrcPath); err != nil {
		return false, fmt.Errorf("cleanup: delete: %w", err)
	}
	cleaned := time.Now().UTC()
	rec.Status = "cleaned"
	rec.CleanedAt = &cleaned
	if err := c.st.Update(rec); err != nil {
		return false, fmt.Errorf("cleanup: state update: %w", err)
	}
	c.log.Info("cleanup: manually cleaned", "key", key, "path", rec.SrcPath)
	return false, nil
}

// RescanReport summarises a state/filesystem reconciliation.
type RescanReport struct {
	Scanned        int `json:"scanned"`
	Restored       int `json:"restored"`
	SkippedChanged int `json:"skipped_changed"`
	StillClean     int `json:"still_clean"`
}

// Rescan reconciles "cleaned" records whose local source file is still present
// (e.g. a delete that failed): when size and mtime still match the record it
// flips the record back to "synced" so the normal cleanup pass deletes the file.
// A file that changed since the record (different size/mtime) is left untouched
// to avoid deleting fresh content.
func (c *Cleanup) Rescan(ctx context.Context) (RescanReport, error) {
	var rep RescanReport
	recs, err := c.st.ListAll()
	if err != nil {
		return rep, err
	}
	for _, rec := range recs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		rep.Scanned++
		if rec.Status != "cleaned" {
			continue
		}
		if rec.SrcPath == "" || !media.Whitelisted(rec.SrcPath, c.cfg.AllowedPrefixes) {
			rep.StillClean++
			continue
		}
		info, err := os.Stat(rec.SrcPath)
		if err != nil {
			rep.StillClean++ // local file gone: correctly cleaned
			continue
		}
		if info.Size() != rec.SrcSize || !sameMtime(info.ModTime(), rec.SrcMtime) {
			rep.SkippedChanged++
			c.log.Warn("rescan: local file differs; leaving as cleaned", "key", rec.Key, "path", rec.SrcPath)
			continue
		}
		rec.Status = "synced"
		rec.CleanedAt = nil
		if err := c.st.Update(rec); err != nil {
			c.log.Error("rescan: state update failed", "key", rec.Key, "err", err)
			continue
		}
		rep.Restored++
	}
	c.log.Info("rescan done", "scanned", rep.Scanned, "restored", rep.Restored, "changed", rep.SkippedChanged, "still_clean", rep.StillClean)
	return rep, nil
}

func sameMtime(a, b time.Time) bool {
	return a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}
