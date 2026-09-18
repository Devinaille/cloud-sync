package httpapi

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cloud-sync/internal/supervisor"
)

// webFS holds the embedded single-page UI. The web/ directory contains
// index.html, app.js and style.css; Task 5 replaces the placeholders with the
// full dashboard.
//
//go:embed web
var webFS embed.FS

// WebServer serves the embedded UI plus the JSON API. It holds a reference to
// the Supervisor so handlers can snapshot the current generation.
type WebServer struct {
	sup *supervisor.Supervisor
	log *slog.Logger
}

func NewWebServer(sup *supervisor.Supervisor, log *slog.Logger) *WebServer {
	return &WebServer{sup: sup, log: log}
}

// Handler builds the HTTP mux. It is exported separately from Serve so tests
// can drive it with httptest.
func (w *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", w.handleStatus)
	mux.HandleFunc("/api/files", w.handleFiles)
	mux.HandleFunc("/api/config", w.handleConfig)
	mux.HandleFunc("/api/retry", w.handleRetry)
	mux.HandleFunc("/api/cleanup/run", w.handleCleanupRun)
	mux.HandleFunc("/api/tasks", w.handleTasks)
	mux.HandleFunc("/api/precheck", w.handlePrecheck)

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		w.log.Error("web: embedded assets unavailable", "err", err)
		return mux
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(sub, name); err != nil {
			http.NotFound(rw, r)
			return
		}
		fileServer.ServeHTTP(rw, r)
	})
	return mux
}

// Serve starts an HTTP server on addr and blocks until ctx is cancelled or the
// server fails. It shuts down gracefully when ctx is done.
func (w *WebServer) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: w.Handler()}

	errCh := make(chan error, 1)
	go func() {
		w.log.Info("web ui listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
