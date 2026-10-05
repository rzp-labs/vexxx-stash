package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/paths"
)

const generationMetadataJSON = `{"format":{"format_name":"mov,mp4","duration":"1.25","start_time":"0.125","bit_rate":"1000","tags":{"title":"synthetic"}},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":64,"height":36,"avg_frame_rate":"24/1","r_frame_rate":"24/1","duration":"1.20","nb_frames":"30","nb_read_frames":"31","side_data_list":[{"rotation":90}],"disposition":{"default":1}},{"index":1,"codec_type":"audio","codec_name":"aac","disposition":{"default":1}}]}`

// The fixture records a completed signal before blocking. It represents real
// subprocess admission/cancellation without decoding media or using GPU engines.
func metadataFixture(t *testing.T) (*Manager, string, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "synthetic.mp4")
	if err := os.WriteFile(input, []byte("synthetic stat fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(dir, "args")
	started := filepath.Join(dir, "started")
	block := filepath.Join(dir, "block")
	probe := filepath.Join(dir, "ffprobe")
	script := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffprobe version 7.0'; exit 0; fi\nprintf '%s\\n' \"$@\" > '" + argsPath + "'\nprintf 'ready' > '" + started + "'\nif [ -f '" + block + "' ]; then exec sleep 30; fi\nprintf '%s' '" + generationMetadataJSON + "'\n"
	if err := os.WriteFile(probe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.InitializeEmpty()
	cfg.SetInterface(config.GenerationBudgetEnabled, true)
	p := paths.NewPaths(filepath.Join(dir, "generated"), filepath.Join(dir, "blobs"))
	mgr := &Manager{Config: cfg, FFProbe: ffmpeg.NewFFProbe(probe), ReadLockManager: fsutil.NewReadLockManager(), Paths: &p}
	return mgr, input, argsPath, block
}

func waitMetadataSignal(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && string(data) == "ready" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("metadata fixture did not start")
		}
		runtime.Gosched()
	}
}

func TestGenerationTasksMetadataWaitForSharedBudget(t *testing.T) {
	for _, kind := range []string{"sprite", "preview"} {
		t.Run(kind, func(t *testing.T) {
			mgr, input, argsPath, _ := metadataFixture(t)
			previous := instance
			instance = mgr
			defer func() { instance = previous }()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			// A running GPU job also occupies the sole total slot. The actual
			// manager task must wait before spawning its metadata CPU process.
			release, err := mgr.Config.GetGenerationBudget().Acquire(context.Background(), generationbudget.GPU)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			scene := models.Scene{Path: input, Checksum: "fixture"}
			if kind == "sprite" {
				task := GenerateSpriteTask{Scene: scene, Overwrite: true}
				err = task.Start(ctx)
			} else {
				task := GeneratePreviewTask{Scene: scene, Overwrite: true}
				err = task.Start(ctx)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting task did not cancel: %v", err)
			}
			if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
				t.Fatal("metadata subprocess bypassed occupied generation budget")
			}
			release()
			fresh, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if _, err := mgr.generationVideoFile(fresh, input); err != nil {
				t.Fatal("cancelled waiter leaked admission", err)
			}
		})
	}
}

func TestGenerationMetadataCanonicalAndIndependentDefaults(t *testing.T) {
	mgr, input, argsPath, _ := metadataFixture(t)
	legacy, err := mgr.FFProbe.NewVideoFile(input)
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := mgr.generationVideoFile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, bounded) {
		t.Fatalf("canonical metadata changed: legacy=%+v bounded=%+v", legacy, bounded)
	}
	if bounded.Width != 36 || bounded.Height != 64 || bounded.FrameRate != 24 || bounded.VideoStreamDuration != 1.2 || bounded.AudioCodec != "aac" {
		t.Fatalf("unexpected fixture metadata %+v", bounded)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-threads\n1\n") {
		t.Fatalf("generation metadata decoder thread request unbounded: %s", args)
	}
	readFrames, err := mgr.generationReadFrameCount(context.Background(), input)
	if err != nil || readFrames != 31 {
		t.Fatalf("canonical read frame count: %d %v", readFrames, err)
	}
	// A normal scan/playback call on the same FFProbe still uses legacy args.
	if _, err := mgr.FFProbe.NewVideoFile(input); err != nil {
		t.Fatal(err)
	}
	args, err = os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "-threads\n") {
		t.Fatal("generation mutated shared scan/playback probe")
	}
	// Source admission has been released before the next extraction acquires.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := mgr.Config.GetGenerationBudget().Acquire(ctx, generationbudget.CPU)
	if err != nil {
		t.Fatal("metadata retained a parent permit", err)
	}
	release()
}

