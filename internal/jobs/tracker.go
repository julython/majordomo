package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusDone
	StatusFailed
	StatusCancelled
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusDone:
		return "done"
	case StatusFailed:
		return "failed"
	case StatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// Job is a single tracked unit of work with its own cancellable context.
type Job struct {
	ID        string
	Kind      string
	Status    Status
	StartedAt time.Time
	EndedAt   time.Time
	Error     string

	cancel context.CancelFunc
}

// Tracker manages the lifecycle of running jobs.
// Both the probe runner and the watch worker use this.
type Tracker struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewTracker() *Tracker {
	return &Tracker{jobs: make(map[string]*Job)}
}

// Start registers a job and returns a cancellable context derived from
// the parent. The caller runs their work under this context — when
// Cancel is called, the context is done and exec.CommandContext,
// http calls, LLM generation, etc. all bail out.
func (t *Tracker) Start(parent context.Context, id, kind string) (context.Context, *Job) {
	ctx, cancel := context.WithCancel(parent)

	job := &Job{
		ID:        id,
		Kind:      kind,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		cancel:    cancel,
	}

	t.mu.Lock()
	t.jobs[id] = job
	t.mu.Unlock()

	slog.Info("job started", "id", id, "kind", kind)
	return ctx, job
}

// Complete marks a job as done or failed. Cleans up the cancel func.
func (t *Tracker) Complete(id string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	job, ok := t.jobs[id]
	if !ok {
		return
	}

	job.EndedAt = time.Now()
	if err != nil {
		job.Status = StatusFailed
		job.Error = err.Error()
	} else {
		job.Status = StatusDone
	}

	// Cancel is safe to call multiple times, clean up the context
	job.cancel()

	slog.Info("job completed", "id", id, "status", job.Status, "duration", job.EndedAt.Sub(job.StartedAt))
}

// Cancel stops a running job. The derived context is cancelled,
// which propagates to exec.CommandContext, http.Request, etc.
func (t *Tracker) Cancel(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	job, ok := t.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if job.Status != StatusRunning {
		return fmt.Errorf("job %s is %s, not running", id, job.Status)
	}

	job.cancel()
	job.Status = StatusCancelled
	job.EndedAt = time.Now()
	job.Error = "cancelled by user"

	slog.Info("job cancelled", "id", id, "kind", job.Kind, "ran_for", job.EndedAt.Sub(job.StartedAt))
	return nil
}

// CancelAll cancels every running job. Used on shutdown / ctrl+c.
func (t *Tracker) CancelAll() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	cancelled := 0
	for _, job := range t.jobs {
		if job.Status == StatusRunning {
			job.cancel()
			job.Status = StatusCancelled
			job.EndedAt = time.Now()
			job.Error = "cancelled: shutdown"
			cancelled++
		}
	}

	slog.Info("cancelled all jobs", "count", cancelled)
	return cancelled
}

// Get returns a snapshot of a job.
func (t *Tracker) Get(id string) (Job, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	job, ok := t.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *job, true
}

// Running returns all currently running jobs.
func (t *Tracker) Running() []Job {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []Job
	for _, job := range t.jobs {
		if job.Status == StatusRunning {
			out = append(out, *job)
		}
	}
	return out
}

// Prune removes completed/cancelled jobs older than the given age.
func (t *Tracker) Prune(olderThan time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	pruned := 0
	for id, job := range t.jobs {
		if job.Status != StatusRunning && job.EndedAt.Before(cutoff) {
			delete(t.jobs, id)
			pruned++
		}
	}
	return pruned
}
