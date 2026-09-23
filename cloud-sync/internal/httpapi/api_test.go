package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud-sync/internal/config"
	"cloud-sync/internal/mockopenlist"
	"cloud-sync/internal/pipeline"
	"cloud-sync/internal/state"
	"cloud-sync/internal/supervisor"
	"cloud-sync/internal/testutil"
)

// mockUploaderFactory returns a Supervisor uploader factory backed by a fresh
// in-memory mock, so tests never touch a real OpenList.
func mockUploaderFactory() func(cfg *config.Config, log *slog.Logger) pipeline.Uploader {
	return func(cfg *config.Config, log *slog.Logger) pipeline.Uploader { return mockopenlist.New() }
}

// apiTestEnv bundles a started Supervisor and its temp dirs for API tests.
type apiTestEnv struct {
	sup     *supervisor.Supervisor
	up      *mockopenlist.Uploader
	watch   string
	syncDir string
	cfgPath string
	dir     string
}

// newTestSupervisor builds a valid on-disk config, loads it, and starts a
// Supervisor backed by a mock uploader. Test-tuned knobs (small min size, fast
// stabilize/poll, cleanup dry-run) keep the tests quick and non-destructive.
func newTestSupervisor(t *testing.T) *apiTestEnv {
	t.Helper()
	testutil.PreserveLoadEnv(t)

	dir := t.TempDir()
	watch := filepath.Join(dir, "media")
	syncDir := filepath.Join(dir, ".sync_status")
	for _, d := range []string{watch, syncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "cloud-sync.yaml")
	testutil.WriteConfigYAML(t, cfgPath, watch, syncDir, 2)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.MinFileSize = 1024
	cfg.StabilizeWait = 20 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond
	cfg.CleanupDryRun = true

	up := mockopenlist.New()
	sup := supervisor.NewSupervisor(cfgPath, cfg, testutil.TestLogger(), supervisor.SupervisorDeps{
		NewUploader: func(c *config.Config, l *slog.Logger) pipeline.Uploader { return up },
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sup.Stop)

	// Let the startup scan finish before tests touch the watch dirs; otherwise
	// a file written immediately after Start could be picked up by the scan.
	time.Sleep(50 * time.Millisecond)

	return &apiTestEnv{sup: sup, up: up, watch: watch, syncDir: syncDir, cfgPath: cfgPath, dir: dir}
}

func (e *apiTestEnv) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewWebServer(e.sup, testutil.TestLogger()).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func doJSON(t *testing.T, method, url string, body any, v any) int {
	t.Helper()
	var buf io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode %s %s: %v", method, url, err)
		}
	}
	return resp.StatusCode
}

func TestAPI_Status_OK(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	var body struct {
		OK     bool           `json:"ok"`
		Counts map[string]int `json:"counts"`
		Config map[string]any `json:"config"`
	}
	if code := getJSON(t, srv.URL+"/api/status", &body); code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", code)
	}
	if !body.OK {
		t.Errorf("ok = false, want true")
	}
	for _, k := range []string{"synced", "failed", "cleaned", "unsynced", "syncing", "processed"} {
		if _, ok := body.Counts[k]; !ok {
			t.Errorf("counts missing %q", k)
		}
	}
	if _, ok := body.Config["openlist_url"]; !ok {
		t.Errorf("config missing openlist_url: %#v", body.Config)
	}
	if _, ok := body.Config["openlist_token"]; ok {
		t.Errorf("config leaked openlist_token: %#v", body.Config)
	}
}

func TestAPI_Status_Degraded(t *testing.T) {
	cfg := testutil.TestConfig(t)
	sup := supervisor.NewSupervisor("", cfg, testutil.TestLogger(), supervisor.SupervisorDeps{NewUploader: mockUploaderFactory()})
	srv := httptest.NewServer(NewWebServer(sup, testutil.TestLogger()).Handler())
	defer srv.Close()

	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if code := getJSON(t, srv.URL+"/api/status", &body); code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", code)
	}
	if body.OK {
		t.Errorf("ok = true, want false for unstarted supervisor")
	}
	if body.Error == "" {
		t.Errorf("error is empty, want non-empty")
	}
}

