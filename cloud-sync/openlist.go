package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	log     *slog.Logger
}

func NewClient(baseURL, token string, log *slog.Logger) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
		log:     log,
	}
}

// Ping checks connectivity by listing the root. Any 2xx with code=200 counts as ok.
func (c *Client) Ping(ctx context.Context) error {
	body := []byte(`{"path":"/","page":1,"per_page":1}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/fs/list", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("openlist ping: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("openlist ping: parse: %w", err)
	}
	if parsed.Code != 200 {
		return fmt.Errorf("openlist ping: code=%d msg=%s", parsed.Code, parsed.Message)
	}
	return nil
}

// Copy uploads one file by invoking POST /api/fs/copy with the real OpenList
// v4 request shape (src_dir + dst_dir + names + overwrite + skip_existing +
// merge). Returns the async task id when OpenList queues a task; returns ""
// with nil error when the copy completed synchronously (same storage) or
// skip_existing caused the server to silently no-op.
func (c *Client) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, overwrite bool) (string, error) {
	_ = dstName // OpenList v4 derives the destination name from names[0] + dst_dir
	body, _ := json.Marshal(map[string]any{
		"src_dir":       srcDir,
		"dst_dir":       dstDir,
		"names":         []string{srcName},
		"overwrite":     overwrite,
		"skip_existing": !overwrite,
		"merge":         false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/fs/copy", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("openlist copy: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Message string `json:"message"`
			Tasks   []struct {
				ID     string `json:"id"`
				State  int    `json:"state"`
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"tasks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("openlist copy: parse: %w", err)
	}
	if parsed.Code != 200 {
		return "", fmt.Errorf("openlist copy: code=%d msg=%s", parsed.Code, parsed.Message)
	}
	if len(parsed.Data.Tasks) == 0 {
		return "", nil
	}
	return parsed.Data.Tasks[0].ID, nil
}

// TaskDone checks the status of an async copy task. When taskID is empty
// (synchronous completion path), returns TaskSucceeded immediately.
// Otherwise queries POST /api/admin/task/copy/info?tid=<taskID> and maps
// the numeric `state` field:
//
//	state==2 (succeeded) → TaskSucceeded
//	state==4 (canceled)  → TaskFailed
//	state==7 (failed)    → TaskFailed
//	otherwise            → TaskPending
func (c *Client) TaskDone(ctx context.Context, taskID string) (TaskStatus, error) {
	if taskID == "" {
		return TaskSucceeded, nil
	}
	url := fmt.Sprintf("%s/api/admin/task/copy/info?tid=%s", c.baseURL, taskID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("openlist task: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ID     string `json:"id"`
			State  int    `json:"state"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("openlist task: parse: %w", err)
	}
	if parsed.Code != 200 {
		return "", fmt.Errorf("openlist task: code=%d msg=%s", parsed.Code, parsed.Message)
	}
	switch parsed.Data.State {
	case 2:
		return TaskSucceeded, nil
	case 4, 7:
		return TaskFailed, nil
	default:
		return TaskPending, nil
	}
}

// Exists reports whether path exists on OpenList (POST /api/fs/get).
//
//   - HTTP 2xx with code==200            → (true, nil)
//   - HTTP 2xx with code!=200 (not found)→ (false, nil)  [definitively absent]
//   - HTTP non-2xx (auth/server)         → (false, err)  [could not determine]
//
// The two failure modes are distinguished so a pre-check can tell "missing"
// apart from "could not reach OpenList".
func (c *Client) Exists(ctx context.Context, path string) (bool, error) {
	body, _ := json.Marshal(map[string]any{"path": path})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/fs/get", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("openlist exists: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("openlist exists: parse: %w", err)
	}
	return parsed.Code == 200, nil
}
