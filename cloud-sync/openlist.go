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

func New(baseURL, token string, log *slog.Logger) *Client {
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

// Copy invokes POST /api/fs/copy and returns the task ID.
func (c *Client) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"src_dir":  srcDir,
		"src_name": srcName,
		"dst_dir":  dstDir,
		"dst_name": dstName,
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
		Code int `json:"code"`
		Data struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("openlist copy: parse: %w", err)
	}
	if parsed.Code != 200 {
		return "", fmt.Errorf("openlist copy: code=%d msg=%s", parsed.Code, parsed.Message)
	}
	return parsed.Data.TaskID, nil
}

// TaskDone checks the status of an async task.
func (c *Client) TaskDone(ctx context.Context, taskID string) (TaskStatus, error) {
	url := fmt.Sprintf("%s/api/admin/task/%s/done", c.baseURL, taskID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.token)
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
		Code int `json:"code"`
		Data struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("openlist task: parse: %w", err)
	}
	if parsed.Code != 200 {
		return "", fmt.Errorf("openlist task: code=%d msg=%s", parsed.Code, parsed.Message)
	}
	switch TaskStatus(parsed.Data.Status) {
	case TaskPending, TaskSucceeded, TaskFailed:
		return TaskStatus(parsed.Data.Status), nil
	default:
		return "", fmt.Errorf("openlist task: unknown status %q", parsed.Data.Status)
	}
}
