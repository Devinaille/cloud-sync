package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func Init(level, file string) *slog.Logger {
	var w io.Writer = os.Stdout
	if file != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			panic(fmt.Sprintf("logging: cannot create log dir %q: %v", filepath.Dir(file), err))
		}
		f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			panic(fmt.Sprintf("logging: cannot open log file %q: %v", file, err))
		}
		w = f
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
