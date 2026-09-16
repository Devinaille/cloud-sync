package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return NewClient(srv.URL, "test-token", slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

// ---- Ping tests (unchanged) ----------------------------------------------

func TestClient_Ping_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "test-token" {
			t.Errorf("missing/wrong Authorization header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok"})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestClient_Ping_NonZeroCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "message": "unauthorized"})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping: expected error for code=401, got nil")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("err = %v, want code and message", err)
	}
}

func TestClient_Ping_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping: expected error, got nil")
	}
}

// ---- Copy tests ----------------------------------------------------------

// TestClient_Copy_Success_SkipExistingDefault verifies the request uses the
// real OpenList v4 shape (src_dir + dst_dir + names + overwrite + skip_existing
// + merge) and parses the task id from data.tasks[0].id. Default overwrite=false
// implies skip_existing=true and merge=false. Legacy fields src_name/dst_name
// must NOT appear.
func TestClient_Copy_Success_SkipExistingDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/copy" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		if r.Header.Get("Authorization") != "test-token" {
			t.Errorf("missing/wrong Authorization header")
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("unexpected Content-Type %s", ct)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req["src_dir"] != "/local_media" {
			t.Errorf("src_dir = %v, want /local_media", req["src_dir"])
		}
		if req["dst_dir"] != "/139yun_media" {
			t.Errorf("dst_dir = %v, want /139yun_media", req["dst_dir"])
		}
		names, ok := req["names"].([]any)
		if !ok {
			t.Fatalf("names field missing or not array: %+v", req["names"])
		}
		if len(names) != 1 || names[0] != "X.mkv" {
			t.Errorf("names = %v, want [X.mkv]", names)
		}
		if req["overwrite"] != false {
			t.Errorf("overwrite = %v, want false", req["overwrite"])
		}
		if req["skip_existing"] != true {
			t.Errorf("skip_existing = %v, want true", req["skip_existing"])
		}
		if req["merge"] != false {
			t.Errorf("merge = %v, want false", req["merge"])
		}
		if _, has := req["src_name"]; has {
			t.Errorf("legacy field src_name must not appear")
		}
		if _, has := req["dst_name"]; has {
			t.Errorf("legacy field dst_name must not appear")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    200,
			"message": "success",
			"data": map[string]any{
				"message": "copied",
				"tasks": []map[string]any{
					{"id": "task-1", "state": 2, "status": "succeeded"},
				},
			},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	id, err := c.Copy(context.Background(), "/local_media", "X.mkv", "/139yun_media", "X.mkv", false)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if id != "task-1" {
		t.Errorf("task id = %q, want task-1", id)
	}
}

// TestClient_Copy_OverwriteTrue verifies overwrite=true yields
// overwrite:true, skip_existing:false in the request.
func TestClient_Copy_OverwriteTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req["overwrite"] != true {
			t.Errorf("overwrite = %v, want true", req["overwrite"])
		}
		if req["skip_existing"] != false {
			t.Errorf("skip_existing = %v, want false", req["skip_existing"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    200,
			"message": "success",
			"data": map[string]any{
				"tasks": []map[string]any{
					{"id": "task-2", "state": 2},
				},
			},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	id, err := c.Copy(context.Background(), "/a", "x", "/b", "x", true)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if id != "task-2" {
		t.Errorf("task id = %q, want task-2", id)
	}
}

// TestClient_Copy_SkipExistingEmptyTasks verifies that when the destination
// file already exists and skip_existing is effective, OpenList returns an
// empty tasks array. Client should return ("", nil) — synchronous skip.
func TestClient_Copy_SkipExistingEmptyTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    200,
			"message": "success",
			"data": map[string]any{
				"message": "skipped",
				"tasks":   []any{},
			},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	id, err := c.Copy(context.Background(), "/a", "x", "/b", "x", false)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if id != "" {
		t.Errorf("task id = %q, want \"\" (synchronous completion)", id)
	}
}

// TestClient_Copy_FileExistsReturnsError verifies overwrite=false +
// skip_existing=false against an existing destination yields a 403 error
// from OpenList. Client surfaces the error.
func TestClient_Copy_FileExistsReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    403,
			"message": "file [X.mkv] exists",
			"data":    nil,
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Copy(context.Background(), "/a", "X.mkv", "/b", "X.mkv", false)
	if err == nil {
		t.Fatal("Copy: expected error, got nil")
	}
}