func TestAPI_Files_FiltersAndPaginates(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no active generation")
	}
	now := time.Now().UTC().Truncate(time.Second)
	seed := func(key, status string) {
		rec := &state.StatusRecord{
			Key:       key,
			SrcPath:   filepath.Join(env.watch, key),
			SrcSize:   2048,
			SyncedAt:  now,
			CleanupAt: now.Add(72 * time.Hour),
			Status:    status,
		}
		if err := st.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	seed("A.mkv", "synced")
	seed("B.mkv", "failed")
	seed("C.mkv", "cleaned")

	srv := env.server(t)

	var synced filesResponse
	if code := getJSON(t, srv.URL+"/api/files?state=synced", &synced); code != http.StatusOK {
		t.Fatalf("state=synced code = %d", code)
	}
	if synced.Total != 1 || len(synced.Items) != 1 {
		t.Fatalf("state=synced total=%d items=%d, want 1/1", synced.Total, len(synced.Items))
	}
	if synced.Items[0].Key != "A.mkv" || synced.Items[0].State != "synced" {
		t.Errorf("state=synced item = %+v", synced.Items[0])
	}

	var paged filesResponse
	if code := getJSON(t, srv.URL+"/api/files?page_size=1&page=2", &paged); code != http.StatusOK {
		t.Fatalf("paged code = %d", code)
	}
	if paged.Total != 3 || paged.Page != 2 || paged.PageSize != 1 {
		t.Errorf("paging meta = total:%d page:%d size:%d, want 3/2/1", paged.Total, paged.Page, paged.PageSize)
	}
	if len(paged.Items) != 1 || paged.Items[0].Key != "B.mkv" {
		t.Errorf("page 2 items = %+v, want [B.mkv]", paged.Items)
	}
}

