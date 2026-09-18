package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/media"
	"cloud-sync/internal/state"
)

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
