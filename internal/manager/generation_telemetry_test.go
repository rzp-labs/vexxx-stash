package manager

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/internal/build"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stretchr/testify/mock"
)

type capturedTelemetryEvent struct {
	UUID       string         `json:"uuid"`
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
}

// Exercise the configured SDK transport, not a replacement capture hook.
func generationTelemetryReceiver(t *testing.T) func() []capturedTelemetryEvent {
	t.Helper()
	var mu sync.Mutex
	var events []capturedTelemetryEvent
	// Opt-in acceptance reuses this receiver as a transparent SDK proxy. Only
	// the one synthetic production-entry test may send an event externally.
	live := os.Getenv("VEX_TELEMETRY_LIVE_ACCEPTANCE") == "1"
	var target *url.URL
	if live {
		if t.Name() != "TestGenerationJobCapturesGPUCommandFailureOnce" {
			t.Fatal("live acceptance requires exactly the synthetic GPU failure test")
		}
		var err error
		target, err = url.Parse(os.Getenv("POSTHOG_HOST"))
		if err != nil || target.Scheme != "https" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Port() != "" || (target.Path != "" && target.Path != "/") {
			t.Fatal("invalid configured PostHog ingestion endpoint")
		}
		switch target.Hostname() {
		case "us.i.posthog.com", "eu.i.posthog.com", "us.posthog.com", "eu.posthog.com":
		default:
			t.Fatal("live acceptance must use the existing PostHog cloud destination")
		}
		if !strings.HasPrefix(os.Getenv("POSTHOG_PROJECT_TOKEN"), "phc_") {
			t.Fatal("live acceptance requires the configured public project ingestion token")
		}
	}
	forwarded := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		if err != nil || len(wire) > 65536 {
			t.Error("SDK payload exceeds bounded receiver limit")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body io.Reader = bytes.NewReader(wire)
		if r.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer reader.Close()
			body = reader
		}
		var payload struct {
			Batch []capturedTelemetryEvent `json:"batch"`
		}
		if err := json.NewDecoder(io.LimitReader(body, 65536)).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		events = append(events, payload.Batch...)
		mu.Unlock()
		if live {
			if r.Method != http.MethodPost || r.URL.Path != "/batch/" || len(payload.Batch) != 1 {
				t.Error("live acceptance must contain exactly one SDK exception")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			event := payload.Batch[0]
			encoded, _ := json.Marshal(event)
			version, revision, _ := build.Version()
			if event.Event != "$exception" || event.Properties["$app_namespace"] != "vexxx-server" || event.Properties["$app_version"] != version || event.Properties["$app_build"] != revision || event.Properties["generation_workload"] != "sprite" {
				t.Error("live acceptance payload is not the production generation diagnostic")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, private := range []string{"fixture-secret", "private media file.mp4", "media file.mp4", "private media description", "source=/", "source=\\", "/tmp/", "/private/", "/mnt/"} {
				if strings.Contains(string(encoded), private) {
					t.Error("live acceptance diagnostic failed privacy checks")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			mu.Lock()
			duplicate := forwarded
			forwarded = true
			mu.Unlock()
			if duplicate {
				t.Error("live acceptance refuses a second external attempt")
				_, _ = io.WriteString(w, `{"status":1}`)
				return
			}
			endpoint := *target
			endpoint.Path = r.URL.Path
			request, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewReader(wire))
			if err != nil {
				t.Error("cannot create bounded PostHog SDK request")
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			request.Header.Set("Content-Type", r.Header.Get("Content-Type"))
			request.Header.Set("Content-Encoding", r.Header.Get("Content-Encoding"))
			transport := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := transport.Do(request)
			if err != nil {
				t.Error("configured PostHog SDK request failed")
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				t.Errorf("PostHog ingestion HTTP %d", response.StatusCode)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			t.Logf("controlled ingestion accepted: event=%s correlation=%s app=%s version=%s build=%s", event.UUID, event.Properties["job_correlation"], event.Properties["$app_namespace"], version, revision)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":1}`)
	}))
	if !live {
		t.Setenv("POSTHOG_PROJECT_TOKEN", "test-project-token")
	}
	t.Setenv("POSTHOG_HOST", server.URL)
	if err := analytics.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := analytics.Close(); err != nil {
			t.Error(err)
		}
		server.Close()
		_ = os.Setenv("POSTHOG_PROJECT_TOKEN", "")
		_ = os.Setenv("POSTHOG_HOST", "")
		if err := analytics.Initialize(); err != nil {
			t.Error(err)
		}
	})
	return func() []capturedTelemetryEvent {
		if err := analytics.Client().Flush(); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedTelemetryEvent(nil), events...)
	}
}

