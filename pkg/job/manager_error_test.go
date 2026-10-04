package job

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCancellationPrecedesLateExecutorError(t *testing.T) {
	m := NewManager()
	defer m.StopAndWait(time.Second)
	started := make(chan struct{})
	finish := make(chan struct{})
	id := m.Add(context.Background(), "late task error", MakeJobExec(func(context.Context, *Progress) error {
		close(started)
		<-finish
		return errors.New("asset failed after executor's last cancellation check")
	}))
	select {
	case <-started:
	case <-time.After(time.Second):
		close(finish)
		t.Fatal("executor did not start")
	}
	m.CancelJob(id)
	close(finish)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		j := m.GetJob(id)
		if j != nil && j.EndTime != nil {
			if j.Status != StatusCancelled || j.Error != nil {
				t.Fatalf("late executor error overrode explicit cancellation: %+v", j)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancelled executor did not drain")
}
