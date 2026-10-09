// Package job provides the job execution and management functionality for the application.
package job

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type correlationKey struct{}

// Correlation returns the opaque identifier of the executing job. It carries no
// library identifiers or descriptions and is shared by the job's child tasks.
func Correlation(ctx context.Context) string {
	value, _ := ctx.Value(correlationKey{}).(string)
	return value
}

func withCorrelation(ctx context.Context) context.Context {
	return context.WithValue(ctx, correlationKey{}, uuid.NewString())
}

type JobExecFn func(ctx context.Context, progress *Progress) error

// JobExec represents the implementation of a Job to be executed.
type JobExec interface {
	Execute(ctx context.Context, progress *Progress) error
}

type jobExecImpl struct {
	fn JobExecFn
}

func (j *jobExecImpl) Execute(ctx context.Context, progress *Progress) error {
	return j.fn(ctx, progress)
}

// MakeJobExec returns a simple JobExec implementation using the provided
// function.
func MakeJobExec(fn JobExecFn) JobExec {
	return &jobExecImpl{
		fn: fn,
	}
}

// Status is the status of a Job
type Status string

const (
	// StatusReady means that the Job is not yet started.
	StatusReady Status = "READY"
	// StatusRunning means that the job is currently running.
	StatusRunning Status = "RUNNING"
	// StatusStopping means that the job is cancelled but is still running.
	StatusStopping Status = "STOPPING"
	// StatusFinished means that the job was completed.
	StatusFinished Status = "FINISHED"
	// StatusCancelled means that the job was cancelled and is now stopped.
	StatusCancelled Status = "CANCELLED"
	// StatusFailed means that the job failed.
	StatusFailed Status = "FAILED"
)

// Job represents the status of a queued or running job.
type Job struct {
	ID     int
	Status Status
	// details of the current operations of the job
	Details     []string
	Description string
	// Progress in terms of 0 - 1.
	Progress float64
	// Reported work units, not successful tasks or activity messages. Nil when
	// counters are unavailable (e.g. percentage-only progress). Total is nil
	// until the job defines a positive total; it may change during execution.
	Processed *int
	Total     *int
	StartTime *time.Time
	EndTime   *time.Time
	AddTime   time.Time
	Error     *string

	outerCtx   context.Context
	exec       JobExec
	cancelFunc context.CancelFunc
}

// statusCopy returns a copy of the Job with only the fields needed for
// status reporting. Internal fields (exec, cancelFunc, outerCtx) are
// excluded so that subscription channels don't retain heavy resources.
func (j *Job) statusCopy() Job {
	return Job{
		ID:          j.ID,
		Status:      j.Status,
		Details:     j.Details,
		Description: j.Description,
		Progress:    j.Progress,
		Processed:   j.Processed,
		Total:       j.Total,
		StartTime:   j.StartTime,
		EndTime:     j.EndTime,
		AddTime:     j.AddTime,
		Error:       j.Error,
	}
}

// TimeElapsed returns the total time elapsed for the job.
// If the EndTime is set, then it uses this to calculate the elapsed time, otherwise it uses time.Now.
func (j *Job) TimeElapsed() time.Duration {
	var end time.Time
	if j.EndTime != nil {
		end = time.Now()
	} else {
		end = *j.EndTime
	}

	return end.Sub(*j.StartTime)
}

func (j *Job) cancel() {
	switch j.Status {
	case StatusReady:
		j.Status = StatusCancelled
	case StatusRunning:
		j.Status = StatusStopping
	}

	if j.cancelFunc != nil {
		j.cancelFunc()
	}
}

func (j *Job) error(err error) {
	errStr := err.Error()
	j.Error = &errStr
	j.Status = StatusFailed
}

// IsCancelled returns true if cancel has been called on the context.
func IsCancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