func TestAPI_Files_Unsynced(t *testing.T) {
	env := newTestSupervisor(t)
	// Pause so the watcher/scan does not claim the file; we want to observe it
	// as plain "unsynced".
	env.sup.Pause()

	if err := os.MkdirAll(filepath.Join(env.watch, "Fresh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.watch, "Fresh", "New.mkv"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := env.server(t)
	var body filesResponse
	if code := getJSON(t, srv.URL+"/api/files?state=unsynced", &body); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	var found *fileItem
	for i := range body.Items {
		if body.Items[i].Key == "Fresh/New.mkv" {
			found = &body.Items[i]
		}
	}
	if found == nil {
		t.Fatalf("unsynced item Fresh/New.mkv not found in %+v", body.Items)
	}
	if found.State != "unsynced" {
		t.Errorf("state = %q, want unsynced", found.State)
	}
	if found.Size != 4096 {
		t.Errorf("size = %d, want 4096", found.Size)
	}
}

func TestAPI_Config_PutValidReloads(t *testing.T) {
	env := newTestSupervisor(t)
	orig, err := os.ReadFile(env.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	newYAML := strings.Replace(string(orig), "upload_concurrency: 2", "upload_concurrency: 7", 1)
	if newYAML == string(orig) {
		t.Fatal("failed to change upload_concurrency in YAML")
	}

	srv := env.server(t)
	var putResp configPutResponse
	if code := doJSON(t, http.MethodPut, srv.URL+"/api/config", map[string]string{"yaml": newYAML}, &putResp); code != http.StatusOK {
		t.Fatalf("PUT code = %d, want 200", code)
	}
	if !putResp.OK {
		t.Errorf("PUT ok = false")
	}

	var cfgResp struct {
		YAML string `json:"yaml"`
	}
	getJSON(t, srv.URL+"/api/config", &cfgResp)
	if !strings.Contains(cfgResp.YAML, "upload_concurrency: 7") {
		t.Errorf("GET /api/config did not reflect update:\n%s", cfgResp.YAML)
	}

	var status struct {
		Config *configView `json:"config"`
	}
	getJSON(t, srv.URL+"/api/status", &status)
	if status.Config == nil || status.Config.UploadConcurrency != 7 {
		t.Errorf("status config = %+v, want upload_concurrency 7", status.Config)
	}
}

func TestAPI_Config_PutInvalidRejected(t *testing.T) {
	env := newTestSupervisor(t)
	before, err := os.ReadFile(env.cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	srv := env.server(t)
	code := doJSON(t, http.MethodPut, srv.URL+"/api/config",
		map[string]string{"yaml": "openlist_url: [unterminated\n"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("PUT invalid code = %d, want 400", code)
	}

	after, err := os.ReadFile(env.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("config file changed after invalid PUT")
	}
}

func TestAPI_Retry_DeletesAndEnqueues(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no active generation")
	}

	// Create the source in a new subdir so the watcher does not pre-empt retry.
	sub := filepath.Join(env.watch, "Movies")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(sub, "R.mkv")
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	key := "Movies/R.mkv"
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.Write(&state.StatusRecord{
		Key: key, SrcPath: src, SrcSize: 4096,
		SyncedAt: now, CleanupAt: now.Add(72 * time.Hour), Status: "failed",
	}); err != nil {
		t.Fatal(err)
	}

	srv := env.server(t)
	var resp struct {
		Results []retryResult `json:"results"`
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/retry", map[string]any{"keys": []string{key}}, &resp); code != http.StatusOK {
		t.Fatalf("retry code = %d, want 200", code)
	}
	if len(resp.Results) != 1 || !resp.Results[0].OK {
		t.Fatalf("retry results = %+v, want one ok", resp.Results)
	}

	// The failed record is deleted synchronously by the handler. (A retried
	// attempt may later write a fresh "synced" record, so assert on "failed".)
	recs, err := st.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.Key == key && rec.Status == "failed" {
			t.Errorf("failed record %q still present after retry", key)
		}
	}

	// The upload is enqueued asynchronously and tracked by the generation
	// WaitGroup. Poll until the mock observes the Copy; sup.Stop (registered by
	// newTestSupervisor's t.Cleanup) then drains the goroutine before the test
	// temp dirs are removed.
	deadline := time.Now().Add(2 * time.Second)
	for {
		env.up.Mu.Lock()
		n := len(env.up.CopyCalls)
		env.up.Mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mock uploader received no Copy call after retry")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAPI_Retry_WhilePaused(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no active generation")
	}
	srv := env.server(t)

	// Pause first so the retry must use the supervisor's one-off path.
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/tasks", map[string]bool{"enabled": false}, nil); code != http.StatusOK {
		t.Fatalf("pause code = %d, want 200", code)
	}

	sub := filepath.Join(env.watch, "Movies")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(sub, "P.mkv")
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	key := "Movies/P.mkv"
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.Write(&state.StatusRecord{
		Key: key, SrcPath: src, SrcSize: 4096,
		SyncedAt: now, CleanupAt: now.Add(72 * time.Hour), Status: "failed",
	}); err != nil {
		t.Fatal(err)
	}

	var resp struct {
		Results []retryResult `json:"results"`
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/retry", map[string]any{"keys": []string{key}}, &resp); code != http.StatusOK {
		t.Fatalf("retry while paused = %d, want 200", code)
	}
	if len(resp.Results) != 1 || !resp.Results[0].OK {
		t.Fatalf("retry results = %+v, want one ok", resp.Results)
	}

	// The one-off pipeline uploads and writes a fresh "synced" record without
	// tasks ever being started; Stop (t.Cleanup) drains it before temp dirs go.
	deadline := time.Now().Add(3 * time.Second)
	for {
		recs, err := st.ListAll()
		if err != nil {
			t.Fatal(err)
		}
		synced := false
		for _, rec := range recs {
			if rec.Key == key && rec.Status == "synced" {
				synced = true
			}
		}
		if synced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("paused retry did not write a synced record")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAPI_CleanupRun(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no active generation")
	}

	past := time.Now().UTC().Add(-100 * time.Hour)
	if err := st.Write(&state.StatusRecord{
		Key: "Due.mkv", SrcPath: filepath.Join(env.watch, "Due.mkv"), SrcSize: 2048,
		SyncedAt: past, CleanupAt: past.Add(time.Hour), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}

	srv := env.server(t)
	var resp struct {
		Processed int `json:"processed"`
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/cleanup/run", nil, &resp); code != http.StatusOK {
		t.Fatalf("cleanup code = %d, want 200", code)
	}
	if resp.Processed < 1 {
		t.Errorf("processed = %d, want >= 1", resp.Processed)
	}
}

func TestAPI_Status_TaskFields(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	var body struct {
		TasksEnabled bool `json:"tasks_enabled"`
		TasksRunning bool `json:"tasks_running"`
	}
	if code := getJSON(t, srv.URL+"/api/status", &body); code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", code)
	}
	if !body.TasksEnabled || !body.TasksRunning {
		t.Errorf("tasks_enabled=%v tasks_running=%v, want true/true", body.TasksEnabled, body.TasksRunning)
	}
}

func TestAPI_Tasks_PauseResumeAndGuards(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	// Pause.
	var paused statusResponse
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/tasks", map[string]bool{"enabled": false}, &paused); code != http.StatusOK {
		t.Fatalf("tasks pause code = %d, want 200", code)
	}
	if paused.TasksRunning || paused.TasksEnabled {
		t.Errorf("after pause: enabled=%v running=%v, want false/false", paused.TasksEnabled, paused.TasksRunning)
	}

	// Retry and cleanup run on the one-off path, so they are allowed while
	// paused (an empty/missing source still returns 200 with per-key results).
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/retry", map[string]any{"keys": []string{"Movies/X.mkv"}}, nil); code != http.StatusOK {
		t.Errorf("retry while paused = %d, want 200", code)
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/cleanup/run", map[string]any{}, nil); code != http.StatusOK {
		t.Errorf("cleanup while paused = %d, want 200", code)
	}

	// Resume.
	var resumed statusResponse
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/tasks", map[string]bool{"enabled": true}, &resumed); code != http.StatusOK {
		t.Fatalf("tasks resume code = %d, want 200", code)
	}
	if !resumed.TasksRunning || !resumed.TasksEnabled {
		t.Errorf("after resume: enabled=%v running=%v, want true/true", resumed.TasksEnabled, resumed.TasksRunning)
	}
	// Retry is no longer blocked (empty request → 200 with no results).
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/retry", map[string]any{}, nil); code != http.StatusOK {
		t.Errorf("retry after resume = %d, want 200", code)
	}
}