// TestClient_Copy_EmptyNamesReturnsError is a defensive guard: if a future
// refactor accidentally sends an empty names slice, OpenList will reject with
// HTTP 400 "Empty file names" and the client should surface an error.
func TestClient_Copy_EmptyNamesReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		names, _ := req["names"].([]any)
		if len(names) == 0 || names[0] == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":400,"message":"Empty file names"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"tasks": []any{}},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Copy(context.Background(), "/a", "", "/b", "", false)
	if err == nil {
		t.Fatal("Copy: expected error when names empty, got nil")
	}
}

// TestClient_Copy_HTTPError verifies that an upstream HTTP 5xx becomes a
// client error containing the status code.
func TestClient_Copy_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Copy(context.Background(), "/a", "x", "/b", "x", false)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("err = %v, want HTTP 502", err)
	}
}

// TestClient_Copy_NonZeroCode verifies a 200 HTTP response with non-200 code
// becomes a client error mentioning the code.
func TestClient_Copy_NonZeroCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    500,
			"message": "kaboom",
			"data":    nil,
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Copy(context.Background(), "/a", "x", "/b", "x", false)
	if err == nil || !strings.Contains(err.Error(), "code=500") {
		t.Errorf("err = %v, want code=500", err)
	}
}

// ---- TaskDone tests ------------------------------------------------------

// TestClient_TaskDone_EmptyTaskID verifies the synchronous completion shortcut:
// when Copy returned ("", nil) the caller passes "" to TaskDone and the
// client must short-circuit to TaskSucceeded without making any HTTP request.
func TestClient_TaskDone_EmptyTaskID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("TaskDone must not make an HTTP request for empty taskID; got %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	st, err := c.TaskDone(context.Background(), "")
	if err != nil {
		t.Fatalf("TaskDone: %v", err)
	}
	if st != TaskSucceeded {
		t.Errorf("status = %q, want succeeded", st)
	}
}

// TestClient_TaskDone_Succeeded verifies state==2 maps to TaskSucceeded.
func TestClient_TaskDone_Succeeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/task/copy/info" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("tid") != "abc-123" {
			t.Errorf("unexpected tid = %q", r.URL.Query().Get("tid"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    200,
			"message": "success",
			"data": map[string]any{
				"id":          "abc-123",
				"name":        "copy",
				"state":       2,
				"status":      "succeeded",
				"progress":    100.0,
				"start_time":  "2026-09-15T00:00:00Z",
				"end_time":    "2026-09-15T00:00:01Z",
				"total_bytes": 100,
				"error":       "",
			},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	st, err := c.TaskDone(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("TaskDone: %v", err)
	}
	if st != TaskSucceeded {
		t.Errorf("status = %q, want succeeded", st)
	}
}

// TestClient_TaskDone_Pending verifies state==1 maps to TaskPending.
func TestClient_TaskDone_Pending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tid") != "task-1" {
			t.Errorf("unexpected tid = %q", r.URL.Query().Get("tid"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"id": "task-1", "state": 1, "status": "pending"},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	st, err := c.TaskDone(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("TaskDone: %v", err)
	}
	if st != TaskPending {
		t.Errorf("status = %q, want pending", st)
	}
}

// TestClient_TaskDone_Failed verifies state==7 maps to TaskFailed.
func TestClient_TaskDone_Failed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{
				"id":     "task-7",
				"state":  7,
				"status": "failed",
				"error":  "destination unreachable",
			},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	st, err := c.TaskDone(context.Background(), "task-7")
	if err != nil {
		t.Fatalf("TaskDone: %v", err)
	}
	if st != TaskFailed {
		t.Errorf("status = %q, want failed", st)
	}
}

// TestClient_TaskDone_Canceled verifies state==4 (canceled) maps to TaskFailed
// since cancellation is a terminal failure for our purposes.
func TestClient_TaskDone_Canceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{
				"id":     "task-4",
				"state":  4,
				"status": "canceled",
			},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	st, err := c.TaskDone(context.Background(), "task-4")
	if err != nil {
		t.Fatalf("TaskDone: %v", err)
	}
	if st != TaskFailed {
		t.Errorf("status = %q, want failed", st)
	}
}

// TestClient_TaskDone_HTTPError verifies HTTP 500 becomes a client error.
func TestClient_TaskDone_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("oops"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.TaskDone(context.Background(), "task-err")
	if err == nil {
		t.Fatal("TaskDone: expected error, got nil")
	}
}

// TestClient_TaskDone_NonZeroCode verifies HTTP 200 with code=500 yields an
// error.
func TestClient_TaskDone_NonZeroCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    500,
			"message": "kaboom",
			"data":    nil,
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.TaskDone(context.Background(), "task-x")
	if err == nil || !strings.Contains(err.Error(), "code=500") {
		t.Errorf("err = %v, want code=500", err)
	}
}
