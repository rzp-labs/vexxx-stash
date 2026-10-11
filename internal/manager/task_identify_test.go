package manager

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type identifyTestScraper struct {
	ids   []int
	after func(int)
}

func (s *identifyTestScraper) ScrapeScenes(_ context.Context, id int) ([]*models.ScrapedScene, error) {
	s.ids = append(s.ids, id)
	if s.after != nil {
		s.after(id)
	}
	return nil, nil // A checked scene with no match is an existing supported outcome.
}

func identifyTestRepository(t *testing.T) *mocks.Database {
	t.Helper()
	db := mocks.NewDatabase()
	previous := instance
	instance = &Manager{Config: config.InitializeEmpty(), Repository: models.Repository{TxnManager: db, Scene: db.Scene}}
	t.Cleanup(func() { instance = previous })
	return db
}

func runIdentifyTestJob(t *testing.T, ctx context.Context, fn job.JobExecFn) *job.Job {
	t.Helper()
	m := job.NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	id := m.Add(ctx, "Identify test selection", job.MakeJobExec(fn))
	return waitMarkerJob(t, m, id)
}

func TestIdentifyAllScenesRemovedBeforeProcessing(t *testing.T) {
	db := identifyTestRepository(t)
	ids := []int{1, 2, 3}
	result := models.NewSceneQueryResult(db.Scene)
	result.IDs = ids
	result.Count = len(ids)
	db.Scene.On("Query", mock.Anything, mock.Anything).Return(result, nil)
	// This reproduces the old Resolve/FindMany failure after an ID query.
	db.Scene.On("FindMany", mock.Anything, ids).Return(nil, fmt.Errorf("scene with id 2 not found"))
	db.Scene.On("Find", mock.Anything, 1).Return(&models.Scene{ID: 1}, nil).Once()
	db.Scene.On("Find", mock.Anything, 2).Return(nil, nil).Once()
	db.Scene.On("Find", mock.Anything, 3).Return(&models.Scene{ID: 3}, nil).Once()
	scraper := &identifyTestScraper{}
	j := &IdentifyJob{}
	status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		return j.identifyAllScenes(ctx, []identify.ScraperSource{{Scraper: scraper}})
	})
	require.Equal(t, job.StatusFinished, status.Status)
	require.Nil(t, status.Error)
	require.Equal(t, []int{1, 3}, scraper.ids)
	require.Equal(t, 3, *status.Processed)
	require.Equal(t, 3, *status.Total)
	require.Equal(t, 1.0, status.Progress)
	db.Scene.AssertCalled(t, "Find", mock.Anything, 2)
	db.Scene.AssertNotCalled(t, "FindMany", mock.Anything, ids)
}

func TestIdentifyAllScenesQueryFailure(t *testing.T) {
	db := identifyTestRepository(t)
	cause := errors.New("database permission denied")
	db.Scene.On("Query", mock.Anything, mock.Anything).Return(nil, cause).Once()
	j := &IdentifyJob{}
	var got error
	status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		got = j.identifyAllScenes(ctx, nil)
		return got
	})
	require.ErrorIs(t, got, cause)
	require.Equal(t, job.StatusFailed, status.Status)
	require.NotNil(t, status.Error)
	db.Scene.AssertExpectations(t)
}

type identifyWarningLogger struct {
	logger.LoggerImpl
	warnings []string
}