func TestAPI_Precheck_WhilePaused(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	// Pause so the watcher does not consume the file we are about to add.
	doJSON(t, http.MethodPost, srv.URL+"/api/tasks", map[string]bool{"enabled": false}, nil)

	if err := os.MkdirAll(filepath.Join(env.watch, "Movies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.watch, "Movies", "A.mkv"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	var rep precheckReport
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/precheck", map[string]any{}, &rep); code != http.StatusOK {
		t.Fatalf("precheck POST = %d, want 200", code)
	}
	if rep.CandidatesTotal != 1 {
		t.Fatalf("candidates_total = %d, want 1 (%+v)", rep.CandidatesTotal, rep.Candidates)
	}
	if rep.Candidates[0].Key != "Movies/A.mkv" {
		t.Errorf("candidate key = %q, want Movies/A.mkv", rep.Candidates[0].Key)
	}
	if rep.CandidatesBytes != 2048 {
		t.Errorf("candidates_bytes = %d, want 2048", rep.CandidatesBytes)
	}
	if _, err := os.Stat(filepath.Join(env.syncDir, precheckFileName)); err != nil {
		t.Errorf("precheck report not persisted: %v", err)
	}
	// The mock reports nothing on the cloud, so the candidate is "missing".
	if rep.Candidates[0].Cloud != "missing" {
		t.Errorf("cloud = %q, want missing", rep.Candidates[0].Cloud)
	}
	if !rep.CloudChecked || rep.CloudMissing != 1 {
		t.Errorf("cloud totals: checked=%v missing=%d, want true/1", rep.CloudChecked, rep.CloudMissing)
	}

	// GET returns the persisted report.
	var got precheckReport
	if code := getJSON(t, srv.URL+"/api/precheck", &got); code != http.StatusOK {
		t.Fatalf("precheck GET = %d, want 200", code)
	}
	if got.CandidatesTotal != 1 {
		t.Errorf("GET candidates_total = %d, want 1", got.CandidatesTotal)
	}
}

func TestAPI_Precheck_GetWithoutReport(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)
	if code := getJSON(t, srv.URL+"/api/precheck", nil); code != http.StatusNotFound {
		t.Errorf("precheck GET without report = %d, want 404", code)
	}
}

