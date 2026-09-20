package mockopenlist

import (
	"context"
	"sync"

	"cloud-sync/internal/openlist"
)

// CopyCall records one Copy invocation.
type CopyCall struct {
	SrcDir, SrcName, DstDir, DstName string
}

// Uploader is an in-memory fake of the OpenList client for tests. It satisfies
// pipeline.Uploader structurally and deliberately does not import the pipeline
// package, so in-package pipeline tests can use it without an import cycle.
// Its fields are exported so tests in other packages can inspect and
// synchronize access.
type Uploader struct {
	Mu           sync.Mutex
	CopyCalls    []CopyCall
	TaskStatuses map[string]openlist.TaskStatus
	// CloudExists backs Exists (pre-check); a nil map means "nothing exists".
	CloudExists map[string]bool

	// Test controls. CopyErr makes Copy fail; TaskErrCount makes the next
	// TaskErrCount TaskDone calls return TaskErr; TaskStatusOverride, when
	// non-empty, forces every TaskDone result.
	CopyErr            error
	TaskErr            error
	TaskErrCount       int
	TaskStatusOverride openlist.TaskStatus
}

func New() *Uploader {
	return &Uploader{TaskStatuses: map[string]openlist.TaskStatus{}}
}

func (m *Uploader) Exists(_ context.Context, path string) (bool, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	return m.CloudExists[path], nil
}

func (m *Uploader) Copy(ctx context.Context, srcDir, srcName, dstDir, dstName string, _ bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.CopyCalls = append(m.CopyCalls, CopyCall{srcDir, srcName, dstDir, dstName})
	if m.CopyErr != nil {
		return "", m.CopyErr
	}
	id := "task-" + srcName
	m.TaskStatuses[id] = openlist.TaskPending
	return id, nil
}

func (m *Uploader) TaskDone(ctx context.Context, taskID string) (openlist.TaskStatus, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	if m.TaskErrCount > 0 {
		m.TaskErrCount--
		return "", m.TaskErr
	}
	if m.TaskStatusOverride != "" {
		return m.TaskStatusOverride, nil
	}
	st, ok := m.TaskStatuses[taskID]
	if !ok {
		return openlist.TaskFailed, nil
	}
	if st == openlist.TaskPending {
		m.TaskStatuses[taskID] = openlist.TaskSucceeded
		return openlist.TaskPending, nil
	}
	return st, nil
}
