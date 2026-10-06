package job

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestJobCorrelationAndRecoveredPanicObserver(t *testing.T) {
	for _, observerPanics := range []bool{false, true} {
		t.Run(map[bool]string{false: "capture", true: "observer-failure"}[observerPanics], func(t *testing.T) {
			m := NewManager()
			t.Cleanup(func() { m.StopAndWait(time.Second) })
			observed := make(chan string, 2)
			m.OnPanic = func(ctx context.Context, value any) {
				if value != "worker failed" {
					t.Errorf("panic value lost: %v", value)
				}
				observed <- Correlation(ctx)
				if observerPanics {
					panic("observer failed")
				}
			}
			var executionCorrelation string
			id := m.Add(context.Background(), "private description", MakeJobExec(func(ctx context.Context, _ *Progress) error {
				executionCorrelation = Correlation(ctx)
				panic("worker failed")
			}))
			select {
			case correlation := <-observed:
				if _, err := uuid.Parse(correlation); err != nil || correlation != executionCorrelation {
					t.Fatalf("invalid or inconsistent correlation: %q %v", correlation, err)
				}
			case <-time.After(time.Second):
				t.Fatal("recovered panic was not observed")
			}
			deadline := time.Now().Add(time.Second)
			for {
				status := m.GetJob(id)
				if status != nil && status.EndTime != nil {
					if status.Status != StatusFailed {
						t.Fatalf("panic observer changed job status: %s", status.Status)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("panicked job did not finish")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case <-observed:
				t.Fatal("panic observed twice")
			default:
			}
		})
	}
}

func TestJobCorrelationsAreIndependent(t *testing.T) {
	m := NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	correlations := make(chan string, 2)
	for range 2 {
		m.Add(context.Background(), "same description", MakeJobExec(func(ctx context.Context, _ *Progress) error {
			correlations <- Correlation(ctx)
			return nil
		}))
	}
	var first string
	for range 2 {
		select {
		case value := <-correlations:
			if value == "" || value == first {
				t.Fatal("job correlation missing or reused")
			}
			first = value
		case <-time.After(time.Second):
			t.Fatal("job did not execute")
		}
	}
	if Correlation(context.Background()) != "" {
		t.Fatal("non-job context acquired a correlation")
	}
}
