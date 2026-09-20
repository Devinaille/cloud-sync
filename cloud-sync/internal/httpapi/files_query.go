package httpapi

import (
	"os"
	"path"
	"path/filepath"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/media"
	"cloud-sync/internal/pipeline"
	"cloud-sync/internal/state"
)

// inflightOf returns the current in-progress uploads, or nil when no pipeline
// is active.
func inflightOf(pl *pipeline.Pipeline) map[string]float64 {
	if pl == nil {
		return nil
	}
	return pl.Inflight()
}

// applyInflight marks items whose key is currently uploading as "syncing" and
// attaches their percent complete.
func applyInflight(items []fileItem, inflight map[string]float64) {
	if len(inflight) == 0 {
		return
	}
	for i := range items {
		if pct, ok := inflight[items[i].Key]; ok {
			items[i].State = "syncing"
			items[i].Progress = pct
		}
	}
}

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
func cloudPathFor(cfg *config.Config, key string) string {
	p := cfg.DstStorage + "/media"
	if parent := path.Dir(key); parent != "." && parent != "" {
		p += "/" + parent
	}
	return p + "/" + path.Base(key)
}

// writePrecheck persists a report atomically under the status dir. The file
// lives beside the date buckets and is ignored by record scanning.
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
