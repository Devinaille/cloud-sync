package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ValidationError marks a config write rejected because the resulting file did
// not load (bad input) rather than an I/O failure.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// IsValidationError reports whether err came from config validation.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// FormValues is the structured shape the web config form reads and writes.
// Field names mirror the on-disk YAML keys. OpenListToken is left zero when the
// caller wants to keep the existing token.
type FormValues struct {
	OpenListURL            string   `json:"openlist_url"`
	OpenListToken          string   `json:"openlist_token,omitempty"`
	OpenListSrcStorage     string   `json:"openlist_src_storage"`
	OpenListDstStorage     string   `json:"openlist_dst_storage"`
	OpenListOverwrite      bool     `json:"openlist_overwrite"`
	UIListen               string   `json:"ui_listen"`
	TasksEnabled           bool     `json:"tasks_enabled"`
	WatchDirs              []string `json:"watch_dirs"`
	SyncStatusDir          string   `json:"sync_status_dir"`
	AllowedSourcePrefixes  []string `json:"allowed_source_prefixes"`
	CleanupAfterHours      int      `json:"cleanup_after_hours"`
	CleanupDryRun          bool     `json:"cleanup_dry_run"`
	CleanupIntervalSeconds int      `json:"cleanup_interval_seconds"`
	UploadConcurrency      int      `json:"upload_concurrency"`
	StabilizeWaitSeconds   int      `json:"stabilize_wait_seconds"`
	PollIntervalSeconds    int      `json:"poll_interval_seconds"`
	TaskTimeoutSeconds     int      `json:"task_timeout_seconds"`
	LogLevel               string   `json:"log_level"`
	LogFile                string   `json:"log_file"`
}

// Values projects the effective config into the form shape. The token is
// deliberately omitted so it is never sent to the browser; callers learn
// whether one is set via Config.OpenListToken != "".
func Values(cfg *Config) FormValues {
	return FormValues{
		OpenListURL:            cfg.OpenListURL,
		OpenListSrcStorage:     cfg.SrcStorage,
		OpenListDstStorage:     cfg.DstStorage,
		OpenListOverwrite:      cfg.OpenListOverwrite,
		UIListen:               cfg.UIListen,
		TasksEnabled:           cfg.TasksEnabled,
		WatchDirs:              cfg.WatchDirs,
		SyncStatusDir:          cfg.SyncStatusDir,
		AllowedSourcePrefixes:  cfg.AllowedPrefixes,
		CleanupAfterHours:      int(cfg.CleanupAfter / time.Hour),
		CleanupDryRun:          cfg.CleanupDryRun,
		CleanupIntervalSeconds: int(cfg.CleanupInterval / time.Second),
		UploadConcurrency:      cfg.UploadConcurrency,
		StabilizeWaitSeconds:   int(cfg.StabilizeWait / time.Second),
		PollIntervalSeconds:    int(cfg.PollInterval / time.Second),
		TaskTimeoutSeconds:     int(cfg.TaskTimeout / time.Second),
		LogLevel:               cfg.LogLevel,
		LogFile:                cfg.LogFile,
	}
}

// ReadFile returns the raw config file bytes.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// SaveRaw atomically replaces the config file with data after validating it
// through Load. Used by the raw-YAML editor.
func SaveRaw(path string, data []byte) error {
	return atomicValidateWrite(path, data)
}

// MergeAndSave updates the config file in place from v, preserving the
// comments, order and any unknown content of existing keys. When v.OpenListToken
// is empty the existing token is kept untouched. The result is validated
// through Load before replacing the live file.
func MergeAndSave(path string, v FormValues) error {
	var existing []byte
	if b, err := os.ReadFile(path); err == nil {
		existing = b
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("config: read %q: %w", path, err)
	}

	var doc yaml.Node
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := yaml.Unmarshal(existing, &doc); err != nil {
			return fmt.Errorf("config: yaml parse %q: %w", path, err)
		}
	} else {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config: %q does not contain a YAML mapping", path)
	}
	root := doc.Content[0]

	setString(root, "openlist_url", v.OpenListURL)
	if v.OpenListToken != "" {
		setString(root, "openlist_token", v.OpenListToken)
	}
	setString(root, "openlist_src_storage", v.OpenListSrcStorage)
	setString(root, "openlist_dst_storage", v.OpenListDstStorage)
	setBool(root, "openlist_overwrite", v.OpenListOverwrite)
	setString(root, "ui_listen", v.UIListen)
	setBool(root, "tasks_enabled", v.TasksEnabled)
	setSequence(root, "watch_dirs", v.WatchDirs)
	setString(root, "sync_status_dir", v.SyncStatusDir)
	setSequence(root, "allowed_source_prefixes", v.AllowedSourcePrefixes)
	setInt(root, "cleanup_after_hours", v.CleanupAfterHours)
	setBool(root, "cleanup_dry_run", v.CleanupDryRun)
	setInt(root, "cleanup_interval_seconds", v.CleanupIntervalSeconds)
	setInt(root, "upload_concurrency", v.UploadConcurrency)
	setInt(root, "stabilize_wait_seconds", v.StabilizeWaitSeconds)
	setInt(root, "poll_interval_seconds", v.PollIntervalSeconds)
	setInt(root, "task_timeout_seconds", v.TaskTimeoutSeconds)
	setString(root, "log_level", v.LogLevel)
	setString(root, "log_file", v.LogFile)

	out, err := marshalNode(&doc)
	if err != nil {
		return err
	}
	return atomicValidateWrite(path, out)
}

