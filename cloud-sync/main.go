package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfgPath := "/config/cloud-sync.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		os.Stderr.WriteString("config error: " + err.Error() + "\n")
		os.Exit(2)
	}
	log := Init(cfg.LogLevel, cfg.LogFile)
	slog.SetDefault(log)
	log.Info("cloud-sync starting",
		"version", version,
		"commit", commit,
		"build_time", buildTime,
		"log_level", cfg.LogLevel,
		"openlist_url", cfg.OpenListURL,
		"watch_dirs", cfg.WatchDirs,
		"concurrency", cfg.UploadConcurrency,
		"cleanup_dry_run", cfg.CleanupDryRun,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	sup := NewSupervisor(cfgPath, cfg, log, SupervisorDeps{})
	if err := sup.Start(ctx); err != nil {
		log.Error("initial start failed; web UI (if enabled) remains available", "err", err)
	}
	defer sup.Stop()

	if cfg.UIListen != "" && cfg.UIListen != "-" {
		srv := NewWebServer(sup, log)
		go func() {
			if err := srv.Serve(ctx, cfg.UIListen); err != nil && ctx.Err() == nil {
				log.Error("web server stopped", "err", err)
			}
		}()
	}

	<-ctx.Done()
	log.Info("shutdown signal received, exiting")
}
