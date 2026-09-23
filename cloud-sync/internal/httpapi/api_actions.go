package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"cloud-sync/internal/state"
	"cloud-sync/internal/watcher"
)

func (w *WebServer) handleRetry(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, st, _, ok := w.sup.Snapshot()
	if !ok {
		writeError(rw, http.StatusServiceUnavailable, "supervisor not running")
		return
	}

	var req retryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}

	records, err := listRecords(st)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	byKey := make(map[string]*state.StatusRecord, len(records))
	for _, rec := range records {
		byKey[rec.Key] = rec
	}

	targets := dedupeStrings(req.Keys)
	// State-batch selection runs only when `all` is set or no explicit keys
	// were supplied; when keys are given they take precedence over `state`.
	if (req.All || len(req.Keys) == 0) && req.State != "" {
		if req.State == "unsynced" || req.State == "all" {
			un, err := unsyncedFiles(cfg, st)
			if err != nil {
				writeError(rw, http.StatusInternalServerError, err.Error())
				return
			}
			for _, rec := range un {
				targets = append(targets, rec.Key)
			}
		}
		if req.State != "unsynced" {
			for _, rec := range records {
				if req.State == "all" || rec.Status == req.State {
					targets = append(targets, rec.Key)
				}
			}
		}
		targets = dedupeStrings(targets)
	}

	results := make([]retryResult, 0, len(targets))
	for _, key := range targets {
		rec := byKey[key]
		var src string
		var size int64
		if rec != nil {
			src = rec.SrcPath
			size = rec.SrcSize
		}
		if src == "" {
			src = resolveWatchPath(cfg.WatchDirs, key)
		}
		if err := st.Delete(key); err != nil {
			results = append(results, retryResult{Key: key, OK: false, Error: err.Error()})
			continue
		}
		if src == "" {
			results = append(results, retryResult{Key: key, OK: false, Error: "source missing"})
			continue
		}
		info, err := os.Stat(src)
		if err != nil {
			results = append(results, retryResult{Key: key, OK: false, Error: "source missing"})
			continue
		}
		if size == 0 {
			size = info.Size()
		}
		// ProcessOne tracks the upload by the generation WaitGroup when tasks
		// run, or runs it on a throwaway pipeline when paused so a retry works
		// without starting tasks. Either way it is drained on Stop, unlike a
		// bare goroutine that could write state after shutdown.
		if err := w.sup.ProcessOne(r.Context(), watcher.FileEvent{Path: src, Size: size, Detected: time.Now()}); err != nil {
			results = append(results, retryResult{Key: key, OK: false, Error: err.Error()})
			continue
		}
		results = append(results, retryResult{Key: key, OK: true})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"results": results})
}

func (w *WebServer) handleCleanupRun(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cl := w.sup.Cleanup()
	if cl == nil {
		writeError(rw, http.StatusServiceUnavailable, "supervisor not running")
		return
	}
	n, err := cl.TickNow(r.Context())
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]int{"processed": n})
}

// handleTasks starts or pauses the runtime tasks (watcher/pipeline/cleanup).
func (w *WebServer) handleTasks(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req tasksRequest
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.Enabled {
		if err := w.sup.Resume(r.Context()); err != nil {
			writeError(rw, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		w.sup.Pause()
	}
	writeJSON(rw, http.StatusOK, statusPayload(r.Context(), w.sup))
}

// handleCleanupFile cleans a single synced file on demand (honors dry-run).
func (w *WebServer) handleCleanupFile(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.Key == "" {
		writeError(rw, http.StatusBadRequest, "key is empty")
		return
	}
	cl := w.sup.Cleanup()
	if cl == nil {
		writeError(rw, http.StatusServiceUnavailable, "supervisor not running")
		return
	}
	dryRun, err := cl.CleanKey(r.Context(), req.Key)
	if err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "dry_run": dryRun})
}

// handleRescan reconciles "cleaned" records whose local file is still present.
func (w *WebServer) handleRescan(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cl := w.sup.Cleanup()
	if cl == nil {
		writeError(rw, http.StatusServiceUnavailable, "supervisor not running")
		return
	}
	rep, err := cl.Rescan(r.Context())
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, rep)
}
