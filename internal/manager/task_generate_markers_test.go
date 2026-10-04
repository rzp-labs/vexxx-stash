package manager

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stashapp/stash/pkg/models/paths"
	"github.com/stashapp/stash/pkg/scene/generate"
	"github.com/stretchr/testify/mock"
)

// Real task/job entry points with synthetic command fixtures exercise persistence
// and failure reporting without treating mock output as media or GPU proof.
func markerTaskFixture(t *testing.T, backend, device string) (*GenerateMarkersTask, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "ffmpeg")
	started := filepath.Join(dir, "started")
	block := filepath.Join(dir, "block")
	script := `#!/bin/sh
if [ "$1" = '-version' ]; then echo 'ffmpeg version 7.1'; exit 0; fi
for last do :; done
if [ "$last" = '-' ]; then exit 0; fi
: > '` + started + `'
if [ -f '` + block + `' ]; then exec sleep 30; fi
printf fixture-output > "$last"
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "ffprobe")
	probeScript := `#!/bin/sh
if [ "$1" = '-version' ]; then echo 'ffprobe version 7.1'; exit 0; fi
for arg do
if [ "$arg" = '-count_packets' ]; then
printf '%s' '{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"1"}]}'
exit 0
fi
done
printf '%s' '{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":640,"height":360,"sample_aspect_ratio":"1:1","r_frame_rate":"30/1","avg_frame_rate":"30/1","duration":"10"}]}'
`
	if err := os.WriteFile(probe, []byte(probeScript), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.InitializeEmpty()
	cfg.SetInterface(config.MarkerGenerationBackend, backend)
	cfg.SetInterface(config.GenerationDevice, device)
	cfg.SetInterface(config.GenerationBudgetEnabled, true)
	cfg.SetInterface(config.GenerationMaxProcesses, 1)
	cfg.SetInterface(config.GenerationMaxGPUProcesses, 1)
	cfg.SetInterface(config.GenerationThreads, 1)
	cfg.SetInterface(config.ParallelTasks, 2)
	cfg.SetInterface(config.NativeMarkerGeneration, false)
	p := paths.NewPaths(filepath.Join(dir, "generated"), filepath.Join(dir, "blobs"))
	mgr := &Manager{Config: cfg, FFMpeg: ffmpeg.NewEncoder(binary), FFProbe: ffmpeg.NewFFProbe(probe), ReadLockManager: fsutil.NewReadLockManager(), Paths: &p}
	previous := instance
	instance = mgr
	t.Cleanup(func() { instance = previous })
	fileID := models.FileID(1)
	scene := &models.Scene{ID: 1, PrimaryFileID: &fileID, Checksum: "fixture", OSHash: "fixture"}
	file := &models.VideoFile{BaseFile: &models.BaseFile{ID: fileID, Path: "synthetic.mp4"}, Width: 640, Height: 360, Duration: 10}
	marker := &models.SceneMarker{ID: 1, SceneID: 1, Seconds: 1}
	db := mocks.NewDatabase()
	db.Scene.On("Find", mock.Anything, 1).Return(scene, nil)
	db.File.On("Find", mock.Anything, fileID).Return([]models.File{file}, nil)
	db.SceneMarker.On("FindMany", mock.Anything, []int{1}).Return([]*models.SceneMarker{marker}, nil)
	db.SceneMarker.On("FindBySceneID", mock.Anything, 1).Return([]*models.SceneMarker{marker}, nil)
	g := &generate.Generator{Encoder: mgr.FFMpeg, Probe: mgr.FFProbe, IntelMarker: cfg.GetIntelMarkerGeneration(), FFMpegConfig: cfg, LockManager: mgr.ReadLockManager, MarkerPaths: p.SceneMarkers, Overwrite: true}
	task := &GenerateMarkersTask{repository: models.Repository{TxnManager: db, Scene: db.Scene, File: db.File, SceneMarker: db.SceneMarker}, Marker: marker, VideoPreview: true, ImagePreview: true, generator: g, fileNamingAlgorithm: models.HashAlgorithmMd5}
	return task, started, block
}

func assertMarkerTempEmpty(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(instance.Paths.Generated.Tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary artifacts leaked: %v %v", entries, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := instance.Config.GetGenerationBudget().Acquire(ctx, generationbudget.GPU)
	if err != nil {
		t.Fatal("generation leaked admission", err)
	}
	release()
}

func TestSingleMarkerCreatesDestinationAndPersists(t *testing.T) {
	for _, c := range []struct{ name, backend, device, actual, stage string }{
		{"cpu", "software", "/dev/dri/renderD128", "software", ""},
		{"intel-quality-fallback", "qsv", "/dev/dri/renderD128", "software", "quality"},
		{"intel-device-fallback", "vaapi", "/dev/dri/renderD99999", "software", "device"},
		{"intel-success-mocked-commands", "vaapi", "/dev/dri/renderD128", "vaapi", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.actual == "vaapi" {
				if err := ffmpeg.ValidateIntelDevice(c.device); err != nil {
					t.Skip("Mock Intel command-success persistence needs an existing authorized render device; run this test in CT102")
				}
			}
			task, _, _ := markerTaskFixture(t, c.backend, c.device)
			video := task.generator.MarkerPaths.GetVideoPreviewPath("fixture", 1)
			if _, err := os.Stat(filepath.Dir(video)); !os.IsNotExist(err) {
				t.Fatal("fixture destination already exists", err)
			}
			var diagnostics []ffmpeg.IntelGenerationDiagnostic
			task.generator.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := task.Start(ctx); err != nil {
				t.Fatal(err)
			}
			for _, output := range []string{video, task.generator.MarkerPaths.GetWebpPreviewPath("fixture", 1)} {
				if data, err := os.ReadFile(output); err != nil || string(data) != "fixture-output" {
					t.Fatalf("missing persisted artifact %s: %q %v", output, data, err)
				}
			}
			if len(diagnostics) != 1 || diagnostics[0].Actual != c.actual || diagnostics[0].Stage != c.stage {
				t.Fatal(diagnostics)
			}
			assertMarkerTempEmpty(t)
		})
	}
}

func waitMarkerJob(t *testing.T, m *job.Manager, id int) *job.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j := m.GetJob(id)
		if j != nil && j.EndTime != nil {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("marker job did not drain")
	return nil
}

func TestMarkerGenerationJobStatusAndPartialAssets(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed-save"}[fail], func(t *testing.T) {
			task, _, _ := markerTaskFixture(t, "software", "/dev/dri/renderD128")
			video := task.generator.MarkerPaths.GetVideoPreviewPath("fixture", 1)
			if fail {
				// An existing directory at the final filename forces SaveMove to
				// fail on every platform without depending on permission/UID rules.
				if err := os.MkdirAll(video, 0700); err != nil {
					t.Fatal(err)
				}
			}
			m := job.NewManager()
			t.Cleanup(func() { m.StopAndWait(time.Second) })
			j := &GenerateJob{repository: task.repository, input: GenerateMetadataInput{MarkerIDs: []string{"1"}, Markers: true, MarkerImagePreviews: true, Overwrite: true}}
			id := m.Add(context.Background(), "synthetic marker", j)
			result := waitMarkerJob(t, m, id)
			if fail {
				if result.Status != job.StatusFailed || result.Error == nil || !strings.Contains(*result.Error, "marker 1 video") || !strings.Contains(*result.Error, "moving") {
					t.Fatalf("save failure reported as success: %+v", result)
				}
			} else if result.Status != job.StatusFinished || result.Error != nil {
				t.Fatalf("success reported as failure: %+v", result)
			}
			// A failed video does not prevent the independent image asset from
			// persisting, and the successful output is not rolled back.
			webp := task.generator.MarkerPaths.GetWebpPreviewPath("fixture", 1)
			if data, err := os.ReadFile(webp); err != nil || string(data) != "fixture-output" {
				t.Fatalf("successful partial image lost: %q %v", data, err)
			}
			assertMarkerTempEmpty(t)
		})
	}
}

func TestMarkerDirectoryFailureStopsBeforeEncoding(t *testing.T) {
	task, started, _ := markerTaskFixture(t, "software", "/dev/dri/renderD128")
	if err := os.MkdirAll(filepath.Dir(instance.Paths.Generated.Markers), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instance.Paths.Generated.Markers, []byte("blocked-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := task.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "creating marker directory") {
		t.Fatal("directory failure ignored", err)
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatal("encoding started despite directory failure", err)
	}
}

func TestMarkerCancellationDrainsAndAllowsNextJob(t *testing.T) {
	task, started, block := markerTaskFixture(t, "software", "/dev/dri/renderD128")
	if err := os.WriteFile(block, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	m := job.NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	j := &GenerateJob{repository: task.repository, input: GenerateMetadataInput{MarkerIDs: []string{"1"}, Markers: true, MarkerImagePreviews: true, Overwrite: true}}
	id := m.Add(context.Background(), "cancelled marker", j)
	waitMetadataSignalFile := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(waitMetadataSignalFile) {
			t.Fatal("marker encoding did not start")
		}
		runtime.Gosched()
	}
	m.CancelJob(id)
	result := waitMarkerJob(t, m, id)
	if result.Status != job.StatusCancelled || result.Error != nil {
		t.Fatalf("explicit cancellation reported as failure: %+v", result)
	}
	assertMarkerTempEmpty(t)
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	id = m.Add(context.Background(), "fresh marker", &GenerateJob{repository: task.repository, input: j.input})
	result = waitMarkerJob(t, m, id)
	if result.Status != job.StatusFinished || result.Error != nil {
		t.Fatalf("post-cancellation generation failed: %+v", result)
	}
}

func TestSceneMarkerFailuresAreAggregated(t *testing.T) {
	task, _, _ := markerTaskFixture(t, "software", "/dev/dri/renderD128")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scene, err := task.repository.Scene.Find(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := scene.LoadPrimaryFile(ctx, task.repository.File); err != nil {
		t.Fatal(err)
	}
	markers := []*models.SceneMarker{task.Marker, {ID: 2, SceneID: 1, Seconds: 2}}
	r := &mocks.SceneMarkerReaderWriter{}
	r.On("FindBySceneID", mock.Anything, 1).Return(markers, nil)
	task.repository.SceneMarker = r
	task.Scene = scene
	task.Marker = nil
	for _, m := range markers {
		if err := os.MkdirAll(task.generator.MarkerPaths.GetVideoPreviewPath("fixture", int(m.Seconds)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	err = task.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "marker 1 video") || !strings.Contains(err.Error(), "marker 2 video") {
		t.Fatal("scene generation lost marker failures", err)
	}
	for _, m := range markers {
		if data, err := os.ReadFile(task.generator.MarkerPaths.GetWebpPreviewPath("fixture", int(m.Seconds))); err != nil || string(data) != "fixture-output" {
			t.Fatal("scene generation lost successful partial asset", err)
		}
	}
	assertMarkerTempEmpty(t)
}

// Opt-in offline CT102 check, restricted to the approved synthetic fixture.
// Ordinary CI exercises the command mocks above and does not require a GPU.
func TestMarkerPersistenceLab(t *testing.T) {
	input := os.Getenv("VEX_MARKER_LAB_FIXTURE")
	if input == "" {
		t.Skip("set VEX_MARKER_LAB_FIXTURE to the approved CT102 synthetic fixture")
	}
	data, err := os.ReadFile(input)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != "63a09b75782c119331e0868488c1205030c4ead169e6b8f5ed80531bfe4d845d" {
		t.Fatal("lab input must match the approved synthetic fixture", err)
	}
	for _, c := range []struct{ backend, actual, stage string }{
		{"software", "software", ""},
		{"vaapi", "vaapi", ""},
		{"qsv", "software", "quality"},
	} {
		t.Run(c.backend, func(t *testing.T) {
			task, _, _ := markerTaskFixture(t, c.backend, "/dev/dri/renderD128")
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			scene, err := task.repository.Scene.Find(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := scene.LoadPrimaryFile(ctx, task.repository.File); err != nil {
				t.Fatal(err)
			}
			scene.Files.Primary().Path = input
			end := 3.0
			task.Marker.EndSeconds = &end
			task.ImagePreview = false
			task.generator.Encoder = ffmpeg.NewEncoder("/usr/bin/ffmpeg")
			task.generator.Probe = ffmpeg.NewFFProbe("/usr/bin/ffprobe")
			var diagnostics []ffmpeg.IntelGenerationDiagnostic
			task.generator.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }
			if err := task.Start(ctx); err != nil {
				t.Fatal(err)
			}
			output := task.generator.MarkerPaths.GetVideoPreviewPath("fixture", 1)
			release, err := instance.Config.GetGenerationBudget().Acquire(ctx, generationbudget.CPU)
			if err != nil {
				t.Fatal(err)
			}
			err = task.generator.Probe.ValidateVideoOutput(ctx, output)
			release()
			if err != nil {
				t.Fatal("persisted marker has no valid video packets", err)
			}
			release, err = instance.Config.GetGenerationBudget().Acquire(ctx, generationbudget.CPU)
			if err != nil {
				t.Fatal(err)
			}
			err = task.generator.Encoder.Generate(ctx, ffmpeg.Args{"-v", "error", "-nostdin", "-threads", "1", "-i", output, "-an", "-f", "null", "-"})
			release()
			if err != nil {
				t.Fatal("persisted marker cannot decode", err)
			}
			if len(diagnostics) != 1 || diagnostics[0].Actual != c.actual || diagnostics[0].Stage != c.stage {
				t.Fatal("unexpected backend/fallback", diagnostics)
			}
			assertMarkerTempEmpty(t)
			t.Logf("persisted and decoded marker selected=%s actual=%s stage=%s", c.backend, c.actual, c.stage)
		})
	}
}
