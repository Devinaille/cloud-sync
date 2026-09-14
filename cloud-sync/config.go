package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	OpenListURL         string
	OpenListToken       string
	SrcStorage          string
	DstStorage          string
	WatchMediaDir       string
	WatchAniRSSDir      string
	SyncStatusDir       string
	CleanupAfter        time.Duration
	CleanupDryRun       bool
	UploadConcurrency   int
	StabilizeWait       time.Duration
	PollInterval        time.Duration
	TaskTimeout         time.Duration
	MinFileSize         int64
	AllowedPrefixes     []string
	RequireNFOForMedia  bool
	RequireNFOForAniRSS bool
	LogLevel            string
	LogFile             string
}

const minFileSizeBytes = 100 * 1024 * 1024 // 100MB

func Load() (*Config, error) {
	cfg := &Config{MinFileSize: minFileSizeBytes}

	strs := []struct {
		env string
		dst *string
		req bool
	}{
		{"OPENLIST_URL", &cfg.OpenListURL, true},
		{"OPENLIST_TOKEN", &cfg.OpenListToken, true},
		{"OPENLIST_SRC_STORAGE", &cfg.SrcStorage, true},
		{"OPENLIST_DST_STORAGE", &cfg.DstStorage, true},
		{"WATCH_MEDIA_DIR", &cfg.WatchMediaDir, true},
		{"WATCH_ANIRSS_DIR", &cfg.WatchAniRSSDir, true},
		{"SYNC_STATUS_DIR", &cfg.SyncStatusDir, true},
		{"LOG_LEVEL", &cfg.LogLevel, true},
	}
	for _, s := range strs {
		v := os.Getenv(s.env)
		if v == "" && s.req {
			return nil, fmt.Errorf("config: required env %s is empty", s.env)
		}
		*s.dst = v
	}
	cfg.LogFile = os.Getenv("LOG_FILE") // optional

	// comma-split prefixes
	raw := os.Getenv("ALLOWED_SOURCE_PREFIXES")
	if raw == "" {
		return nil, fmt.Errorf("config: required env ALLOWED_SOURCE_PREFIXES is empty")
	}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			cfg.AllowedPrefixes = append(cfg.AllowedPrefixes, p)
		}
	}
	if len(cfg.AllowedPrefixes) == 0 {
		return nil, fmt.Errorf("config: ALLOWED_SOURCE_PREFIXES resolved to empty list")
	}

	// numeric durations / ints
	var err error
	if cfg.CleanupAfter, err = hoursDuration("CLEANUP_AFTER_HOURS"); err != nil {
		return nil, err
	}
	if cfg.UploadConcurrency, err = positiveInt("UPLOAD_CONCURRENCY"); err != nil {
		return nil, err
	}
	if cfg.StabilizeWait, err = secondsDuration("STABILIZE_WAIT_SECONDS"); err != nil {
		return nil, err
	}
	if cfg.PollInterval, err = secondsDuration("POLL_INTERVAL_SECONDS"); err != nil {
		return nil, err
	}
	if cfg.TaskTimeout, err = secondsDuration("TASK_TIMEOUT_SECONDS"); err != nil {
		return nil, err
	}

	cfg.CleanupDryRun = envBool("CLEANUP_DRY_RUN", false)
	cfg.RequireNFOForMedia = envBool("REQUIRE_NFO_FOR_MEDIA", false)
	cfg.RequireNFOForAniRSS = envBool("REQUIRE_NFO_FOR_ANIRSS", false)

	// OS-level path existence check
	for _, p := range []string{
		cfg.WatchMediaDir, cfg.WatchAniRSSDir, cfg.SyncStatusDir,
	} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("config: path %q not accessible: %w", p, err)
		}
	}

	return cfg, nil
}

func hoursDuration(env string) (time.Duration, error) {
	v := os.Getenv(env)
	if v == "" {
		return 0, fmt.Errorf("config: required env %s is empty", env)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s must be positive int, got %q", env, v)
	}
	return time.Duration(n) * time.Hour, nil
}

func secondsDuration(env string) (time.Duration, error) {
	v := os.Getenv(env)
	if v == "" {
		return 0, fmt.Errorf("config: required env %s is empty", env)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s must be positive int, got %q", env, v)
	}
	return time.Duration(n) * time.Second, nil
}

func positiveInt(env string) (int, error) {
	v := os.Getenv(env)
	if v == "" {
		return 0, fmt.Errorf("config: required env %s is empty", env)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s must be positive int, got %q", env, v)
	}
	return n, nil
}

func envBool(env string, def bool) bool {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
