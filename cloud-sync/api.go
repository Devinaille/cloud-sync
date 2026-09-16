package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPageSize = 50
	maxPageSize     = 500
	// pingTimeout bounds the OpenList connectivity probe in /api/status.
	pingTimeout = 3 * time.Second
)

// statusResponse is the payload for GET /api/status.
type statusResponse struct {
	OK            bool         `json:"ok"`
	Error         string       `json:"error,omitempty"`
	StartedAt     string       `json:"started_at,omitempty"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	OpenListPing  bool         `json:"openlist_ping"`
	WatchDirs     []string     `json:"watch_dirs,omitempty"`
	Counts        statusCounts `json:"counts"`
	Config        *configView  `json:"config,omitempty"`
}

type statusCounts struct {
	Synced   int `json:"synced"`
	Failed   int `json:"failed"`
	Cleaned  int `json:"cleaned"`
	Unsynced int `json:"unsynced"`
}

// configView is the effective config exposed by the API. openlist_token is
// deliberately absent.
type configView struct {
	OpenListURL       string `json:"openlist_url"`
	OpenListOverwrite bool   `json:"openlist_overwrite"`
	CleanupDryRun     bool   `json:"cleanup_dry_run"`
	UploadConcurrency int    `json:"upload_concurrency"`
	UIListen          string `json:"ui_listen"`
}

// fileItem is one row in GET /api/files.
type fileItem struct {
	Key        string `json:"key"`
	SrcPath    string `json:"src_path"`
	Size       int64  `json:"size"`
	State      string `json:"state"`
	CloudPath  string `json:"cloud_path"`
	SyncedAt   string `json:"synced_at"`
	CleanupAt  string `json:"cleanup_at"`
	RetryCount int    `json:"retry_count"`
	Error      string `json:"error"`
}

type filesResponse struct {
	Items    []fileItem `json:"items"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

type configPutResponse struct {
	OK     bool           `json:"ok"`
	Status statusResponse `json:"status"`
}

type retryRequest struct {
	Keys  []string `json:"keys"`
	State string   `json:"state"`
	All   bool     `json:"all"`
}

type retryResult struct {
	Key   string `json:"key"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// ---- handlers ----------------------------------------------------------

func (w *WebServer) handleStatus(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(rw, http.StatusOK, statusPayload(r.Context(), w.sup))
}

func (w *WebServer) handleFiles(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, st, _, ok := w.sup.Snapshot()
	if !ok {
		writeError(rw, http.StatusServiceUnavailable, "supervisor not running")
		return
	}

	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "all"
	}
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefault(q.Get("page_size"), defaultPageSize)
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	records, err := listRecords(st)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	items, err := buildFileItems(cfg, st, records)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}

	filtered := filterItems(items, state, q.Get("q"))
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Key < filtered[j].Key })

	resp := filesResponse{
		Items:    paginate(filtered, page, pageSize),
		Total:    len(filtered),
		Page:     page,
		PageSize: pageSize,
	}
	writeJSON(rw, http.StatusOK, resp)
}

func (w *WebServer) handleConfig(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(w.sup.ConfigPath())
		if err != nil {
			writeError(rw, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
			return
		}
		writeJSON(rw, http.StatusOK, map[string]string{"yaml": string(data)})
	case http.MethodPut:
		w.putConfig(rw, r)
	default:
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (w *WebServer) putConfig(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(rw, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeError(rw, http.StatusBadRequest, "yaml is empty")
		return
	}

	cfgPath := w.sup.ConfigPath()
	dir := filepath.Dir(cfgPath)
	tmp, err := os.CreateTemp(dir, "cloud-sync-*.yaml")
	if err != nil {
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("temp file: %v", err))
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(body.YAML); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("write temp: %v", err))
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("close temp: %v", err))
		return
	}

	// Validate by parsing the candidate file before replacing the live config.
	if _, err := Load(tmpName); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("chmod temp: %v", err))
		return
	}
	if err := os.Rename(tmpName, cfgPath); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("replace config: %v", err))
		return
	}

	if err := w.sup.Reload(r.Context()); err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, configPutResponse{OK: true, Status: statusPayload(r.Context(), w.sup)})
}

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
	byKey := make(map[string]*StatusRecord, len(records))
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
		// Enqueue on the supervisor so the upload is tracked by the generation
		// WaitGroup and cancelled on Stop/Reload; a bare goroutine would outlive
		// the generation and could write state after shutdown.
		if err := w.sup.Enqueue(FileEvent{Path: src, Size: size, Detected: time.Now()}); err != nil {
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

// ---- helpers -----------------------------------------------------------

func statusPayload(ctx context.Context, sup *Supervisor) statusResponse {
	cfg, st, _, ok := sup.Snapshot()
	if !ok {
		errMsg := sup.LastError()
		if errMsg == "" {
			errMsg = "supervisor not running"
		}
		return statusResponse{OK: false, Error: errMsg, Counts: statusCounts{}}
	}

	started := sup.StartedAt()
	uptime := int64(0)
	if !started.IsZero() {
		if d := time.Since(started); d > 0 {
			uptime = int64(d.Seconds())
		}
	}

	// Bound the connectivity probe: the client's own timeout is 30s and the UI
	// polls status every 5s, so an unbounded ping against a down OpenList would
	// stack requests. A timeout is reported as openlist_ping:false, not an error.
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	pingOK := sup.Ping(pingCtx) == nil
	cancel()

	resp := statusResponse{
		OK:            true,
		StartedAt:     started.UTC().Format(time.RFC3339),
		UptimeSeconds: uptime,
		OpenListPing:  pingOK,
		WatchDirs:     cfg.WatchDirs,
		Config: &configView{
			OpenListURL:       cfg.OpenListURL,
			OpenListOverwrite: cfg.OpenListOverwrite,
			CleanupDryRun:     cfg.CleanupDryRun,
			UploadConcurrency: cfg.UploadConcurrency,
			UIListen:          cfg.UIListen,
		},
	}

	records, err := listRecords(st)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		return resp
	}
	for _, rec := range records {
		switch rec.Status {
		case "synced":
			resp.Counts.Synced++
		case "failed":
			resp.Counts.Failed++
		case "cleaned":
			resp.Counts.Cleaned++
		}
	}
	un, err := unsyncedFiles(cfg, st)
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
		return resp
	}
	resp.Counts.Unsynced = len(un)
	return resp
}

// listRecords returns all persisted records (synced/failed/cleaned).
func listRecords(st *StateManager) ([]*StatusRecord, error) {
	return st.ListAll()
}

// buildFileItems merges persisted records with unsynced files found by walking
// the watch dirs. Record keys win over unsynced duplicates.
func buildFileItems(cfg *Config, st *StateManager, records []*StatusRecord) ([]fileItem, error) {
	items := make([]fileItem, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, rec := range records {
		items = append(items, recordToItem(rec))
		seen[rec.Key] = struct{}{}
	}
	un, err := unsyncedFiles(cfg, st)
	if err != nil {
		return nil, err
	}
	for _, rec := range un {
		if _, dup := seen[rec.Key]; dup {
			continue
		}
		items = append(items, recordToItem(rec))
	}
	return items, nil
}

func recordToItem(rec *StatusRecord) fileItem {
	item := fileItem{
		Key:        rec.Key,
		SrcPath:    rec.SrcPath,
		Size:       rec.SrcSize,
		State:      rec.Status,
		CloudPath:  rec.CloudPath,
		RetryCount: rec.RetryCount,
		Error:      rec.Error,
	}
	if !rec.SyncedAt.IsZero() {
		item.SyncedAt = rec.SyncedAt.UTC().Format(time.RFC3339)
	}
	if !rec.CleanupAt.IsZero() {
		item.CleanupAt = rec.CleanupAt.UTC().Format(time.RFC3339)
	}
	return item
}

// unsyncedFiles walks every watch dir for video files that pass shouldEmit and
// have no record yet. Returned records carry Key/SrcPath/SrcSize and a
// synthetic "unsynced" status.
func unsyncedFiles(cfg *Config, st *StateManager) ([]*StatusRecord, error) {
	var out []*StatusRecord
	for _, root := range cfg.WatchDirs {
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if info.IsDir() {
				return nil
			}
			if !shouldEmit(p, info.Size(), cfg.MinFileSize) {
				return nil
			}
			key, ok := keyForPath(cfg.WatchDirs, p)
			if !ok {
				return nil
			}
			synced, err := st.AlreadySynced(key)
			if err != nil {
				return nil
			}
			if synced {
				return nil
			}
			out = append(out, &StatusRecord{
				Key:     key,
				SrcPath: p,
				SrcSize: info.Size(),
				Status:  "unsynced",
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// keyForPath returns the slash-separated path relative to whichever watch root
// contains path.
func keyForPath(roots []string, path string) (string, bool) {
	for _, root := range roots {
		if !hasPrefix(path, root) {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}

// resolveWatchPath returns join(watchDir, key) for the first watch dir where
// the file exists, or "" when none match.
func resolveWatchPath(roots []string, key string) string {
	for _, root := range roots {
		candidate := filepath.Join(root, filepath.FromSlash(key))
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func filterItems(items []fileItem, state, query string) []fileItem {
	out := make([]fileItem, 0, len(items))
	q := strings.ToLower(strings.TrimSpace(query))
	for _, item := range items {
		if state != "all" && item.State != state {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(item.Key), q) &&
			!strings.Contains(strings.ToLower(item.SrcPath), q) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func paginate(items []fileItem, page, pageSize int) []fileItem {
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []fileItem{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func parseIntDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}
