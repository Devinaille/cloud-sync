package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	OpenListURL       string
	OpenListToken     string
	SrcStorage        string
	DstStorage        string
	WatchDirs         []string
	SyncStatusDir     string
	CleanupAfter      time.Duration
	CleanupDryRun     bool
	OpenListOverwrite bool
	UploadConcurrency int
	StabilizeWait     time.Duration
	PollInterval      time.Duration
	TaskTimeout       time.Duration
	CleanupInterval   time.Duration
	MinFileSize       int64
	AllowedPrefixes   []string
	LogLevel          string
	LogFile           string
	UIListen          string
	// TasksEnabled is the initial task state. Default false: watching/uploading
	// and cleanup are opt-in so a fresh deploy does not start mutating the
	// cloud before an operator confirms (see also the precheck endpoint).
	TasksEnabled bool
}

const MinFileSizeBytes = 100 * 1024 * 1024 // 100MB

// fileConfig is the on-disk YAML shape. Field names map 1:1 to env names
// (snake_case). Durations are stored as integer counts of seconds/hours so
// yaml.v3 doesn't have to parse "30s" or "5m".
type fileConfig struct {
	OpenListURL        string   `yaml:"openlist_url"`
	OpenListToken      string   `yaml:"openlist_token"`
	OpenListSrcStorage string   `yaml:"openlist_src_storage"`
	OpenListDstStorage string   `yaml:"openlist_dst_storage"`
	WatchDirs          []string `yaml:"watch_dirs"`
	SyncStatusDir      string   `yaml:"sync_status_dir"`
	CleanupAfterHours  int      `yaml:"cleanup_after_hours"`
	// CleanupDryRun is a pointer so we can distinguish "user set false" from
	// "user omitted the field". Without this, omitting the key would silently
	// overwrite a CLEANUP_DRY_RUN env value with "false" — violating the
	// documented "missing keys fall back to env" contract.
	CleanupDryRun          *bool    `yaml:"cleanup_dry_run"`
	OpenListOverwrite      *bool    `yaml:"openlist_overwrite"`
	UploadConcurrency      int      `yaml:"upload_concurrency"`
	StabilizeWaitSeconds   int      `yaml:"stabilize_wait_seconds"`
	PollIntervalSeconds    int      `yaml:"poll_interval_seconds"`
	TaskTimeoutSeconds     int      `yaml:"task_timeout_seconds"`
	CleanupIntervalSeconds int      `yaml:"cleanup_interval_seconds"`
	AllowedSourcePrefixes  []string `yaml:"allowed_source_prefixes"`
	LogLevel               string   `yaml:"log_level"`
	LogFile                string   `yaml:"log_file"`
	UIListen               *string  `yaml:"ui_listen"`
	TasksEnabled           *bool    `yaml:"tasks_enabled"`
}

// Load builds a Config from an optional YAML file plus process environment.
//
// If path is non-empty and the file exists, every key present in the file
// overrides the corresponding env var (via os.Setenv before the env pass);
// keys absent from the file fall back to env. If path is empty or the file
// does not exist, Load uses only the environment.
func Load(path string) (*Config, error) {
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			return loadWithFile(path)
		}
	}
	return loadFromEnv()
}

func loadWithFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	var f fileConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("config: yaml parse %q: %w", path, err)
	}
	// Push any explicitly-set (non-zero) field into the environment so the
	// shared env-reading path handles validation uniformly.
	if f.OpenListURL != "" {
		os.Setenv("OPENLIST_URL", f.OpenListURL)
	}
	if f.OpenListToken != "" {
		os.Setenv("OPENLIST_TOKEN", f.OpenListToken)
	}
	if f.OpenListSrcStorage != "" {
		os.Setenv("OPENLIST_SRC_STORAGE", f.OpenListSrcStorage)
	}
	if f.OpenListDstStorage != "" {
		os.Setenv("OPENLIST_DST_STORAGE", f.OpenListDstStorage)
	}
	if len(f.WatchDirs) > 0 {
		os.Setenv("WATCH_DIRS", strings.Join(f.WatchDirs, ","))
	}
	if f.SyncStatusDir != "" {
		os.Setenv("SYNC_STATUS_DIR", f.SyncStatusDir)
	}
	if f.CleanupAfterHours != 0 {
		os.Setenv("CLEANUP_AFTER_HOURS", strconv.Itoa(f.CleanupAfterHours))
	}
	if f.UploadConcurrency != 0 {
		os.Setenv("UPLOAD_CONCURRENCY", strconv.Itoa(f.UploadConcurrency))
	}
	if f.StabilizeWaitSeconds != 0 {
		os.Setenv("STABILIZE_WAIT_SECONDS", strconv.Itoa(f.StabilizeWaitSeconds))
	}
	if f.PollIntervalSeconds != 0 {
		os.Setenv("POLL_INTERVAL_SECONDS", strconv.Itoa(f.PollIntervalSeconds))
	}
	if f.TaskTimeoutSeconds != 0 {
		os.Setenv("TASK_TIMEOUT_SECONDS", strconv.Itoa(f.TaskTimeoutSeconds))
	}
	if f.CleanupIntervalSeconds != 0 {
		os.Setenv("CLEANUP_INTERVAL_SECONDS", strconv.Itoa(f.CleanupIntervalSeconds))
	}
	if f.LogLevel != "" {
		os.Setenv("LOG_LEVEL", f.LogLevel)
	}
	if f.LogFile != "" {
		os.Setenv("LOG_FILE", f.LogFile)
	}
	if f.UIListen != nil {
		os.Setenv("UI_LISTEN", *f.UIListen)
	}
	if f.TasksEnabled != nil {
		os.Setenv("TASKS_ENABLED", strconv.FormatBool(*f.TasksEnabled))
	}
	// Bool: only override env when the YAML actually set the key. Pointer-
	// nil distinguishes "absent" from "explicitly false".
	if f.CleanupDryRun != nil {
		os.Setenv("CLEANUP_DRY_RUN", strconv.FormatBool(*f.CleanupDryRun))
	}
	if f.OpenListOverwrite != nil {
		os.Setenv("OPENLIST_OVERWRITE", strconv.FormatBool(*f.OpenListOverwrite))
	}
	if len(f.AllowedSourcePrefixes) > 0 {
		os.Setenv("ALLOWED_SOURCE_PREFIXES", strings.Join(f.AllowedSourcePrefixes, ","))
	}
	return loadFromEnv()
}

