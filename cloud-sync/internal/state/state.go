package state

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type StatusRecord struct {
	Key            string     `json:"-"` // rel-path under source root; used as disk key (not serialized)
	SrcPath        string     `json:"src_path"`
	SrcSize        int64      `json:"src_size"`
	SrcMtime       time.Time  `json:"src_mtime"`
	CloudPath      string     `json:"cloud_path"`
	OpenListTaskID string     `json:"openlist_task_id"`
	SyncedAt       time.Time  `json:"synced_at"`
	CleanupAt      time.Time  `json:"cleanup_at"`
	Status         string     `json:"status"` // "synced" | "failed" | "cleaned"
	RetryCount     int        `json:"retry_count"`
	CleanedAt      *time.Time `json:"cleaned_at,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type StateManager struct {
	root string
	log  *slog.Logger
	mu   sync.Mutex
}

func NewStateManager(root string, log *slog.Logger) *StateManager {
	return &StateManager{root: root, log: log}
}

func (s *StateManager) bucket(status string, t time.Time) string {
	date := t.UTC().Format("2006-01-02")
	if status == "failed" {
		return filepath.Join(s.root, "FAILED", date)
	}
	return filepath.Join(s.root, date)
}

func (s *StateManager) RecordPath(rec *StatusRecord) string {
	return filepath.Join(s.bucket(rec.Status, rec.SyncedAt), filepath.FromSlash(rec.Key)+".json")
}

func (s *StateManager) EnsureDirs() error {
	today := time.Now().UTC()
	for _, d := range []string{
		filepath.Join(s.root, today.Format("2006-01-02")),
		filepath.Join(s.root, "FAILED"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("state: mkdir %q: %w", d, err)
		}
	}
	return nil
}

// AlreadySynced returns true if any record (any status) exists for the given
// key, scanning both top-level date buckets and FAILED/<date>/ subdirectories.
func (s *StateManager) AlreadySynced(key string) (bool, error) {
	target := filepath.FromSlash(filepath.ToSlash(key) + ".json")
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "FAILED" {
			continue
		}
		found, err := fileExists(filepath.Join(s.root, e.Name(), target))
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	failedDir := filepath.Join(s.root, "FAILED")
	fEntries, err := os.ReadDir(failedDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, e := range fEntries {
		if !e.IsDir() {
			continue
		}
		found, err := fileExists(filepath.Join(failedDir, e.Name(), target))
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func fileExists(p string) (bool, error) {
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Write atomically writes rec at the path determined by its Key + SyncedAt + Status.
func (s *StateManager) Write(rec *StatusRecord) error {
	return s.atomicWrite(s.RecordPath(rec), rec)
}

// Update re-writes rec to its existing path (same Key/SyncedAt/Status).
func (s *StateManager) Update(rec *StatusRecord) error {
	return s.atomicWrite(s.RecordPath(rec), rec)
}

func (s *StateManager) atomicWrite(p string, rec *StatusRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ListForCleanup walks all date buckets (not FAILED/), parses each .json, and
// returns records where Status == "synced" and CleanupAt < now. Each returned
// record's Key is set to its path relative to the date bucket, so Update
// rewrites the same file in place.
func (s *StateManager) ListForCleanup(now time.Time, after time.Duration) ([]*StatusRecord, error) {
	var out []*StatusRecord
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "FAILED" || !looksLikeDate(e.Name()) {
			continue
		}
		base := filepath.Join(s.root, e.Name())
		walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			if !strings.HasSuffix(p, ".json") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				s.log.Warn("state: read failed", "path", p, "err", err)
				return nil
			}
			var rec StatusRecord
			if err := json.Unmarshal(b, &rec); err != nil {
				s.log.Warn("state: parse failed", "path", p, "err", err)
				return nil
			}
			if rec.Status == "synced" && !rec.SyncedAt.IsZero() && !now.Before(rec.SyncedAt.Add(after)) {
				rel, err := filepath.Rel(base, p)
				if err != nil {
					s.log.Warn("state: rel path failed", "path", p, "err", err)
					return nil
				}
				rec.Key = strings.TrimSuffix(filepath.ToSlash(rel), ".json")
				out = append(out, &rec)
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return out, nil
}

// Get returns the record for key, or (nil, nil) when none exists. It scans the
// date buckets and FAILED/<date>/.
func (s *StateManager) Get(key string) (*StatusRecord, error) {
	target := filepath.FromSlash(filepath.ToSlash(key) + ".json")
	var dirs []string
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && looksLikeDate(e.Name()) {
			dirs = append(dirs, filepath.Join(s.root, e.Name()))
		}
	}
	if fEntries, err := os.ReadDir(filepath.Join(s.root, "FAILED")); err == nil {
		for _, e := range fEntries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(s.root, "FAILED", e.Name()))
			}
		}
	}
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, target))
		if err != nil {
			continue
		}
		var rec StatusRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, err
		}
		rec.Key = key
		return &rec, nil
	}
	return nil, nil
}

// ListAll returns every record across all date buckets and FAILED/<date>/.
// Each returned record's Key is its path relative to the bucket root.
func (s *StateManager) ListAll() ([]*StatusRecord, error) {
	var out []*StatusRecord
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "FAILED" {
			failedDir := filepath.Join(s.root, "FAILED")
			fEntries, err := os.ReadDir(failedDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			for _, fe := range fEntries {
				if !fe.IsDir() {
					continue
				}
				if err := s.collectBucketRecords(filepath.Join(failedDir, fe.Name()), &out); err != nil {
					return nil, err
				}
			}
			continue
		}
		if !looksLikeDate(e.Name()) {
			continue
		}
		if err := s.collectBucketRecords(filepath.Join(s.root, e.Name()), &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *StateManager) collectBucketRecords(base string, out *[]*StatusRecord) error {
	return filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(p, ".json") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			s.log.Warn("state: read failed", "path", p, "err", err)
			return nil
		}
		var rec StatusRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			s.log.Warn("state: parse failed", "path", p, "err", err)
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			s.log.Warn("state: rel path failed", "path", p, "err", err)
			return nil
		}
		rec.Key = strings.TrimSuffix(filepath.ToSlash(rel), ".json")
		*out = append(*out, &rec)
		return nil
	})
}

// Delete removes key's record from every date bucket and FAILED/<date>/.
// Missing records are not an error. Empty parent dirs are removed best-effort.
func (s *StateManager) Delete(key string) error {
	key = filepath.ToSlash(key)
	if key == "" || strings.HasPrefix(key, "/") || key == ".." ||
		strings.HasPrefix(key, "../") || strings.Contains(key, "/../") {
		return fmt.Errorf("state: invalid key %q", key)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	target := filepath.FromSlash(key + ".json")
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "FAILED" {
			failedDir := filepath.Join(s.root, "FAILED")
			fEntries, err := os.ReadDir(failedDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			for _, fe := range fEntries {
				if !fe.IsDir() {
					continue
				}
				bucket := filepath.Join(failedDir, fe.Name())
				s.removeRecord(filepath.Join(bucket, target), bucket)
			}
			continue
		}
		bucket := filepath.Join(s.root, e.Name())
		s.removeRecord(filepath.Join(bucket, target), bucket)
	}
	return nil
}

func (s *StateManager) removeRecord(file, stop string) {
	if err := os.Remove(file); err != nil {
		if !os.IsNotExist(err) {
			s.log.Warn("state: delete failed", "path", file, "err", err)
		}
		return
	}
	removeEmptyParents(file, stop)
}

func removeEmptyParents(file, stop string) {
	dir := filepath.Dir(file)
	for dir != stop && dir != "." && dir != string(filepath.Separator) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func looksLikeDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}