func TestAPI_Precheck_CloudExists(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)
	doJSON(t, http.MethodPost, srv.URL+"/api/tasks", map[string]bool{"enabled": false}, nil)

	if err := os.MkdirAll(filepath.Join(env.watch, "Movies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.watch, "Movies", "A.mkv"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pretend the file already exists on the cloud.
	env.up.CloudExists = map[string]bool{"/139yun_media/media/Movies/A.mkv": true}

	var rep precheckReport
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/precheck", map[string]any{}, &rep); code != http.StatusOK {
		t.Fatalf("precheck POST = %d, want 200", code)
	}
	if rep.CandidatesTotal != 1 {
		t.Fatalf("candidates_total = %d, want 1", rep.CandidatesTotal)
	}
	if rep.Candidates[0].Cloud != "exists" {
		t.Errorf("cloud = %q, want exists", rep.Candidates[0].Cloud)
	}
	if !rep.CloudChecked || rep.CloudExists != 1 || rep.CloudMissing != 0 {
		t.Errorf("cloud totals: checked=%v exists=%d missing=%d, want true/1/0",
			rep.CloudChecked, rep.CloudExists, rep.CloudMissing)
	}

	// /api/files must reflect the pre-check result in each item's Cloud field.
	var files filesResponse
	getJSON(t, srv.URL+"/api/files?state=all", &files)
	found := false
	for _, it := range files.Items {
		if it.Key == "Movies/A.mkv" {
			found = true
			if it.Cloud != "exists" {
				t.Errorf("file cloud = %q, want exists", it.Cloud)
			}
		}
	}
	if !found {
		t.Error("Movies/A.mkv not present in /api/files")
	}
}

func TestAPI_Status_BuildInfo(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	var body struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildTime string `json:"build_time"`
	}
	if code := getJSON(t, srv.URL+"/api/status", &body); code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", code)
	}
	if body.Version == "" {
		t.Error("version empty in /api/status")
	}
	if body.Commit == "" {
		t.Error("commit empty in /api/status")
	}
	if body.BuildTime == "" {
		t.Error("build_time empty in /api/status")
	}
}

func TestApplyInflight(t *testing.T) {
	items := []fileItem{
		{Key: "A", State: "unsynced"},
		{Key: "B", State: "synced"},
		{Key: "C", State: "failed"},
	}
	applyInflight(items, map[string]float64{"A": 42.5, "C": 7})

	if items[0].State != "syncing" || items[0].Progress != 42.5 {
		t.Errorf("A = %q/%.1f, want syncing/42.5", items[0].State, items[0].Progress)
	}
	if items[1].State != "synced" || items[1].Progress != 0 {
		t.Errorf("B = %q/%.1f, want unchanged synced/0", items[1].State, items[1].Progress)
	}
	if items[2].State != "syncing" || items[2].Progress != 7 {
		t.Errorf("C = %q/%.1f, want syncing/7", items[2].State, items[2].Progress)
	}
}