func TestGenerationJobCapturesGPUCommandFailureOnce(t *testing.T) {
	received := generationTelemetryReceiver(t)
	mgr, input, _, _ := metadataFixture(t)
	privateInput := filepath.Join(filepath.Dir(input), "private media file.mp4")
	if err := os.Rename(input, privateInput); err != nil {
		t.Fatal(err)
	}
	input = privateInput
	previous := instance
	instance = mgr
	t.Cleanup(func() { instance = previous })
	mgr.Config.SetInterface(config.SpriteGenerationBackend, "vaapi")
	mgr.Config.SetInterface(config.GenerationMaxProcesses, 16)
	mgr.Config.SetInterface(config.GenerationMaxGPUProcesses, 16)
	mgr.Config.SetInterface(config.GenerationThreads, 0)
	mgr.Config.SetInterface(config.ParallelTasks, 1)
	var metadata map[string]any
	if err := json.Unmarshal([]byte(generationMetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	video := metadata["streams"].([]any)[0].(map[string]any)
	delete(video, "side_data_list")
	video["pix_fmt"], video["duration"], video["nb_frames"] = "yuv420p", "10", "300"
	video["nb_read_frames"] = "300"
	metadata["format"].(map[string]any)["duration"] = "10"
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(filepath.Dir(input), "ffprobe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s' '"+string(data)+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(filepath.Dir(input), "ffmpeg")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 8.1.2'; exit 0; fi\nprintf '%s\\n' 'Failed setup for format vaapi: hwaccel initialisation returned error 23' 'Error while decoding stream #0:0: Input/output error' 'source=" + input + " token=fixture-secret' >&2\nexit 23\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	mgr.FFMpeg = ffmpeg.NewEncoder(binary)
	file := &models.VideoFile{BaseFile: &models.BaseFile{ID: 1, Path: input}, Width: 64, Height: 36, Duration: 10}
	scene := &models.Scene{ID: 1, Path: input, Checksum: "fixture", OSHash: "fixture"}
	db := mocks.NewDatabase()
	db.Scene.On("FindMany", mock.Anything, []int{1}).Return([]*models.Scene{scene}, nil)
	db.Scene.On("GetFiles", mock.Anything, 1).Return([]*models.VideoFile{file}, nil)
	m := job.NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	j := &GenerateJob{repository: models.Repository{TxnManager: db, Scene: db.Scene}, input: GenerateMetadataInput{SceneIDs: []string{"1"}, Sprites: true, Overwrite: true}}
	id := m.Add(context.Background(), "private media description", j)
	result := waitMarkerJob(t, m, id)
	if result.Status != job.StatusFailed || result.Error == nil || !strings.Contains(*result.Error, "error 23") {
		t.Fatalf("fixture did not reach GPU command failure: %+v", result)
	}
	events := received()
	if len(events) != 1 || events[0].Event != "$exception" {
		t.Fatalf("expected one generation exception, got %+v", events)
	}
	properties := events[0].Properties
	version, revision, _ := build.Version()
	if version == "" {
		version = "development"
	}
	if revision == "" {
		revision = "development"
	}
	for key, want := range map[string]any{
		"$app_namespace":                      "vexxx-server",
		"$app_version":                        version,
		"$app_build":                          revision,
		"failure_origin":                      "generation",
		"generation_workload":                 "sprite",
		"generation_selected_backend":         "vaapi",
		"generation_actual_backend":           "none",
		"generation_stage":                    "metadata",
		"generation_configured_processes":     float64(16),
		"generation_configured_gpu_processes": float64(16),
		"generation_configured_threads":       float64(0),
		"generation_effective_processes":      float64(16),
		"generation_effective_gpu_processes":  float64(16),
		"generation_effective_threads":        float64(1),
		"generation_admitted_slots":           float64(1),
		"generation_command_threads":          float64(1),
		"generation_parallel_tasks":           float64(1),
		"ffmpeg_exit_code":                    float64(23),
		"hwaccel_error_code":                  float64(23),
	} {
		if properties[key] != want {
			t.Fatalf("%s=%v, want %v", key, properties[key], want)
		}
	}
	if _, err := uuid.Parse(properties["job_correlation"].(string)); err != nil {
		t.Fatal("generation event has no opaque correlation", err)
	}
	encoded, _ := json.Marshal(events[0])
	for _, sensitive := range []string{input, filepath.Base(input), filepath.Dir(input), "fixture-secret", "private media description"} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("private value leaked: %s", sensitive)
		}
	}
	for _, technical := range []string{"hwaccel initialisation returned error 23", "Input/output error"} {
		if !strings.Contains(string(encoded), technical) {
			t.Fatalf("technical error lost: %s", technical)
		}
	}
}

func TestConfiguredJobManagerCapturesRecoveredPanic(t *testing.T) {
	received := generationTelemetryReceiver(t)
	m := initJobManager(config.InitializeEmpty())
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	var correlation string
	id := m.Add(context.Background(), "private description", job.MakeJobExec(func(ctx context.Context, _ *job.Progress) error {
		correlation = job.Correlation(ctx)
		panic("VAAPI worker failed with error 23 token=fixture-secret /private/media/file.mp4")
	}))
	if result := waitMarkerJob(t, m, id); result.Status != job.StatusFailed {
		t.Fatalf("recovered panic status=%s", result.Status)
	}
	if _, err := uuid.Parse(correlation); err != nil {
		t.Fatal("job correlation was not opaque UUID", err)
	}
	events := received()
	if len(events) != 1 || events[0].Event != "$exception" {
		t.Fatalf("expected one worker panic exception, got %+v", events)
	}
	if events[0].Properties["failure_origin"] != "worker_panic" {
		t.Fatal("worker panic origin missing", events[0])
	}
	encoded, _ := json.Marshal(events[0])
	if !strings.Contains(string(encoded), correlation) || strings.Contains(string(encoded), "fixture-secret") || strings.Contains(string(encoded), "/private/media") {
		t.Fatalf("correlation absent or sensitive panic leaked: %s", encoded)
	}
}

func TestGenerationJobCapturesJoinedMarkerOutputsWithoutAggregateDuplicate(t *testing.T) {
	received := generationTelemetryReceiver(t)
	task, _, _ := markerTaskFixture(t, "qsv", "/dev/dri/renderD128")
	m := job.NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	j := &GenerateJob{repository: task.repository, input: GenerateMetadataInput{MarkerIDs: []string{"1"}, Markers: true, MarkerImagePreviews: true, Overwrite: true}}
	id := m.Add(context.Background(), "private marker description", j)
	if result := waitMarkerJob(t, m, id); result.Status != job.StatusFailed {
		t.Fatalf("joined marker failures did not fail job: %+v", result)
	}
	events := received()
	if len(events) != 2 {
		t.Fatalf("expected two independently failed outputs without job duplicate, got %+v", events)
	}
	for _, event := range events {
		if event.Event != "$exception" {
			t.Fatalf("unexpected event: %+v", event)
		}
		if event.Properties["generation_workload"] != "marker" {
			t.Fatalf("marker failure workload lost: %+v", event)
		}
	}
}

func TestGenerationJobObservesLegacyThumbnailFailureWithoutStatusChange(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed-open", true: "unsupported-gif"}[unsupported], func(t *testing.T) {
			received := generationTelemetryReceiver(t)
			mgr, _, _, _ := metadataFixture(t)
			previous := instance
			instance = mgr
			t.Cleanup(func() { instance = previous })
			mgr.Config.SetInterface(config.ParallelTasks, 1)
			input := filepath.Join(t.TempDir(), "private image filename.png")
			format := "png"
			if unsupported {
				format = "gif"
				input = filepath.Join(filepath.Dir(input), "private animated filename.gif")
				if err := os.WriteFile(input, []byte("synthetic animated format sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			file := &models.ImageFile{BaseFile: &models.BaseFile{ID: 1, Path: input}, Width: 1024, Height: 1024, Format: format}
			image := &models.Image{ID: 1, Path: input, Checksum: "fixture"}
			db := mocks.NewDatabase()
			db.Image.On("FindMany", mock.Anything, []int{1}).Return([]*models.Image{image}, nil)
			db.Image.On("GetFiles", mock.Anything, 1).Return([]models.File{file}, nil)
			m := job.NewManager()
			t.Cleanup(func() { m.StopAndWait(time.Second) })
			j := &GenerateJob{repository: models.Repository{TxnManager: db, Image: db.Image}, input: GenerateMetadataInput{ImageIDs: []string{"1"}, ImageThumbnails: true, Overwrite: true}}
			id := m.Add(context.Background(), "private image description", j)
			result := waitMarkerJob(t, m, id)
			if result.Status != job.StatusFinished || result.Error != nil {
				t.Fatalf("telemetry changed legacy thumbnail job status: %+v", result)
			}
			events := received()
			if unsupported {
				if len(events) != 0 {
					t.Fatalf("unsupported thumbnail sentinel emitted exception: %+v", events)
				}
				return
			}
			if len(events) != 1 || events[0].Event != "$exception" || events[0].Properties["generation_workload"] != "image_thumbnail" {
				t.Fatalf("legacy thumbnail error missing or duplicated: %+v", events)
			}
			encoded, _ := json.Marshal(events[0])
			if !strings.Contains(string(encoded), "no such file or directory") {
				t.Fatalf("filesystem cause missing: %s", encoded)
			}
			for _, private := range []string{input, filepath.Base(input), filepath.Dir(input), "private image description"} {
				if strings.Contains(string(encoded), private) {
					t.Fatalf("legacy thumbnail telemetry leaked %s", private)
				}
			}
		})
	}
}

func TestLegacyThumbnailCancellationEmitsNoException(t *testing.T) {
	received := generationTelemetryReceiver(t)
	mgr, _, _, _ := metadataFixture(t)
	previous := instance
	instance = mgr
	t.Cleanup(func() { instance = previous })
	input := filepath.Join(t.TempDir(), "private cancelled image.png")
	file := &models.ImageFile{BaseFile: &models.BaseFile{ID: 1, Path: input}, Width: 1024, Height: 1024, Format: "png"}
	task := &GenerateImageThumbnailTask{Image: models.Image{Path: input, Checksum: "fixture", Files: models.NewRelatedFiles([]models.File{file})}, Overwrite: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = withGenerationFailureReporter(ctx, func(err error) {
		analytics.CaptureGenerationFailure(ctx, err, analytics.GenerationFailureContext{Workload: "image_thumbnail", PrivateValues: []string{input}})
	})
	if err := task.Start(ctx); err != nil {
		t.Fatal("legacy thumbnail cancellation return changed", err)
	}
	if events := received(); len(events) != 0 {
		t.Fatalf("cancelled legacy thumbnail emitted exception: %+v", events)
	}
}

func TestGenerationJobHeatmapFailureRedactsColocatedFunscript(t *testing.T) {
	received := generationTelemetryReceiver(t)
	mgr, _, _, _ := metadataFixture(t)
	previous := instance
	instance = mgr
	t.Cleanup(func() { instance = previous })
	mgr.Config.SetInterface(config.ParallelTasks, 1)
	input := filepath.Join(t.TempDir(), "private scene with spaces.mp4")
	script := video.GetFunscriptPath(input)
	if err := os.WriteFile(script, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	file := &models.VideoFile{BaseFile: &models.BaseFile{ID: 1, Path: input}, Duration: 10, Interactive: true}
	scene := &models.Scene{ID: 1, Path: input, Checksum: "fixture", OSHash: "fixture"}
	db := mocks.NewDatabase()
	db.Scene.On("FindMany", mock.Anything, []int{1}).Return([]*models.Scene{scene}, nil)
	db.Scene.On("GetFiles", mock.Anything, 1).Return([]*models.VideoFile{file}, nil)
	m := job.NewManager()
	t.Cleanup(func() { m.StopAndWait(time.Second) })
	j := &GenerateJob{repository: models.Repository{TxnManager: db, Scene: db.Scene}, input: GenerateMetadataInput{SceneIDs: []string{"1"}, InteractiveHeatmapsSpeeds: true, Overwrite: true}}
	id := m.Add(context.Background(), "private heatmap description", j)
	result := waitMarkerJob(t, m, id)
	if result.Status != job.StatusFinished || result.Error != nil {
		t.Fatalf("telemetry changed legacy heatmap job status: %+v", result)
	}
	events := received()
	if len(events) != 1 || events[0].Event != "$exception" || events[0].Properties["generation_workload"] != "interactive_heatmap" {
		t.Fatalf("heatmap failure missing or duplicated: %+v", events)
	}
	encoded, _ := json.Marshal(events[0])
	if !strings.Contains(string(encoded), "actions list missing") {
		t.Fatalf("technical funscript failure lost: %s", encoded)
	}
	// A generic unquoted-path pattern can remove only the first filename word.
	// Check the distinctive remainder too, so a partial redaction cannot pass.
	for _, private := range []string{input, script, filepath.Base(input), filepath.Base(script), filepath.Dir(input), "scene with spaces", "with spaces.funscript", "private heatmap description"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("heatmap telemetry leaked %s", private)
		}
	}
}
