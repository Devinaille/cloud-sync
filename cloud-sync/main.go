package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg, err := Load()
	if err != nil {
		os.Stderr.WriteString("config error: " + err.Error() + "\n")
		os.Exit(2)
	}
	log := Init(cfg.LogLevel, cfg.LogFile)
	slog.SetDefault(log)
	log.Info("cloud-sync starting",
		"log_level", cfg.LogLevel,
		"openlist_url", cfg.OpenListURL,
		"watch_media", cfg.WatchMediaDir,
		"watch_anirss", cfg.WatchAniRSSDir,
		"concurrency", cfg.UploadConcurrency,
		"cleanup_dry_run", cfg.CleanupDryRun,
	)

	client := NewClient(cfg.OpenListURL, cfg.OpenListToken, log)
	pingCtx, cancelPing := context.WithCancel(context.Background())
	if err := client.Ping(pingCtx); err != nil {
		cancelPing()
		log.Error("openlist ping failed at startup", "err", err)
		os.Exit(3)
	}
	cancelPing()
	log.Info("openlist ping ok")

	stateMgr := NewStateManager(cfg.SyncStatusDir, log)
	if err := stateMgr.EnsureDirs(); err != nil {
		log.Error("state init failed", "err", err)
		os.Exit(4)
	}

	watcher, err := NewWatcher(
		[]string{cfg.WatchMediaDir, cfg.WatchAniRSSDir},
		cfg.WatchMediaDir, cfg.WatchAniRSSDir,
		cfg.MinFileSize, log,
	)
	if err != nil {
		log.Error("watcher init failed", "err", err)
		os.Exit(5)
	}
	defer watcher.Close()

	pipeline := NewPipeline(cfg, log, client, stateMgr)
	cleanup := NewCleanup(cfg, log, stateMgr)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		if err := pipeline.StartupScan(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("startup scan error", "err", err)
		}
	}()

	go cleanup.Run(ctx)

	go func() {
		for {
			select {
			case err := <-watcher.Errors():
				log.Warn("watcher error", "err", err)
			case <-ctx.Done():
				return
			}
		}
	}()

	pipeline.Run(ctx, watcher.Events())

	<-ctx.Done()
	log.Info("shutdown signal received, exiting")
	<-scanDone
}