func TestAPI_ConfigForm_Get(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	var body struct {
		Values           config.FormValues `json:"values"`
		TokenSet         bool              `json:"token_set"`
		ConfigPath       string            `json:"config_path"`
		MinFileSizeBytes int64             `json:"min_file_size_bytes"`
		RestartFields    []string          `json:"restart_fields"`
		TasksRunning     bool              `json:"tasks_running"`
		TasksEnabled     bool              `json:"tasks_enabled"`
	}
	if code := getJSON(t, srv.URL+"/api/config/form", &body); code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if !body.TasksEnabled || !body.TasksRunning {
		t.Errorf("runtime tasks = enabled:%v running:%v, want both true (newTestSupervisor starts them)", body.TasksEnabled, body.TasksRunning)
	}
	if body.Values.OpenListURL == "" || len(body.Values.WatchDirs) == 0 {
		t.Errorf("values incomplete: %+v", body.Values)
	}
	if body.Values.OpenListToken != "" {
		t.Errorf("token must not be returned, got %q", body.Values.OpenListToken)
	}
	if !body.TokenSet {
		t.Error("token_set = false, want true")
	}
	if body.ConfigPath != env.cfgPath {
		t.Errorf("config_path = %q, want %q", body.ConfigPath, env.cfgPath)
	}
	if body.MinFileSizeBytes <= 0 {
		t.Errorf("min_file_size_bytes = %d, want > 0", body.MinFileSizeBytes)
	}
	found := false
	for _, f := range body.RestartFields {
		if f == "ui_listen" {
			found = true
		}
	}
	if !found {
		t.Errorf("restart_fields = %v, want to include ui_listen", body.RestartFields)
	}
}

func TestAPI_ConfigForm_PutUpdatesAndKeepsToken(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	var form struct {
		Values config.FormValues `json:"values"`
	}
	if code := getJSON(t, srv.URL+"/api/config/form", &form); code != http.StatusOK {
		t.Fatalf("GET form code = %d", code)
	}
	form.Values.UploadConcurrency = 5
	form.Values.OpenListToken = "" // keep existing

	var putResp configPutResponse
	if code := doJSON(t, http.MethodPut, srv.URL+"/api/config/form",
		map[string]any{"values": form.Values}, &putResp); code != http.StatusOK {
		t.Fatalf("PUT form code = %d, want 200", code)
	}

	var cfgResp struct {
		YAML string `json:"yaml"`
	}
	getJSON(t, srv.URL+"/api/config", &cfgResp)
	if !strings.Contains(cfgResp.YAML, "upload_concurrency: 5") {
		t.Errorf("yaml not updated:\n%s", cfgResp.YAML)
	}
	if !strings.Contains(cfgResp.YAML, "tok") {
		t.Errorf("token was dropped:\n%s", cfgResp.YAML)
	}

	var status struct {
		Config *configView `json:"config"`
	}
	getJSON(t, srv.URL+"/api/status", &status)
	if status.Config == nil || status.Config.UploadConcurrency != 5 {
		t.Errorf("status config = %+v, want upload_concurrency 5", status.Config)
	}
}

