package jobs

import (
	"context"
	"github.com/itsbryanman/RCLIFT/internal/operation"
	"sync"
	"time"
)

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Done      Status = "done"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

type Job struct {
	ID                int
	Spec              operation.Spec
	Status            Status
	Started, Finished time.Time
	Output            string
	Err               error
	cancel            context.CancelFunc
}
type Runner func(context.Context, []string, func(string)) error
type Manager struct {
	mu    sync.RWMutex
	jobs  []*Job
	run   Runner
	slots chan struct{}
}

func New(concurrency int, r Runner) *Manager {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Manager{run: r, slots: make(chan struct{}, concurrency)}
}
func (m *Manager) Start(spec operation.Spec) *Job {
	ctx, c := context.WithCancel(context.Background())
	m.mu.Lock()
	j := &Job{ID: len(m.jobs) + 1, Spec: spec, Status: Queued, cancel: c}
	m.jobs = append(m.jobs, j)
	m.mu.Unlock()
	go m.execute(ctx, j)
	return j
}

// execute waits for a slot; a job cancelled while queued never runs.
func (m *Manager) execute(ctx context.Context, j *Job) {
	select {
	case m.slots <- struct{}{}:
	case <-ctx.Done():
		m.mu.Lock()
		j.Status = Cancelled
		j.Finished = time.Now()
		m.mu.Unlock()
		return
	}
	defer func() { <-m.slots }()
	if ctx.Err() != nil {
		m.mu.Lock()
		j.Status = Cancelled
		j.Finished = time.Now()
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	j.Status = Running
	j.Started = time.Now()
	m.mu.Unlock()
	err := m.run(ctx, j.Spec.Command(), func(s string) {
		m.mu.Lock()
		j.Output += s
		if len(j.Output) > 8000 {
			j.Output = j.Output[len(j.Output)-8000:]
		}
		m.mu.Unlock()
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	j.Finished = time.Now()
	j.Err = err
	if ctx.Err() != nil {
		j.Status = Cancelled
	} else if err != nil {
		j.Status = Failed
	} else {
		j.Status = Done
	}
	j.cancel()
}
func (m *Manager) Jobs() []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Job, len(m.jobs))
	for i, j := range m.jobs {
		out[i] = *j
		out[i].cancel = nil
	}
	return out
}
func (m *Manager) Cancel(id int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, j := range m.jobs {
		if j.ID == id && j.cancel != nil {
			j.cancel()
		}
	}
}
