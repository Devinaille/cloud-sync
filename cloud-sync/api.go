package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud-sync/internal/buildinfo"
	"cloud-sync/internal/config"
	"cloud-sync/internal/media"
	"cloud-sync/internal/state"
	"cloud-sync/internal/watcher"
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
	Version       string       `json:"version"`
	Commit        string       `json:"commit"`
	BuildTime     string       `json:"build_time"`
	StartedAt     string       `json:"started_at,omitempty"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	OpenListPing  bool         `json:"openlist_ping"`
	TasksEnabled  bool         `json:"tasks_enabled"`
	TasksRunning  bool         `json:"tasks_running"`
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
	TasksEnabled      bool   `json:"tasks_enabled"`
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
	// Cloud is the pre-check's cloud-existence result for this file:
	// "exists" | "missing" | "unknown" | "" (not checked yet).
	Cloud string `json:"cloud"`
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

type tasksRequest struct {
	Enabled bool `json:"enabled"`
}

type precheckItem struct {
	Key     string `json:"key"`
	SrcPath string `json:"src_path"`
	Size    int64  `json:"size"`
	// Cloud is the OpenList-side existence check: "exists" | "missing" | "unknown".
	Cloud string `json:"cloud"`
}

// precheckReport is a read-only dry run: which files would be uploaded if tasks
// were started, and whether each candidate already exists on the cloud. It is
// persisted to <SyncStatusDir>/precheck.json.
type precheckReport struct {
	GeneratedAt     string         `json:"generated_at"`
	WatchDirs       []string       `json:"watch_dirs"`
	MinFileSize     int64          `json:"min_file_size"`
	Scanned         int            `json:"scanned"`
	SkippedExt      int            `json:"skipped_ext"`
	TooSmall        int            `json:"too_small"`
	AlreadySynced   int            `json:"already_synced"`
	Candidates      []precheckItem `json:"candidates"`
	CandidatesTotal int            `json:"candidates_total"`
	CandidatesBytes int64          `json:"candidates_bytes"`
	// CloudChecked is false when the OpenList existence probe failed (see
	// CloudError); per-candidate Cloud is then "unknown".
	CloudChecked bool   `json:"cloud_checked"`
	CloudError   string `json:"cloud_error,omitempty"`
	CloudExists  int    `json:"cloud_exists"`
	CloudMissing int    `json:"cloud_missing"`
	CloudUnknown int    `json:"cloud_unknown"`
}

const precheckFileName = "precheck.json"

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
	applyCloudStatus(cfg, items)

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
	if _, err := config.Load(tmpName); err != nil {
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

// handlePrecheck runs (POST) or returns (GET) the upload pre-check report. It
// only reads the filesystem and writes the report file, so it is allowed while
// tasks are paused.
func (w *WebServer) handlePrecheck(rw http.ResponseWriter, r *http.Request) {
	cfg, st, _, ok := w.sup.Snapshot()
	if !ok {
		writeError(rw, http.StatusServiceUnavailable, "state not initialized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(filepath.Join(cfg.SyncStatusDir, precheckFileName))
		if err != nil {
			if os.IsNotExist(err) {
				writeError(rw, http.StatusNotFound, "no pre-check report yet")
				return
			}
			writeError(rw, http.StatusInternalServerError, err.Error())
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write(data)
	case http.MethodPost:
		rep, err := runPrecheck(r.Context(), cfg, st, w.sup.CloudExists)
		if err != nil {
			writeError(rw, http.StatusInternalServerError, err.Error())
			return
		}
		if err := writePrecheck(cfg.SyncStatusDir, rep); err != nil {
			// The report is best-effort; still return it to the caller.
			w.log.Warn("precheck: persist report failed", "err", err)
		}
		writeJSON(rw, http.StatusOK, rep)
	default:
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---- helpers -----------------------------------------------------------

func statusPayload(ctx context.Context, sup *Supervisor) statusResponse {
	cfg, st, _, ok := sup.Snapshot()
	if !ok {
		errMsg := sup.LastError()
		if errMsg == "" {
			errMsg = "supervisor not running"
		}
		return statusResponse{
			OK:           false,
			Error:        errMsg,
			Version:      buildinfo.Version,
			Commit:       buildinfo.Commit,
			BuildTime:    buildinfo.BuildTime,
			Counts:       statusCounts{},
			TasksEnabled: sup.TasksEnabled(),
			TasksRunning: sup.TasksRunning(),
		}
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
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit,
		BuildTime:     buildinfo.BuildTime,
		UptimeSeconds: uptime,
		OpenListPing:  pingOK,
		TasksEnabled:  sup.TasksEnabled(),
		TasksRunning:  sup.TasksRunning(),
		WatchDirs:     cfg.WatchDirs,
		Config: &configView{
			OpenListURL:       cfg.OpenListURL,
			OpenListOverwrite: cfg.OpenListOverwrite,
			CleanupDryRun:     cfg.CleanupDryRun,
			UploadConcurrency: cfg.UploadConcurrency,
			UIListen:          cfg.UIListen,
			TasksEnabled:      sup.TasksEnabled(),
		},
	}
	if !started.IsZero() {
		resp.StartedAt = started.UTC().Format(time.RFC3339)
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
func listRecords(st *state.StateManager) ([]*state.StatusRecord, error) {
	return st.ListAll()
}

// buildFileItems merges persisted records with unsynced files found by walking
// the watch dirs. Record keys win over unsynced duplicates.
func buildFileItems(cfg *config.Config, st *state.StateManager, records []*state.StatusRecord) ([]fileItem, error) {
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

func recordToItem(rec *state.StatusRecord) fileItem {
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

// unsyncedFiles walks every watch dir for video files that pass media.ShouldEmit and
// have no record yet. Returned records carry Key/SrcPath/SrcSize and a
// synthetic "unsynced" status.
func unsyncedFiles(cfg *config.Config, st *state.StateManager) ([]*state.StatusRecord, error) {
	var out []*state.StatusRecord
	for _, root := range cfg.WatchDirs {
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if info.IsDir() {
				return nil
			}
			if !media.ShouldEmit(p, info.Size(), cfg.MinFileSize) {
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
			out = append(out, &state.StatusRecord{
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

// runPrecheck walks the watch dirs and reports which files would be uploaded if
// tasks were running, then probes OpenList to see whether each candidate already
// exists on the cloud. It never touches sync status records.
//
// cloudExists reports cloud-side existence; a transport error marks the
// candidate "unknown" (and CloudChecked=false) rather than failing the report.
func runPrecheck(ctx context.Context, cfg *config.Config, st *state.StateManager, cloudExists func(context.Context, string) (bool, error)) (*precheckReport, error) {
	rep := &precheckReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		WatchDirs:   cfg.WatchDirs,
		MinFileSize: cfg.MinFileSize,
		Candidates:  []precheckItem{},
	}
	for _, root := range cfg.WatchDirs {
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if info.IsDir() {
				return nil
			}
			rep.Scanned++
			if !media.IsVideoExt(p) {
				rep.SkippedExt++
				return nil
			}
			if info.Size() <= cfg.MinFileSize {
				rep.TooSmall++
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
				rep.AlreadySynced++
				return nil
			}
			rep.Candidates = append(rep.Candidates, precheckItem{Key: key, SrcPath: p, Size: info.Size()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(rep.Candidates, func(i, j int) bool { return rep.Candidates[i].Key < rep.Candidates[j].Key })

	rep.CandidatesTotal = len(rep.Candidates)
	rep.CloudChecked = cloudExists != nil
	for i := range rep.Candidates {
		c := &rep.Candidates[i]
		rep.CandidatesBytes += c.Size
		if cloudExists == nil {
			c.Cloud = "unknown"
			rep.CloudUnknown++
			continue
		}
		exists, err := cloudExists(ctx, cloudPathFor(cfg, c.Key))
		switch {
		case err != nil:
			c.Cloud = "unknown"
			rep.CloudUnknown++
			rep.CloudChecked = false
			if rep.CloudError == "" {
				rep.CloudError = err.Error()
			}
		case exists:
			c.Cloud = "exists"
			rep.CloudExists++
		default:
			c.Cloud = "missing"
			rep.CloudMissing++
		}
	}
	return rep, nil
}

// cloudPathFor maps a watch-relative key to its OpenList path, mirroring the
// pipeline's layout: <DstStorage>/media/<rel>.
func cloudPathFor(cfg *config.Config, key string) string {
	p := cfg.DstStorage + "/media"
	if parent := path.Dir(key); parent != "." && parent != "" {
		p += "/" + parent
	}
	return p + "/" + path.Base(key)
}

// writePrecheck persists a report atomically under the status dir. The file
// lives beside the date buckets and is ignored by record scanning.
func writePrecheck(dir string, rep *precheckReport) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, precheckFileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, precheckFileName))
}

// readPrecheck loads the persisted pre-check report, or (nil, nil) when none.
func readPrecheck(dir string) (*precheckReport, error) {
	data, err := os.ReadFile(filepath.Join(dir, precheckFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rep precheckReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// applyCloudStatus fills each item's Cloud field: synced records are known to
// exist on the cloud; other files use the last pre-check's result when present.
func applyCloudStatus(cfg *config.Config, items []fileItem) {
	byKey := map[string]string{}
	if rep, err := readPrecheck(cfg.SyncStatusDir); err == nil && rep != nil {
		for _, c := range rep.Candidates {
			byKey[c.Key] = c.Cloud
		}
	}
	for i := range items {
		switch {
		case items[i].State == "synced":
			items[i].Cloud = "exists"
		case byKey[items[i].Key] != "":
			items[i].Cloud = byKey[items[i].Key]
		default:
			items[i].Cloud = ""
		}
	}
}

// keyForPath returns the slash-separated path relative to whichever watch root
// contains path.
func keyForPath(roots []string, path string) (string, bool) {
	for _, root := range roots {
		if !media.HasPrefix(path, root) {
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
