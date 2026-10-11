//go:build integration

package manager

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
	"github.com/stashapp/stash/pkg/txn"
	"github.com/stretchr/testify/require"
)

type identifyDeletingQuery struct {
	models.SceneReaderWriter
	db       *sqlite.Database
	selected []int
}

func (q *identifyDeletingQuery) Query(ctx context.Context, options models.SceneQueryOptions) (*models.SceneQueryResult, error) {
	result, err := q.SceneReaderWriter.Query(ctx, options)
	if err != nil {
		return nil, err
	}
	q.selected = append([]int(nil), result.IDs...)
	if len(result.IDs) > 1 {
		// Deterministically interleave deletion after selection and before
		// hydration, at the same boundary that failed on v0.2.4.
		err = txn.WithTxn(ctx, q.db, func(ctx context.Context) error {
			return q.SceneReaderWriter.Destroy(ctx, result.IDs[1])
		})
	}
	return result, err
}

func TestIdentifySQLiteDeletionAfterSelection(t *testing.T) {
	cfg := config.InitializeEmpty()
	db := sqlite.NewDatabase()
	db.SetBlobStoreOptions(sqlite.BlobStoreOptions{UseDatabase: true})
	require.NoError(t, db.Open(filepath.Join(t.TempDir(), "identify.sqlite")))
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, txn.WithTxn(context.Background(), db, func(ctx context.Context) error {
		for i := 0; i < 1001; i++ {
			s := models.NewScene()
			if err := db.Scene.Create(ctx, &s, nil); err != nil {
				return err
			}
		}
		return nil
	}))
	reader := &identifyDeletingQuery{SceneReaderWriter: db.Scene, db: db}
	previous := instance
	instance = &Manager{Config: cfg, Repository: models.Repository{TxnManager: db, Scene: reader}}
	t.Cleanup(func() { instance = previous })
	var interleaveErr error
	scraper := &identifyTestScraper{after: func(id int) {
		if id != reader.selected[0] {
			return
		}
		// A scene in the next hydration batch disappears while the first is being checked. Also
		// move that first scene out of the original unorganized selection.
		interleaveErr = txn.WithTxn(context.Background(), db, func(ctx context.Context) error {
			if err := db.Scene.Destroy(ctx, reader.selected[1000]); err != nil {
				return err
			}
			_, err := db.Scene.UpdatePartial(ctx, reader.selected[0], models.ScenePartial{Organized: models.NewOptionalBool(true)})
			return err
		})
	}}
	j := &IdentifyJob{}
	status := runIdentifyTestJob(t, context.Background(), func(ctx context.Context, p *job.Progress) error {
		j.progress = p
		return instance.Repository.WithDB(ctx, func(ctx context.Context) error {
			return j.identifyAllScenes(ctx, []identify.ScraperSource{{Scraper: scraper}})
		})
	})
	require.NoError(t, interleaveErr)
	require.Len(t, reader.selected, 1001)
	require.Equal(t, job.StatusFinished, status.Status)
	require.Nil(t, status.Error)
	want := append([]int{reader.selected[0]}, reader.selected[2:1000]...)
	require.Equal(t, want, scraper.ids)
	require.Equal(t, 1001, *status.Processed)
	require.Equal(t, 1001, *status.Total)
	require.Equal(t, 1.0, status.Progress)
}
