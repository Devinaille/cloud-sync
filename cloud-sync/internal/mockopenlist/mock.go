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
}

func New() *Uploader {
	return &Uploader{TaskStatuses: map[string]openlist.TaskStatus{}}
}

func (m *Uploader) Exists(_ context.Context, path string) (bool, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	return m.CloudExists[path], nil
}

func (m *Uploader) Copy(_ context.Context, srcDir, srcName, dstDir, dstName string, _ bool) (string, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.CopyCalls = append(m.CopyCalls, CopyCall{srcDir, srcName, dstDir, dstName})
	id := "task-" + srcName
	m.TaskStatuses[id] = openlist.TaskPending
	return id, nil
}

func (m *Uploader) TaskDone(_ context.Context, taskID string) (openlist.TaskStatus, error) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
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
