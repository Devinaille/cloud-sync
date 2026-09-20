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
	// TaskErrCount TaskPoll calls return TaskErr; TaskPendingCount makes the
	// next TaskPendingCount TaskPoll calls return pending with TaskProgress;
	// TaskStatusOverride, when non-empty, forces every TaskPoll result;
	// TaskProgress is reported for pending states; TaskNotFound makes TaskPoll
	// return ErrTaskNotFound.
	CopyErr            error
	TaskErr            error
	TaskErrCount       int
	TaskPendingCount   int
	TaskStatusOverride openlist.TaskStatus
	TaskProgress       float64
	TaskNotFound       bool
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

func (m *Uploader) TaskPoll(ctx context.Context, taskID string) (openlist.TaskProgress, error) {
	if err := ctx.Err(); err != nil {
		return openlist.TaskProgress{}, err
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	if m.TaskErrCount > 0 {
		m.TaskErrCount--
		return openlist.TaskProgress{}, m.TaskErr
	}
	if m.TaskNotFound {
		return openlist.TaskProgress{}, openlist.ErrTaskNotFound
	}
	if m.TaskStatusOverride != "" {
		return openlist.TaskProgress{Status: m.TaskStatusOverride, Progress: m.TaskProgress}, nil
	}
	if m.TaskPendingCount > 0 {
		m.TaskPendingCount--
		return openlist.TaskProgress{Status: openlist.TaskPending, Progress: m.TaskProgress}, nil
	}
	st, ok := m.TaskStatuses[taskID]
	if !ok {
		return openlist.TaskProgress{Status: openlist.TaskFailed}, nil
	}
	if st == openlist.TaskPending {
		m.TaskStatuses[taskID] = openlist.TaskSucceeded
		return openlist.TaskProgress{Status: openlist.TaskPending, Progress: m.TaskProgress}, nil
	}
	return openlist.TaskProgress{Status: st, Progress: 100}, nil
}
