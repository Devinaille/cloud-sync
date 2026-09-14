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

func TestClient_Copy_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/copy" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["src_dir"] != "/local_media" || req["dst_dir"] != "/139yun_media" {
			t.Errorf("bad request body: %+v", req)
		}
		if !strings.HasPrefix(req["src_name"], "media/") {
			t.Errorf("src_name should preserve directory: %q", req["src_name"])
		}
		if !strings.HasPrefix(req["dst_name"], "media/") {
			t.Errorf("dst_name should preserve directory: %q", req["dst_name"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]string{"task_id": "abc-123"},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	id, err := c.Copy(context.Background(), "/local_media", "media/X.mkv", "/139yun_media", "media/X.mkv")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if id != "abc-123" {
		t.Errorf("task id = %q, want abc-123", id)
	}
}

func TestClient_Copy_NonZeroCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "kaboom"})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Copy(context.Background(), "/a", "x", "/b", "y")
	if err == nil || !strings.Contains(err.Error(), "code=500") {
		t.Errorf("err = %v, want code=500", err)
	}
}

func TestClient_Copy_HTTP5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Copy(context.Background(), "/a", "x", "/b", "y")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("err = %v, want HTTP 502", err)
	}
}

func TestClient_TaskDone_Succeeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/task/abc-123/done" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]string{"status": "succeeded"},
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

func TestClient_TaskDone_Pending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]string{"status": "pending"},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	st, err := c.TaskDone(context.Background(), "x")
	if err != nil {
		t.Fatalf("TaskDone: %v", err)
	}
	if st != TaskPending {
		t.Errorf("status = %q, want pending", st)
	}
}

func TestClient_TaskDone_FailedWithMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]string{"status": "failed", "error": "quota exceeded"},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	st, err := c.TaskDone(context.Background(), "x")
	if err != nil {
		t.Fatalf("TaskDone: %v", err)
	}
	if st != TaskFailed {
		t.Errorf("status = %q, want failed", st)
	}
}