func TestGPUGenerationMetadataUsesHeaderOnlyPolicy(t *testing.T) {
	for _, kind := range []string{"sprite", "preview"} {
		t.Run(kind, func(t *testing.T) {
			mgr, input, argsPath, _ := metadataFixture(t)
			setting := config.SpriteGenerationBackend
			probe := mgr.generationSpriteVideoFile
			if kind == "preview" {
				setting, probe = config.PreviewGenerationBackend, mgr.generationPreviewVideoFile
			}
			mgr.Config.SetInterface(setting, "vaapi")
			if _, err := probe(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(argsPath)
			if err != nil || !strings.Contains(string(args), "-fflags\n+no_pixel_probe\n") {
				t.Fatalf("GPU source metadata can decode on CPU: %s %v", args, err)
			}
			if _, err := mgr.FFProbe.NewVideoFile(input); err != nil {
				t.Fatal(err)
			}
			args, _ = os.ReadFile(argsPath)
			if strings.Contains(string(args), "no_pixel_probe") {
				t.Fatalf("GPU source metadata policy leaked into shared scan/playback: %s", args)
			}
		})
	}
}

func TestGenerationMetadataActiveCancelReleasesPermit(t *testing.T) {
	mgr, input, _, block := metadataFixture(t)
	if err := os.WriteFile(block, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := mgr.generationVideoFile(ctx, input); done <- err }()
	waitMetadataSignal(t, filepath.Join(filepath.Dir(block), "started"))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active probe cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metadata child did not terminate")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := mgr.generationReadFrameCount(fresh, input); err != nil {
		t.Fatal("cancelled metadata process leaked admission", err)
	}
}

func TestGenerationMetadataPreservesCallerDeadline(t *testing.T) {
	for _, budgeted := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("budget_%t/deadline_%t", budgeted, deadline), func(t *testing.T) {
				cfg := config.InitializeEmpty()
				cfg.SetInterface(config.GenerationBudgetEnabled, budgeted)
				mgr := &Manager{Config: cfg}
				ctx := context.Background()
				if deadline {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
				}
				want, hasDeadline := ctx.Deadline()
				err := mgr.generationMetadataStage(ctx, func(child context.Context, threads int) error {
					got, childHasDeadline := child.Deadline()
					if childHasDeadline != hasDeadline || !got.Equal(want) {
						t.Fatalf("metadata shortened caller deadline: want %v/%t got %v/%t", want, hasDeadline, got, childHasDeadline)
					}
					wantThreads := 0
					if budgeted {
						wantThreads = 1
					}
					if threads != wantThreads {
						t.Fatalf("metadata threading changed: got %d want %d", threads, wantThreads)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestGenerationMetadataCallerDeadlineReleasesPermit(t *testing.T) {
	mgr, input, _, block := metadataFixture(t)
	if err := os.WriteFile(block, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := mgr.generationMetadataStage(ctx, func(ctx context.Context, threads int) error {
		_, err := mgr.FFProbe.NewVideoFileContext(ctx, input, threads)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("child deadline was lost: %v", err)
	}
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	release, err := mgr.Config.GetGenerationBudget().Acquire(fresh, generationbudget.CPU)
	if err != nil {
		t.Fatal("deadline leaked admission", err)
	}
	release()
}

func TestGenerationFrameRateFallbackAndShortSpriteReleaseBetweenStages(t *testing.T) {
	mgr, input, _, block := metadataFixture(t)
	previous := instance
	instance = mgr
	defer func() { instance = previous }()
	argsPath := filepath.Join(filepath.Dir(block), "ffmpeg-args")
	encoderPath := filepath.Join(filepath.Dir(block), "ffmpeg")
	script := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffmpeg version 7.0'; exit 0; fi\nprintf '%s\\n' \"$@\" > '" + argsPath + "'\nprintf 'frame=   31 time=00:00:01.25\\n' >&2\n"
	if err := os.WriteFile(encoderPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	mgr.FFMpeg = ffmpeg.NewEncoder(encoderPath)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Missing metadata needs the packet-copy fallback, under the same budget.
	info := generatorInfo{VideoFile: ffmpeg.VideoFile{Path: input}}
	if err := info.calculateFrameRate(ctx, &ffmpeg.FFProbeStream{}); err != nil {
		t.Fatal(err)
	}
	if info.NumberOfFrames != 31 || info.FrameRate != 24.8 {
		t.Fatalf("canonical frame fallback changed: %+v", info)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-threads\n1\n") || !strings.Contains(string(args), "-c:v\ncopy\n") {
		t.Fatalf("frame metadata threading/copy behavior: %s", args)
	}
	// Source metadata releases before the short-video read-frame-count stage.
	video, err := mgr.generationVideoFile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	sprite, err := NewSpriteGenerator(ctx, *video, "fixture", filepath.Join(filepath.Dir(input), "sprite.jpg"), filepath.Join(filepath.Dir(input), "sprite.vtt"), 9, 9)
	if err != nil {
		t.Fatal("short sprite metadata stages retained nested permit", err)
	}
	if !sprite.SlowSeek || sprite.Info.VideoFile.FrameCount != 31 {
		t.Fatalf("short-video canonical frame count changed: %+v", sprite.Info)
	}
	release, err := mgr.Config.GetGenerationBudget().Acquire(ctx, generationbudget.CPU)
	if err != nil {
		t.Fatal("metadata stages leaked admission", err)
	}
	release()
}

func TestShortSpriteFrameCountWaitingCancellation(t *testing.T) {
	mgr, input, argsPath, _ := metadataFixture(t)
	previous := instance
	instance = mgr
	defer func() { instance = previous }()
	release, err := mgr.Config.GetGenerationBudget().Acquire(context.Background(), generationbudget.GPU)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = NewSpriteGenerator(ctx, ffmpeg.VideoFile{Path: input, VideoStreamDuration: 1, FrameCount: 25}, "fixture", "sprite.jpg", "sprite.vtt", 9, 9)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short frame-count stage ignored cancellation: %v", err)
	}
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatal("short frame-count subprocess bypassed occupied budget")
	}
}