func loadFromEnv() (*Config, error) {
	cfg := &Config{MinFileSize: MinFileSizeBytes}

	strs := []struct {
		env string
		dst *string
		req bool
	}{
		{"OPENLIST_URL", &cfg.OpenListURL, true},
		{"OPENLIST_TOKEN", &cfg.OpenListToken, true},
		{"OPENLIST_SRC_STORAGE", &cfg.SrcStorage, true},
		{"OPENLIST_DST_STORAGE", &cfg.DstStorage, true},
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

	// Sync status defaults under the config directory (mounted /config) so it
	// persists with the config; override with SYNC_STATUS_DIR / sync_status_dir.
	cfg.SyncStatusDir = os.Getenv("SYNC_STATUS_DIR")
	if cfg.SyncStatusDir == "" {
		cfg.SyncStatusDir = "/config/.sync_status"
	}
	if v, ok := os.LookupEnv("UI_LISTEN"); ok {
		cfg.UIListen = v
	} else {
		cfg.UIListen = ":8099"
	}
	// Tasks default to disabled: require an explicit opt-in (config or UI).
	cfg.TasksEnabled = false
	if v, ok := os.LookupEnv("TASKS_ENABLED"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("config: TASKS_ENABLED must be bool, got %q", v)
		}
		cfg.TasksEnabled = b
	}

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

	// comma-split watch dirs
	raw = os.Getenv("WATCH_DIRS")
	if raw == "" {
		return nil, fmt.Errorf("config: required env WATCH_DIRS is empty")
	}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			cfg.WatchDirs = append(cfg.WatchDirs, p)
		}
	}
	if len(cfg.WatchDirs) == 0 {
		return nil, fmt.Errorf("config: WATCH_DIRS resolved to empty list")
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
	if cfg.CleanupInterval, err = cleanupInterval("CLEANUP_INTERVAL_SECONDS"); err != nil {
		return nil, err
	}

	if cfg.CleanupDryRun, err = envBool("CLEANUP_DRY_RUN", false); err != nil {
		return nil, err
	}
	if cfg.OpenListOverwrite, err = envBool("OPENLIST_OVERWRITE", false); err != nil {
		return nil, err
	}

	// Watch dirs must exist (fsnotify needs them). The sync status dir is
	// created on demand by StateManager.EnsureDirs, so it is not stat-checked.
	for _, p := range cfg.WatchDirs {
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

// minCleanupInterval is the floor for CLEANUP_INTERVAL_SECONDS: too-frequent
// ticks would re-walk the whole state directory.
const minCleanupInterval = 300 * time.Second

// cleanupInterval reads env seconds, defaulting to one hour when unset, and
// rejects values below the floor.
func cleanupInterval(env string) (time.Duration, error) {
	v := os.Getenv(env)
	if v == "" {
		return time.Hour, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive int, got %q", env, v)
	}
	d := time.Duration(n) * time.Second
	if d < minCleanupInterval {
		return 0, fmt.Errorf("config: %s must be >= %d seconds, got %d", env, int(minCleanupInterval/time.Second), n)
	}
	return d, nil
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

func envBool(env string, def bool) (bool, error) {
	v := os.Getenv(env)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s must be bool, got %q", env, v)
	}
	return b, nil
}