func (l *identifyWarningLogger) Warnf(format string, args ...interface{}) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func TestIdentifySelectionsMissingScenes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		ids     []int
		want    []int
		skipped int
	}{
		{"all valid", []int{3, 1}, []int{3, 1}, 0},
		{"missing first", []int{2, 1, 3}, []int{1, 3}, 1},
		{"missing middle", []int{1, 2, 3}, []int{1, 3}, 1},
		{"missing last", []int{1, 3, 2}, []int{1, 3}, 1},
		{"all missing", []int{2, 4}, nil, 2},
		{"repeated selection", []int{1, 1, 2}, []int{1, 1}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := identifyTestRepository(t)
			db.Scene.On("Find", mock.Anything, mock.Anything).Return(func(_ context.Context, id int) *models.Scene {
				if id == 2 || id == 4 {
					return nil
				}
				return &models.Scene{ID: id}
			}, nil).Times(len(tt.ids))
			previousLogger := logger.Logger
			logs := &identifyWarningLogger{LoggerImpl: &logger.BasicLogger{}}
			logger.Logger = logs
			t.Cleanup(func() { logger.Logger = previousLogger })
			scraper := &identifyTestScraper{}
			j := &IdentifyJob{}
			status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
				j.progress = p
				return j.identifyScenes(ctx, tt.ids, []identify.ScraperSource{{Scraper: scraper}})
			})
			require.Equal(t, job.StatusFinished, status.Status)
			require.Nil(t, status.Error)
			require.Equal(t, tt.want, scraper.ids)
			require.Equal(t, len(tt.ids), *status.Processed)
			require.Equal(t, len(tt.ids), *status.Total)
			require.Equal(t, 1.0, status.Progress)
			require.Len(t, logs.warnings, tt.skipped)
			for _, warning := range logs.warnings {
				require.Contains(t, warning, "scene no longer exists")
			}
			db.Scene.AssertExpectations(t)
		})
	}
}

func TestIdentifyLookupFailurePreservesPartialProgress(t *testing.T) {
	db := identifyTestRepository(t)
	cause := errors.New("database permission denied")
	db.Scene.On("Find", mock.Anything, 1).Return(&models.Scene{ID: 1}, nil).Once()
	db.Scene.On("Find", mock.Anything, 2).Return(nil, cause).Once()
	scraper := &identifyTestScraper{}
	j := &IdentifyJob{}
	var got error
	status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		got = j.identifyScenes(ctx, []int{1, 2, 3}, []identify.ScraperSource{{Scraper: scraper}})
		return got
	})
	require.ErrorIs(t, got, cause)
	require.Contains(t, got.Error(), "finding scene id 2")
	require.Equal(t, job.StatusFailed, status.Status)
	require.Equal(t, []int{1}, scraper.ids)
	require.Equal(t, 1, *status.Processed)
	require.Equal(t, 3, *status.Total)
	require.InDelta(t, 1.0/3, status.Progress, 0.0001)
	db.Scene.AssertNotCalled(t, "Find", mock.Anything, 3)
	db.Scene.AssertExpectations(t)
}

func TestIdentifyAllScenesSnapshotsSelection(t *testing.T) {
	db := identifyTestRepository(t)
	result := models.NewSceneQueryResult(db.Scene)
	for id := 1; id <= 1001; id++ {
		result.IDs = append(result.IDs, id)
	}
	// Total is the ID snapshot, even if a separate count would already be stale.
	result.Count = 2000
	db.Scene.On("Query", mock.Anything, mock.MatchedBy(func(o models.SceneQueryOptions) bool {
		return !o.Count && o.FindFilter != nil && *o.FindFilter.PerPage == models.PerPageAll &&
			*o.FindFilter.Sort == "path" && o.SceneFilter != nil && !*o.SceneFilter.Organized &&
			o.SceneFilter.Path != nil && o.SceneFilter.Path.Value == "/fixture/%"
	})).Return(result, nil).Once()
	db.Scene.On("Find", mock.Anything, mock.Anything).Return(func(_ context.Context, id int) *models.Scene {
		return &models.Scene{ID: id}
	}, nil).Times(len(result.IDs))
	scraper := &identifyTestScraper{}
	j := &IdentifyJob{input: identify.Options{Paths: []string{"/fixture"}}}
	status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		return j.identifyAllScenes(ctx, []identify.ScraperSource{{Scraper: scraper}})
	})
	require.Equal(t, job.StatusFinished, status.Status)
	require.Equal(t, result.IDs, scraper.ids)
	require.Equal(t, 1001, *status.Processed)
	require.Equal(t, 1001, *status.Total)
	db.Scene.AssertExpectations(t)
}

