package manager

import (
	"context"
	"encoding/json"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stashapp/stash/pkg/scene/generate"
	"github.com/stretchr/testify/mock"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func waitPreviewReplay(t *testing.T, dir, reference string) *diagnostics.PreviewReplayRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := diagnostics.FindPreviewReplay(dir, reference)
		if err == nil {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("queued mapping was not retained", err)
		}
		runtime.Gosched()
	}
}

func TestPreviewReplayManagerEarlyHeaderMapsInsideConfigVolume(t *testing.T) {
	mgr, input, _, _ := metadataFixture(t)
	previous := instance
	instance = mgr
	t.Cleanup(func() { instance = previous })
	configDir := t.TempDir()
	mgr.Config.SetConfigFile(filepath.Join(configDir, "config.yml"))
	mgr.Config.SetInterface(config.PreviewGenerationBackend, "vaapi")
	mgr.FFProbe = nil
	fileID := models.FileID(29)
	start, end := 4.0, 8.0
	vrMode := models.VRModeTB360
	g := &generate.Generator{IntelPreviews: mgr.Config.GetIntelPreviewGeneration()}
	task := GeneratePreviewTask{Scene: models.Scene{ID: 17, Path: input, PrimaryFileID: &fileID, Checksum: "fixture", StartPoint: &start, EndPoint: &end, VRMode: &vrMode}, Overwrite: true, generator: g, Options: generate.PreviewOptions{Segments: 2, SegmentDuration: 0.25, ExcludeStart: "12.5%"}, fileNamingAlgorithm: models.HashAlgorithmMd5}
	err := task.Start(context.Background())
	r, ok := diagnostics.PreviewFailureSnapshot(err)
	if !ok || r.Command.Stage != "header" || r.Command.SeekSeconds != nil || r.Request.DurationSeconds != nil || r.Request.LimitStart == nil || *r.Request.LimitStart != 4 || *r.Request.ExcludeStart != 12.5 || r.Request.Projection != string(models.VRModeTB360) || r.Retention != "queued" || r.Retained {
		t.Fatal("header/request facts", r, err)
	}
	stored := waitPreviewReplay(t, filepath.Join(configDir, "preview-replay"), r.Reference)
	if stored.SceneID != 17 || stored.FileID != 29 || !stored.Replay.SourceResolved || stored.Replay.Request.Projection != string(models.VRModeTB360) {
		t.Fatal("source/projection mapping", stored)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), input) || strings.Contains(string(data), "scene_id") || strings.Contains(string(data), "file_id") {
		t.Fatal("private source in exported facts", string(data))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(mgr.Paths.Generated.Tmp), "preview-replay")); !os.IsNotExist(err) {
		t.Fatal("retention used replaceable generated storage", err)
	}
}

func TestPreviewReplayGenerateJobAndScanWatcherResolveOwnSource(t *testing.T) {
	for _, mode := range []string{"job", "sequential", "queued", "watcher"} {
		t.Run(mode, func(t *testing.T) {
			received := generationTelemetryReceiver(t)
			mgr, scene, file := scanPreviewFixture(t)
			configDir := t.TempDir()
			mgr.Config.SetConfigFile(filepath.Join(configDir, "config.yml"))
			mgr.Config.SetInterface(config.PreviewGenerationBackend, "vaapi")
			mgr.FFProbe = nil
			db := mocks.NewDatabase()
			mgr.Repository = models.Repository{TxnManager: db, Scene: db.Scene}
			m := job.NewManager()
			t.Cleanup(func() { m.StopAndWait(time.Second) })
			var id int
			if mode == "job" {
				scene.Files = models.RelatedVideoFiles{}
				db.Scene.On("FindMany", mock.Anything, []int{1}).Return([]*models.Scene{scene}, nil)
				db.Scene.On("GetFiles", mock.Anything, 1).Return([]*models.VideoFile{file}, nil)
				j := &GenerateJob{repository: mgr.Repository, input: GenerateMetadataInput{SceneIDs: []string{"1"}, Previews: true, Overwrite: true}}
				id = m.Add(context.Background(), "private preview", j)
			} else {
				id = m.Add(context.Background(), "scan previews", job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
					opts := config.ScanMetadataOptions{ScanGeneratePreviews: true}
					if mode == "watcher" {
						mgr.Config.SetInterface(config.DefaultScanSettings, opts)
						_ = (&watcherSceneGenerator{paths: mgr.Paths}).Generate(ctx, scene, file)
					} else {
						queue := job.NewTaskQueue(ctx, progress, 10, 1)
						g := sceneGenerators{input: ScanMetadataInput{ScanMetadataOptions: opts}, paths: mgr.Paths, progress: progress, taskQueue: queue, sequentialScanning: mode == "sequential", fileNamingAlgorithm: models.HashAlgorithmMd5}
						_ = g.Generate(ctx, scene, file)
						queue.Close()
					}
					return nil
				}))
			}
			result := waitMarkerJob(t, m, id)
			if mode == "job" && result.Status != job.StatusFailed {
				t.Fatal("job failure changed", result)
			}
			if mode != "job" && result.Status != job.StatusFinished {
				t.Fatal("scan abort changed", result)
			}
			events := received()
			if len(events) != 1 {
				t.Fatal("duplicate or absent failure", events)
			}
			data, err := json.Marshal(events[0].Properties["preview_replay"])
			if err != nil {
				t.Fatal(err)
			}
			var replay diagnostics.PreviewReplay
			if err := json.Unmarshal(data, &replay); err != nil {
				t.Fatal(err)
			}
			if replay.Reference == "" || replay.Command.Stage != "header" || !replay.SourceResolved || replay.Retention != "queued" {
				t.Fatal("task boundary lost own producer", string(data), events)
			}
			stored := waitPreviewReplay(t, filepath.Join(configDir, "preview-replay"), replay.Reference)
			if stored.SceneID != scene.ID || stored.FileID != int(file.ID) || stored.JobCorrelation == "" {
				t.Fatal("local source/job mapping", stored)
			}
			db.Scene.AssertExpectations(t)
		})
	}
}
