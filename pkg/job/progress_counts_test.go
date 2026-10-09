package job

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// No dispatcher is needed to exercise progress snapshots and throttling.
func countProgress(t *testing.T) (*Manager, *Progress, int) {
	t.Helper()
	m := &Manager{updateThrottleLimit: time.Hour}
	j := &Job{ID: 1, Status: StatusRunning}
	m.queue = []*Job{j}
	p := m.newProgress(j)
	t.Cleanup(func() {
		m.mutex.Lock()
		defer m.mutex.Unlock()
		if p.updater.updateTimer != nil {
			p.updater.updateTimer.Stop()
		}
	})
	return m, p, j.ID
}

func TestProgressCountsBeyondRetainedActivity(t *testing.T) {
	m, p, id := countProgress(t)
	s := newSubscription()
	m.subscriptions = []*ManagerSubscription{s}
	p.SetTotal(1200)
	initial := <-s.UpdatedJob
	for i := 0; i < 1200; i++ {
		p.ExecuteTask("duplicate activity", p.Increment)
		if i == 500 || i == 719 {
			snapshot := m.GetJob(id)
			require.Equal(t, i+1, *snapshot.Processed)
			require.Equal(t, 1200, *snapshot.Total)
		}
	}
	snapshot := m.GetQueue()[0]
	require.Equal(t, 1200, *snapshot.Processed)
	require.Equal(t, 1200, *snapshot.Total)
	require.Equal(t, 1.0, snapshot.Progress)
	require.Empty(t, snapshot.Details)
	// Throttled activities and deduplication cannot limit integer counts.
	require.Empty(t, s.UpdatedJob)
	require.Equal(t, 0, *initial.Processed, "published snapshots must not mutate")
	require.Equal(t, 1200, *initial.Total)
	// The throttled notification uses the current authoritative snapshot.
	m.mutex.Lock()
	p.updater.notifyUpdate()
	m.mutex.Unlock()
	update := <-s.UpdatedJob
	require.Equal(t, snapshot.Processed, update.Processed)
	require.Equal(t, snapshot.Total, update.Total)
}

func TestProgressCountsChangingTotalsAndModes(t *testing.T) {
	m, p, id := countProgress(t)
	p.ExecuteTask("activity alone has no counters", func() {})
	require.Nil(t, m.GetJob(id).Processed)
	p.SetProcessed(600)
	require.Equal(t, 600, *m.GetJob(id).Processed)
	require.Nil(t, m.GetJob(id).Total)
	require.Equal(t, ProgressIndefinite, m.GetJob(id).Progress)
	p.AddTotal(800)
	require.Nil(t, m.GetJob(id).Total, "discovery has not defined a total")
	p.Definite()
	before := m.GetJob(id)
	require.Equal(t, 800, *before.Total)
	require.Equal(t, 0.75, before.Progress)
	p.AddTotal(400)
	after := m.GetJob(id)
	require.Equal(t, 600, *after.Processed)
	require.Equal(t, 1200, *after.Total)
	require.Equal(t, 0.5, after.Progress)
	require.Equal(t, 800, *before.Total)
	p.SetPercent(0.9)
	p.ExecuteTask("percentage-only activity", func() {})
	require.Nil(t, m.GetJob(id).Processed)
	require.Nil(t, m.GetJob(id).Total)
	require.Equal(t, 0.9, m.GetJob(id).Progress)
	p.Increment()
	require.Equal(t, 601, *m.GetJob(id).Processed)
	p.Indefinite()
	require.Nil(t, m.GetJob(id).Total)
	require.Equal(t, 601, *m.GetJob(id).Processed)
	p.SetTotal(0)
	require.Nil(t, m.GetJob(id).Total)
	p.SetProcessed(-1)
	require.Nil(t, m.GetJob(id).Processed)
}

func TestProgressModesDoNotInventCounters(t *testing.T) {
	for _, percentageOnly := range []bool{false, true} {
		m, p, id := countProgress(t)
		if percentageOnly {
			p.SetPercent(0.5)
		}
		p.Indefinite()
		require.Nil(t, m.GetJob(id).Processed)
		require.Nil(t, m.GetJob(id).Total)
		p.Definite()
		require.Nil(t, m.GetJob(id).Processed)
		require.Nil(t, m.GetJob(id).Total)
		p.SetProcessed(7)
		p.Indefinite()
		require.Equal(t, 7, *m.GetJob(id).Processed)
		require.Nil(t, m.GetJob(id).Total)
		p.SetPercent(0.5)
		p.Definite()
		require.Nil(t, m.GetJob(id).Processed, "mode change cannot revive percentage-only counters")
	}
}

func TestProgressCountsConcurrentSnapshots(t *testing.T) {
	m, p, id := countProgress(t)
	p.SetTotal(600)
	var workers, discovery sync.WaitGroup
	workers.Add(4)
	discovery.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer workers.Done()
			p.AddTotal(150)
			discovery.Done()
			discovery.Wait()
			for n := 0; n < 300; n++ {
				p.Increment()
			}
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	for {
		snapshot := m.GetJob(id)
		require.NotNil(t, snapshot.Processed)
		require.NotNil(t, snapshot.Total)
		require.LessOrEqual(t, *snapshot.Processed, *snapshot.Total)
		require.Equal(t, float64(*snapshot.Processed)/float64(*snapshot.Total), snapshot.Progress)
		select {
		case <-done:
			require.Equal(t, 1200, *m.GetJob(id).Processed)
			require.Equal(t, 1200, *m.GetJob(id).Total)
			return
		default:
		}
	}
}

func TestProgressCountsPreservedOnCancellation(t *testing.T) {
	m, p, id := countProgress(t)
	s := newSubscription()
	m.subscriptions = []*ManagerSubscription{s}
	p.SetTotal(1200)
	p.SetProcessed(720)
	m.CancelJob(id)
	j := m.queue[0]
	m.onJobFinish(j)
	m.mutex.Lock()
	m.removeJob(j)
	m.mutex.Unlock()
	final := <-s.RemovedJob
	require.Equal(t, StatusCancelled, final.Status)
	require.Equal(t, 720, *final.Processed)
	require.Equal(t, 1200, *final.Total)
	require.Equal(t, 0.6, final.Progress)
	require.Equal(t, final.Processed, m.GetJob(id).Processed)
}