func TestIdentifyCancellationStopsSelection(t *testing.T) {
	db := identifyTestRepository(t)
	db.Scene.On("Find", mock.Anything, 1).Return(&models.Scene{ID: 1}, nil).Once()
	m := job.NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	started := make(chan struct{})
	var id int
	scraper := &identifyTestScraper{after: func(int) {
		<-started // Publish the job ID before cancellation from the worker.
		m.CancelJob(id)
	}}
	j := &IdentifyJob{}
	id = m.Add(context.Background(), "Identify cancellation", job.MakeJobExec(func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		return j.identifyScenes(ctx, []int{1, 2, 3}, []identify.ScraperSource{{Scraper: scraper}})
	}))
	close(started)
	status := waitMarkerJob(t, m, id)
	require.Equal(t, job.StatusCancelled, status.Status)
	require.Nil(t, status.Error)
	require.Equal(t, []int{1}, scraper.ids)
	require.Equal(t, 1, *status.Processed)
	require.Equal(t, 3, *status.Total)
	db.Scene.AssertNotCalled(t, "Find", mock.Anything, 2)
	db.Scene.AssertExpectations(t)
}

func TestIdentifyCancelledBeforeLookup(t *testing.T) {
	db := identifyTestRepository(t)
	j := &IdentifyJob{}
	status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return j.identifyScenes(ctx, []int{1, 2}, nil)
	})
	require.Equal(t, 0, *status.Processed)
	require.Equal(t, 2, *status.Total)
	require.Zero(t, status.Progress)
	db.Scene.AssertNotCalled(t, "Find", mock.Anything, mock.Anything)
}

func TestIdentifyExecuteMissingAndInvalidSelections(t *testing.T) {
	for _, tt := range []struct {
		name      string
		ids       []string
		cause     error
		wantError string
	}{
		{"missing", []string{"2"}, nil, ""},
		{"invalid", []string{"invalid"}, nil, "invalid scene IDs"},
		{"database failure", []string{"2"}, errors.New("database permission denied"), "finding scene id 2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := identifyTestRepository(t)
			if tt.name != "invalid" {
				db.Scene.On("Find", mock.Anything, 2).Return(nil, tt.cause).Once()
			}
			index := 0
			j := &IdentifyJob{
				input:      identify.Options{SceneIDs: tt.ids, Sources: []*identify.Source{{Source: &scraper.Source{StashBoxIndex: &index}}}},
				stashBoxes: []*models.StashBox{{Endpoint: "http://unused.invalid"}},
			}
			var got error
			status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
				got = j.Execute(ctx, p)
				return got
			})
			if tt.wantError == "" {
				require.NoError(t, got)
				require.Equal(t, job.StatusFinished, status.Status)
				require.Equal(t, 1, *status.Processed)
				require.Equal(t, 1, *status.Total)
			} else {
				require.Contains(t, got.Error(), tt.wantError)
				require.Contains(t, got.Error(), "error encountered while identifying scenes")
				require.Equal(t, job.StatusFailed, status.Status)
				if tt.cause != nil {
					require.ErrorIs(t, got, tt.cause)
				}
			}
			db.Scene.AssertExpectations(t)
		})
	}
}

func TestIdentifyRenameMissingScene(t *testing.T) {
	db := identifyTestRepository(t)
	db.Scene.On("Find", mock.Anything, 2).Return(nil, nil).Once()
	db.Scene.On("Find", mock.Anything, 3).Return(&models.Scene{ID: 3}, nil).Once()
	db.Scene.On("GetFiles", mock.Anything, 3).Return([]*models.VideoFile{}, nil).Once()
	j := &IdentifyJob{scenesToRename: []int{2, 3}}
	status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		j.processRenames(ctx)
		return nil
	})
	if status.Error != nil {
		t.Log("deferred rename failure:", *status.Error)
	}
	require.Equal(t, job.StatusFinished, status.Status)
	require.Nil(t, status.Error)
	db.Scene.AssertNotCalled(t, "GetFiles", mock.Anything, 2)
	db.Scene.AssertExpectations(t)
}
