package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stretchr/testify/mock"
)

func scanPreviewFixture(t *testing.T) (*Manager, *models.Scene, *models.VideoFile) {
	t.Helper()
	mgr, input, _, _ := metadataFixture(t)
	previous := instance
	instance = mgr
	t.Cleanup(func() { instance = previous })
	mgr.Config.SetInterface(config.PreviewGenerationBackend, "software")
	mgr.Config.SetInterface(config.ParallelTasks, 1)
	file := &models.VideoFile{BaseFile: &models.BaseFile{ID: 1, Path: input}, Width: 64, Duration: 1}
	scene := &models.Scene{ID: 1, Path: input, Title: "private scan title", Checksum: "fixture", OSHash: "fixture", HasPreview: true, Files: models.NewRelatedVideoFiles([]*models.VideoFile{file})}
	if err := os.MkdirAll(mgr.Paths.Generated.Tmp, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "ffmpeg")
	// Successful cover sibling, failing preview encodes, no actual media workload.
	script := `#!/bin/sh
if [ "$1" = '-version' ]; then echo 'ffmpeg version 7.1'; exit 0; fi
for output do :; done
case "$output" in
 *.jpg) printf 'synthetic cover' > "$output"; exit 0;;
esac
echo 'controlled preview encode failure' >&2
exit 23
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	mgr.FFMpeg = ffmpeg.NewEncoder(binary)
	return mgr, scene, file
}

func TestScanPreviewFailureKeepsSuccessfulSiblings(t *testing.T) {
	for _, mode := range []string{"sequential", "queued", "watcher"} {
		for _, failure := range []string{"metadata", "mp4", "webp", "success"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				received := generationTelemetryReceiver(t)
				mgr, scene, file := scanPreviewFixture(t)
				opts := config.ScanMetadataOptions{ScanGeneratePreviews: true, ScanGenerateCovers: true}
				if failure == "metadata" {
					mgr.FFProbe = nil
				}
				if failure == "webp" || failure == "success" {
					path := mgr.Paths.Scene.GetVideoPreviewPath(scene.Checksum)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("existing preview"), 0600); err != nil {
						t.Fatal(err)
					}
					opts.ScanGenerateImagePreviews = failure == "webp"
				}
				db := mocks.NewDatabase()
				db.Scene.On("HasCover", mock.Anything, scene.ID).Return(false, nil).Once()
				db.Scene.On("UpdateCover", mock.Anything, scene.ID, []byte("synthetic cover")).Return(nil).Once()
				db.Scene.On("UpdatePartial", mock.Anything, scene.ID, mock.Anything).Return(scene, nil).Once()
				mgr.Repository = models.Repository{TxnManager: db, Scene: db.Scene}
				m := job.NewManager()
				t.Cleanup(func() { m.StopAndWait(time.Second) })
				id := m.Add(context.Background(), "scan previews", job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
					var err error
					if mode == "watcher" {
						mgr.Config.SetInterface(config.DefaultScanSettings, opts)
						err = (&watcherSceneGenerator{paths: mgr.Paths}).Generate(ctx, scene, file)
					} else {
						queue := job.NewTaskQueue(ctx, progress, 10, 1)
						g := sceneGenerators{input: ScanMetadataInput{ScanMetadataOptions: opts}, paths: mgr.Paths, progress: progress, taskQueue: queue, sequentialScanning: mode == "sequential", fileNamingAlgorithm: models.HashAlgorithmMd5}
						err = g.Generate(ctx, scene, file)
						queue.Close()
					}
					if mode == "queued" || failure == "success" {
						if err != nil {
							t.Errorf("unexpected generation error: %v", err)
						}
					} else if err == nil {
						t.Error("synchronous preview failure was swallowed")
					}
					// ScanHandler logs post-commit generation errors and continues scanning.
					// Exercise the same nonfatal return so telemetry cannot change job status.
					return nil
				}))
				result := waitMarkerJob(t, m, id)
				if result.Status != job.StatusFinished || result.Error != nil {
					t.Fatalf("preview failure aborted scan: %+v", result)
				}
				db.Scene.AssertExpectations(t) // Cover after the failed preview completed and saved.
				events := received()
				want := 1
				if failure == "success" {
					want = 0
				}
				if len(events) != want {
					t.Fatalf("exceptions=%d, want %d: %+v", len(events), want, events)
				}
				if want == 1 {
					if events[0].Properties["generation_workload"] != "preview" || events[0].Properties["failure_origin"] != "generation" {
						t.Fatalf("wrong failure context: %+v", events)
					}
					if events[0].Properties["job_correlation"] == "unavailable" {
						t.Fatal("job correlation lost")
					}
					encoded, _ := json.Marshal(events)
					for _, private := range []string{scene.Path, scene.Title} {
						if strings.Contains(string(encoded), private) {
							t.Fatalf("telemetry leaked %q", private)
						}
					}
				}
				if failure == "webp" || failure == "success" {
					data, err := os.ReadFile(mgr.Paths.Scene.GetVideoPreviewPath(scene.Checksum))
					if err != nil || string(data) != "existing preview" {
						t.Fatal("successful asset changed", err)
					}
				}
			})
		}
	}
}

func TestScanPreviewCancellationEmitsNoException(t *testing.T) {
	for _, mode := range []string{"sequential", "queued", "watcher"} {
		for _, cancellation := range []string{"cancelled", "deadline"} {
			t.Run(mode+"/"+cancellation, func(t *testing.T) {
				received := generationTelemetryReceiver(t)
				mgr, scene, file := scanPreviewFixture(t)
				// Keep the only admission slot occupied so cancellation happens in a real
				// preview task, including queued execution, before any metadata subprocess.
				release, err := mgr.Config.GetGenerationBudget().Acquire(context.Background(), generationbudget.CPU)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				m := job.NewManager()
				t.Cleanup(func() { m.StopAndWait(time.Second) })
				id := m.Add(context.Background(), "cancelled scan preview", job.MakeJobExec(func(jobCtx context.Context, progress *job.Progress) error {
					ctx, cancel := context.WithTimeout(jobCtx, 50*time.Millisecond)
					want := context.DeadlineExceeded
					if cancellation == "cancelled" {
						cancel()
						ctx, cancel = context.WithCancel(jobCtx)
						timer := time.AfterFunc(50*time.Millisecond, cancel)
						defer timer.Stop()
						want = context.Canceled
					}
					defer cancel()
					opts := config.ScanMetadataOptions{ScanGeneratePreviews: true}
					var err error
					if mode == "watcher" {
						mgr.Config.SetInterface(config.DefaultScanSettings, opts)
						err = (&watcherSceneGenerator{paths: mgr.Paths}).Generate(ctx, scene, file)
					} else {
						queue := job.NewTaskQueue(ctx, progress, 10, 1)
						g := sceneGenerators{input: ScanMetadataInput{ScanMetadataOptions: opts}, paths: mgr.Paths, progress: progress, taskQueue: queue, sequentialScanning: mode == "sequential", fileNamingAlgorithm: models.HashAlgorithmMd5}
						err = g.Generate(ctx, scene, file)
						queue.Close()
					}
					if mode != "queued" && !errors.Is(err, want) {
						t.Errorf("cancellation lost: %v", err)
					}
					return nil
				}))
				if result := waitMarkerJob(t, m, id); result.Status != job.StatusFinished {
					t.Fatalf("changed scan cancellation handling: %+v", result)
				}
				if events := received(); len(events) != 0 {
					t.Fatalf("cancellation emitted exception: %+v", events)
				}
			})
		}
	}
}

func TestScanClipPreviewCapturesLegacyReportOnce(t *testing.T) {
	for _, mode := range []string{"sequential", "queued", "watcher"} {
		t.Run(mode, func(t *testing.T) {
			received := generationTelemetryReceiver(t)
			mgr, scene, file := scanPreviewFixture(t)
			image := &models.Image{Path: scene.Path, Checksum: "fixture"}
			m := job.NewManager()
			t.Cleanup(func() { m.StopAndWait(time.Second) })
			id := m.Add(context.Background(), "scan clip", job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
				opts := config.ScanMetadataOptions{ScanGenerateClipPreviews: true}
				if mode == "watcher" {
					mgr.Config.SetInterface(config.DefaultScanSettings, opts)
					return (&watcherImageGenerator{paths: mgr.Paths}).Generate(ctx, image, file)
				}
				queue := job.NewTaskQueue(ctx, progress, 10, 1)
				g := imageGenerators{input: ScanMetadataInput{ScanMetadataOptions: opts}, paths: mgr.Paths, progress: progress, taskQueue: queue, sequentialScanning: mode == "sequential"}
				err := g.Generate(ctx, image, file)
				queue.Close()
				return err
			}))
			if result := waitMarkerJob(t, m, id); result.Status != job.StatusFinished {
				t.Fatalf("legacy clip status changed: %+v", result)
			}
			events := received()
			if len(events) != 1 || events[0].Properties["generation_workload"] != "clip_preview" {
				t.Fatalf("legacy report missing or duplicated: %+v", events)
			}
		})
	}
}

type reportingPreviewTask struct{ err error }

func (*reportingPreviewTask) GetDescription() string { return "report and return" }
func (t *reportingPreviewTask) Start(ctx context.Context) error {
	reportGenerationFailure(ctx, t.err)
	return t.err
}

func TestScanPreviewCaptureDoesNotDuplicateReturnedReport(t *testing.T) {
	received := generationTelemetryReceiver(t)
	scanPreviewFixture(t)
	ctx := diagnostics.WithState(context.Background())
	task := &reportingPreviewTask{err: fmt.Errorf("preview operation: %w", errors.New("controlled failure"))}
	err := startScanPreviewTask(ctx, task)
	if !errors.Is(err, task.err) {
		t.Fatal("returned error identity lost")
	}
	analytics.CaptureJobFailure(ctx, err, "", "scan")
	if events := received(); len(events) != 1 {
		t.Fatalf("task/job captured duplicate errors: %+v", events)
	}
}

func TestScanPreviewJobCancellationStatus(t *testing.T) {
	for _, mode := range []string{"sequential", "queued", "watcher"} {
		t.Run(mode, func(t *testing.T) {
			received := generationTelemetryReceiver(t)
			mgr, scene, file := scanPreviewFixture(t)
			m := job.NewManager()
			t.Cleanup(func() { m.StopAndWait(time.Second) })
			started, proceed := make(chan struct{}), make(chan struct{})
			id := m.Add(context.Background(), "cancel scan", job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
				close(started)
				<-proceed
				opts := config.ScanMetadataOptions{ScanGeneratePreviews: true}
				if mode == "watcher" {
					mgr.Config.SetInterface(config.DefaultScanSettings, opts)
					return (&watcherSceneGenerator{paths: mgr.Paths}).Generate(ctx, scene, file)
				}
				queue := job.NewTaskQueue(ctx, progress, 10, 1)
				g := sceneGenerators{input: ScanMetadataInput{ScanMetadataOptions: opts}, paths: mgr.Paths, progress: progress, taskQueue: queue, sequentialScanning: mode == "sequential", fileNamingAlgorithm: models.HashAlgorithmMd5}
				err := g.Generate(ctx, scene, file)
				queue.Close()
				return err
			}))
			select {
			case <-started:
			case <-time.After(time.Second):
				close(proceed)
				t.Fatal("scan job did not start")
			}
			m.CancelJob(id)
			close(proceed)
			result := waitMarkerJob(t, m, id)
			if result.Status != job.StatusCancelled || result.Error != nil {
				t.Fatalf("preview return changed cancellation status: %+v", result)
			}
			if events := received(); len(events) != 0 {
				t.Fatalf("cancelled job emitted exception: %+v", events)
			}
		})
	}
}
