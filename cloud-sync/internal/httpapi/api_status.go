package httpapi

import (
	"context"
	"net/http"
	"time"

	"cloud-sync/internal/buildinfo"
	"cloud-sync/internal/state"
	"cloud-sync/internal/supervisor"
)

func (w *WebServer) handleStatus(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(rw, http.StatusOK, statusPayload(r.Context(), w.sup))
}

func statusPayload(ctx context.Context, sup *supervisor.Supervisor) statusResponse {
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
