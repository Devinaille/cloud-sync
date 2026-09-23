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
		c.deleteFiles(rec.SrcPath)
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

// deleteFiles removes the local video variants sharing srcPath's base name.
func (c *Cleanup) deleteFiles(srcPath string) {
	base := strings.TrimSuffix(srcPath, filepath.Ext(srcPath))
	for _, ext := range []string{".mkv", ".mp4", ".ts", ".iso"} {
		candidate := base + ext
		if _, err := os.Stat(candidate); err == nil {
			if err := os.Remove(candidate); err != nil {
				c.log.Error("cleanup: delete failed", "path", candidate, "err", err)
			}
		}
	}
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
	c.deleteFiles(rec.SrcPath)
	cleaned := time.Now().UTC()
	rec.Status = "cleaned"
	rec.CleanedAt = &cleaned
	if err := c.st.Update(rec); err != nil {
		return false, fmt.Errorf("cleanup: state update: %w", err)
	}
	c.log.Info("cleanup: manually cleaned", "key", key, "path", rec.SrcPath)
	return false, nil
}