func TestAPI_ConfigForm_PutInvalidRejected(t *testing.T) {
	env := newTestSupervisor(t)
	before, err := os.ReadFile(env.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := env.server(t)

	var form struct {
		Values config.FormValues `json:"values"`
	}
	getJSON(t, srv.URL+"/api/config/form", &form)
	form.Values.WatchDirs = []string{"/nonexistent/cloud-sync-form-test"} // must fail the dir check

	code := doJSON(t, http.MethodPut, srv.URL+"/api/config/form",
		map[string]any{"values": form.Values}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("PUT invalid code = %d, want 400", code)
	}
	after, _ := os.ReadFile(env.cfgPath)
	if !bytes.Equal(before, after) {
		t.Errorf("config file changed after invalid form PUT")
	}
}

func TestAPI_ConfigRegenerate(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)

	var putResp configPutResponse
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/config/regenerate", nil, &putResp); code != http.StatusOK {
		t.Fatalf("regenerate code = %d, want 200", code)
	}
	var cfgResp struct {
		YAML string `json:"yaml"`
	}
	getJSON(t, srv.URL+"/api/config", &cfgResp)
	if !strings.Contains(cfgResp.YAML, "#") {
		t.Errorf("regenerated yaml has no comments:\n%s", cfgResp.YAML)
	}
	if !strings.Contains(cfgResp.YAML, "openlist_url") {
		t.Errorf("regenerated yaml missing openlist_url:\n%s", cfgResp.YAML)
	}
}

func TestAPI_CleanupFile(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no state")
	}
	src := filepath.Join(env.watch, "Movies", "Manual.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(&state.StatusRecord{
		Key: "Movies/Manual.mkv", SrcPath: src, SyncedAt: time.Now().UTC(), Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}

	srv := env.server(t)
	var resp struct {
		OK     bool `json:"ok"`
		DryRun bool `json:"dry_run"`
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/cleanup/file",
		map[string]string{"key": "Movies/Manual.mkv"}, &resp); code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if !resp.OK {
		t.Error("ok = false")
	}
}

func TestAPI_CleanupFile_RejectsNonSynced(t *testing.T) {
	env := newTestSupervisor(t)
	srv := env.server(t)
	code := doJSON(t, http.MethodPost, srv.URL+"/api/cleanup/file",
		map[string]string{"key": "nope.mkv"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

func TestAPI_Files_CleanupAtLive(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no state")
	}
	synced := time.Now().UTC().Add(-time.Hour)
	if err := st.Write(&state.StatusRecord{
		Key: "Live.mkv", SrcPath: filepath.Join(env.watch, "Live.mkv"),
		SyncedAt: synced, Status: "synced",
	}); err != nil {
		t.Fatal(err)
	}
	srv := env.server(t)
	var body filesResponse
	if code := getJSON(t, srv.URL+"/api/files?state=synced", &body); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	var want string
	for _, it := range body.Items {
		if it.Key == "Live.mkv" {
			want = it.CleanupAt
		}
	}
	if want == "" {
		t.Fatal("Live.mkv not found")
	}
	got, err := time.Parse(time.RFC3339, want)
	if err != nil {
		t.Fatalf("cleanup_at %q: %v", want, err)
	}
	exp := synced.Add(72 * time.Hour) // testutil.TestConfig uses CleanupAfter=72h
	if got.Sub(exp).Abs() > time.Minute {
		t.Errorf("cleanup_at = %v, want ~%v", got, exp)
	}
}

func TestAPI_Rescan(t *testing.T) {
	env := newTestSupervisor(t)
	_, st, _, ok := env.sup.Snapshot()
	if !ok {
		t.Fatal("no state")
	}
	src := filepath.Join(env.watch, "Res.mkv")
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(&state.StatusRecord{
		Key: "Res.mkv", SrcPath: src, SrcSize: info.Size(), SrcMtime: info.ModTime().UTC(),
		SyncedAt: time.Now().UTC(), Status: "cleaned",
	}); err != nil {
		t.Fatal(err)
	}

	srv := env.server(t)
	var rep struct {
		Restored int `json:"restored"`
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/api/rescan", nil, &rep); code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if rep.Restored != 1 {
		t.Fatalf("restored = %d, want 1", rep.Restored)
	}
}