// RegenerateAndSave rewrites the config file from cfg as a fully commented
// document. Intended for the explicit "regenerate" action; it discards the
// previous file's comments and order.
func RegenerateAndSave(path string, cfg *Config) error {
	out, err := Render(cfg)
	if err != nil {
		return err
	}
	return atomicValidateWrite(path, out)
}

// Render returns a fully commented YAML document for cfg.
func Render(cfg *Config) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# cloud-sync configuration. Generated by the web UI.\n")
	fmt.Fprintf(&b, "# Field names map 1:1 to env-var names (lowercased). The loader is strict.\n\n")

	fmt.Fprintf(&b, "# OpenList HTTP endpoint and admin token.\n")
	fmt.Fprintf(&b, "openlist_url: %q\n", cfg.OpenListURL)
	fmt.Fprintf(&b, "openlist_token: %q\n", cfg.OpenListToken)
	fmt.Fprintf(&b, "# OpenList storage roots; cloud-sync appends a \"media\" subdir on upload.\n")
	fmt.Fprintf(&b, "openlist_src_storage: %q\n", cfg.SrcStorage)
	fmt.Fprintf(&b, "openlist_dst_storage: %q\n", cfg.DstStorage)
	fmt.Fprintf(&b, "# false: skip_existing (keep cloud file); true: overwrite.\n")
	fmt.Fprintf(&b, "openlist_overwrite: %t\n", cfg.OpenListOverwrite)

	fmt.Fprintf(&b, "\n# Web UI listen address. \"\" or \"-\" disables the UI. Restart to change.\n")
	fmt.Fprintf(&b, "ui_listen: %q\n", cfg.UIListen)
	fmt.Fprintf(&b, "# Tasks default off until explicitly enabled.\n")
	fmt.Fprintf(&b, "tasks_enabled: %t\n", cfg.TasksEnabled)

	fmt.Fprintf(&b, "\n# Watched local directories (must exist).\n")
	writeSequence(&b, "watch_dirs", cfg.WatchDirs)
	fmt.Fprintf(&b, "\n# Absolute path prefixes cleanup may delete under.\n")
	writeSequence(&b, "allowed_source_prefixes", cfg.AllowedPrefixes)
	fmt.Fprintf(&b, "\n# Where .sync_status/<date>/... is written.\n")
	fmt.Fprintf(&b, "sync_status_dir: %q\n", cfg.SyncStatusDir)

	fmt.Fprintf(&b, "\n# Cleanup policy.\n")
	fmt.Fprintf(&b, "cleanup_after_hours: %d\n", int(cfg.CleanupAfter/time.Hour))
	fmt.Fprintf(&b, "cleanup_dry_run: %t\n", cfg.CleanupDryRun)
	fmt.Fprintf(&b, "cleanup_interval_seconds: %d\n", int(cfg.CleanupInterval/time.Second))

	fmt.Fprintf(&b, "\n# Upload pipeline.\n")
	fmt.Fprintf(&b, "upload_concurrency: %d\n", cfg.UploadConcurrency)
	fmt.Fprintf(&b, "stabilize_wait_seconds: %d\n", int(cfg.StabilizeWait/time.Second))
	fmt.Fprintf(&b, "poll_interval_seconds: %d\n", int(cfg.PollInterval/time.Second))
	fmt.Fprintf(&b, "task_timeout_seconds: %d\n", int(cfg.TaskTimeout/time.Second))

	fmt.Fprintf(&b, "\n# Logging.\n")
	fmt.Fprintf(&b, "log_level: %q\n", cfg.LogLevel)
	fmt.Fprintf(&b, "log_file: %q\n", cfg.LogFile)

	return []byte(b.String()), nil
}

func writeSequence(b *strings.Builder, key string, items []string) {
	if len(items) == 0 {
		fmt.Fprintf(b, "%s: []\n", key)
		return
	}
	fmt.Fprintf(b, "%s:\n", key)
	for _, it := range items {
		fmt.Fprintf(b, "  - %q\n", it)
	}
}

// atomicValidateWrite writes data to a temp file in the config directory,
// validates it with Load, then atomically renames it over path.
func atomicValidateWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "cloud-sync-*.yaml")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: close temp: %w", err)
	}
	if _, err := Load(tmpName); err != nil {
		os.Remove(tmpName)
		return &ValidationError{Err: err}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: replace: %w", err)
	}
	return nil
}

func marshalNode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("config: encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("config: encode yaml: %w", err)
	}
	return buf.Bytes(), nil
}

func findKey(root *yaml.Node, key string) int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func setScalar(root *yaml.Node, key, value, tag string) {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	if i := findKey(root, key); i >= 0 {
		prev := root.Content[i+1]
		node.HeadComment, node.LineComment, node.FootComment = prev.HeadComment, prev.LineComment, prev.FootComment
		root.Content[i+1] = node
		return
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		node,
	)
}

func setString(root *yaml.Node, key, value string) { setScalar(root, key, value, "!!str") }
func setInt(root *yaml.Node, key string, value int) {
	setScalar(root, key, strconv.Itoa(value), "!!int")
}
func setBool(root *yaml.Node, key string, value bool) {
	setScalar(root, key, strconv.FormatBool(value), "!!bool")
}

func setSequence(root *yaml.Node, key string, items []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, it := range items {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: it})
	}
	if i := findKey(root, key); i >= 0 {
		prev := root.Content[i+1]
		seq.HeadComment, seq.LineComment, seq.FootComment = prev.HeadComment, prev.LineComment, prev.FootComment
		root.Content[i+1] = seq
		return
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
}
