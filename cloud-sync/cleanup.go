package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Cleanup struct {
	cfg *Config
	log *slog.Logger
	st  *StateManager
}

func NewCleanup(cfg *Config, log *slog.Logger, st *StateManager) *Cleanup {
	return &Cleanup{cfg: cfg, log: log, st: st}
}

// Run ticks once at startup and then every hour until ctx is cancelled.
func (c *Cleanup) Run(ctx context.Context) {
	if err := c.Tick(ctx, time.Now()); err != nil {
		c.log.Error("cleanup: tick error", "err", err)
	}
	t := time.NewTicker(time.Hour)
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
	recs, err := c.st.ListForCleanup(now)
	if err != nil {
		return err
	}
	const maxPerTick = 50
	processed := 0
	for _, rec := range recs {
		if processed >= maxPerTick {
			c.log.Info("cleanup: max-per-tick reached", "limit", maxPerTick)
			break
		}
		// Defensive whitelist re-check (spec §3.7).
		if !whitelisted(rec.SrcPath, c.cfg.AllowedPrefixes) {
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
		base := strings.TrimSuffix(rec.SrcPath, filepath.Ext(rec.SrcPath))
		for _, ext := range []string{".mkv", ".mp4", ".ts", ".iso"} {
			candidate := base + ext
			if _, err := os.Stat(candidate); err == nil {
				if err := os.Remove(candidate); err != nil {
					c.log.Error("cleanup: delete failed", "path", candidate, "err", err)
				}
			}
		}
		cleaned := time.Now().UTC()
		rec.Status = "cleaned"
		rec.CleanedAt = &cleaned
		if err := c.st.Update(rec); err != nil {
			c.log.Error("cleanup: state update failed", "key", rec.Key, "err", err)
		}
		processed++
	}
	return nil
}
