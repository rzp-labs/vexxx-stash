package api

import (
	"bytes"
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stretchr/testify/require"
)

func TestJobModelWorkUnitCounts(t *testing.T) {
	processed, total := 720, 1200
	snapshot := job.Job{ID: 1, Status: job.StatusRunning, Progress: 0.6, Processed: &processed, Total: &total, Details: []string{"sampled activity"}}
	model := jobToJobModel(snapshot)
	require.Equal(t, int64(720), *model.Processed)
	require.Equal(t, int64(1200), *model.Total)
	require.Equal(t, 0.6, *model.Progress)
	require.Equal(t, model, makeJobStatusUpdate(JobStatusUpdateTypeUpdate, snapshot).Job)
	snapshot.Status = job.StatusCancelled
	cancelled := makeJobStatusUpdate(JobStatusUpdateTypeRemove, snapshot).Job
	require.Equal(t, JobStatusCancelled, cancelled.Status)
	require.Equal(t, int64(720), *cancelled.Processed)
	require.Equal(t, int64(1200), *cancelled.Total)
	snapshot.Progress = job.ProgressIndefinite
	snapshot.Total = nil
	unknown := jobToJobModel(snapshot)
	require.Nil(t, unknown.Progress)
	require.Nil(t, unknown.Total)
	require.Equal(t, int64(720), *unknown.Processed)
	snapshot.Processed = nil
	snapshot.Progress = 0.9
	percentage := jobToJobModel(snapshot)
	require.Nil(t, percentage.Processed)
	require.Nil(t, percentage.Total)
	require.Equal(t, 0.9, *percentage.Progress)
}

func TestJobModelWorkUnitCountsInt64Boundary(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("Go int cannot represent counts above GraphQL Int on 32-bit targets")
	}
	schema := NewExecutableSchema(Config{Resolvers: &Resolver{}}).Schema()
	for _, field := range []string{"processed", "total"} {
		require.Equal(t, "Int64", schema.Types["Job"].Fields.ForName(field).Type.NamedType)
	}
	for _, value := range []int64{math.MaxInt32, math.MaxInt32 + 1, math.MaxInt64} {
		t.Run(strconv.FormatInt(value, 10), func(t *testing.T) {
			count := int(value)
			model := jobToJobModel(job.Job{Processed: &count, Total: &count})
			require.Equal(t, value, *model.Processed)
			require.Equal(t, value, *model.Total)
			var serialized bytes.Buffer
			ec := &executionContext{}
			ec.marshalOInt642ᚖint64(context.Background(), nil, model.Processed).MarshalGQL(&serialized)
			require.Equal(t, strconv.FormatInt(value, 10), serialized.String())
		})
	}
}
