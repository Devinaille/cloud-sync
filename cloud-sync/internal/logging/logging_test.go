package logging

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_StdoutJSONInfoLevel(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = orig
		r.Close()
	})

	log := Init("", "")
	log.Debug("hidden")
	log.Info("visible", "k", "v")

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	os.Stdout = orig

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(out, &entry); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\noutput: %q", err, out)
	}
	if entry["msg"] != "visible" {
		t.Errorf("msg = %v, want visible", entry["msg"])
	}
	if entry["k"] != "v" {
		t.Errorf("attr k = %v, want v", entry["k"])
	}
	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	if strings.Contains(string(out), "hidden") {
		t.Errorf("debug message leaked at info level: %s", out)
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{" info ", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"gibberish", slog.LevelInfo},
		{"", slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := parseLevel(tc.in); got != tc.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestInit_FileOutputCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nested", "sub", "app.log")

	log := Init("warn", logPath)
	log.Info("not-emitted")
	log.Warn("emitted", "x", 1)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "not-emitted") {
		t.Errorf("info leaked at warn level: %s", s)
	}
	if !strings.Contains(s, "emitted") || !strings.Contains(s, "x") {
		t.Errorf("warn entry missing: %s", s)
	}
}

func TestInit_UnknownLevelDefaultsToInfo(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	log := Init("gibberish", logPath)
	log.Debug("hidden")
	log.Info("visible")
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "hidden") {
		t.Errorf("debug leaked: %s", string(data))
	}
	if !strings.Contains(string(data), "visible") {
		t.Errorf("info missing: %s", string(data))
	}
}
