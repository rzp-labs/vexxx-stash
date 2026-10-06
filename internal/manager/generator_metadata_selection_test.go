package manager

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/scene/generate"
)

func selectedMetadataFixture(t *testing.T) (*Manager, string, string) {
	t.Helper()
	mgr, input, _, _ := metadataFixture(t)
	mgr.Config.SetInterface(config.PreviewGenerationBackend, "vaapi")
	mgr.Config.SetInterface(config.SpriteGenerationBackend, "qsv")
	mgr.Config.SetInterface(config.GenerationMaxProcesses, 1)
	mgr.Config.SetInterface(config.GenerationMaxGPUProcesses, 1)
	header := `{"format":{"format_name":"mov,mp4","start_time":"0.25","duration":"20"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":64,"height":36,"avg_frame_rate":"24/1","r_frame_rate":"24/1","duration":"1","nb_frames":"24","disposition":{"default":1}},{"index":2,"codec_type":"video","codec_name":"hevc","profile":"Main 10","width":1024,"height":768,"avg_frame_rate":"30000/1001","r_frame_rate":"60000/1001","duration":"10","nb_frames":"300","bit_rate":"6000","side_data_list":[{"rotation":90}]}]}`
	probe := filepath.Join(filepath.Dir(input), "ffprobe")
	headerArgs := filepath.Join(filepath.Dir(input), "args")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+headerArgs+"'\nprintf '%s' '"+header+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(filepath.Dir(input), "GPU-metadata-args")
	binary := filepath.Join(filepath.Dir(input), "ffmpeg")
	record := `VEXXX_GPU_METADATA={"stream_index":2,"bit_depth":10,"is_rgb":false,"width":1920,"height":1080,"pix_fmt":"p010le","sample_aspect_ratio":"1/1","color_range":"tv","color_space":"bt709","color_primaries":"bt709","color_transfer":"bt709","frame_rate":"50/1"}`
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 8.1.2'; exit 0; fi\nprintf '%s\\n' \"$@\" > '" + argsPath + "'\nprintf '%s\\n' '" + record + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	mgr.FFMpeg = ffmpeg.NewEncoder(binary)
	return mgr, input, argsPath
}

func TestGPUPlanningMetadataUsesNativeSelectedStreamUnderOnePermit(t *testing.T) {
	for _, workload := range []string{"preview", "sprite"} {
		t.Run(workload, func(t *testing.T) {
			mgr, input, argsPath := selectedMetadataFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var selected *ffmpeg.VideoFile
			var err error
			backend := "vaapi"
			if workload == "preview" {
				selected, err = mgr.generationPreviewVideoFile(ctx, input)
			} else {
				backend = "qsv"
				selected, err = mgr.generationSpriteVideoFile(ctx, input)
			}
			if err != nil {
				t.Fatal("selected-stream metadata failed or nested CPU/GPU admission deadlocked", err)
			}
			if selected.VideoStream == nil || selected.VideoStream.Index != 2 || selected.VideoCodec != "hevc" || selected.VideoBitrate != 6000 || selected.VideoStreamDuration != 10 || selected.FrameCount != 300 || selected.FrameRate != 29.97 || selected.Width != 1080 || selected.Height != 1920 || selected.StartTime != 0.25 {
				t.Fatalf("planning retained first/default video properties: %+v", selected)
			}
			stream := selected.VideoStream
			if stream.Width != 1920 || stream.Height != 1080 || stream.PixFmt != "yuv420p10le" || stream.RFrameRate != "60000/1001" || stream.AvgFrameRate != "30000/1001" || stream.SampleAspectRatio != "1:1" || stream.ColorRange != "tv" || stream.ColorSpace != "bt709" || stream.ColorPrimaries != "bt709" || stream.ColorTransfer != "bt709" {
				t.Fatalf("selected frame/header interpretation not aligned: %+v", stream)
			}
			args, readErr := os.ReadFile(argsPath)
			if readErr != nil || !strings.Contains(string(args), "-hwaccel\n"+backend+"\n") || strings.Contains(string(args), "-map\n") {
				t.Fatalf("wrong workload backend or forced header video selection: %s %v", args, readErr)
			}
			options := generate.PreviewOptions{Segments: 2, SegmentDuration: 2}
			if generate.PreviewIsSingleSegment(options, selected.VideoStreamDuration) {
				t.Fatal("preview targets used the one-second unselected stream")
			}
			info, infoErr := newGeneratorInfo(*selected)
			if infoErr != nil {
				t.Fatal(infoErr)
			}
			info.ChunkCount = 81
			if err := configureGPUSpriteInfo(info, false); err != nil || info.NumberOfFrames != 300 || info.FrameRate != 29.97 {
				t.Fatalf("sprite VTT plan retained unselected timing: %+v %v", info, err)
			}
			release, acquireErr := mgr.Config.GetIntelGenerationBudget().Acquire(ctx, generationbudget.GPU)
			if acquireErr != nil {
				t.Fatal("metadata helper leaked its shared permit", acquireErr)
			}
			release()
		})
	}
}

func TestSelectedGPUVideoPlanningDurationAndCountFallbacks(t *testing.T) {
	for _, duration := range []string{"", "N/A", "NaN", "+Inf", "-2", "0"} {
		for _, container := range []float64{12.25, math.NaN(), math.Inf(1), -1, 0} {
			file := &ffmpeg.VideoFile{FileDuration: container, VideoStreamDuration: 99, FrameCount: 999,
				JSON: ffmpeg.FFProbeJSON{Streams: []ffmpeg.FFProbeStream{{Index: 2, CodecType: "video", NbFrames: "N/A"}}}}
			if err := applySelectedGPUVideoSource(file, ffmpeg.IntelSource{StreamIndex: 2, Width: 1920, Height: 1080, Duration: duration}); err != nil {
				t.Fatal(err)
			}
			want := 0.0
			if container > 0 && !math.IsNaN(container) && !math.IsInf(container, 0) {
				want = container
			}
			if file.VideoStreamDuration != want || file.FrameCount != 0 {
				t.Fatalf("invalid selected declarations retained old timing: %+v", file)
			}
		}
	}
	if err := applySelectedGPUVideoSource(&ffmpeg.VideoFile{}, ffmpeg.IntelSource{StreamIndex: 2}); err == nil {
		t.Fatal("missing selected stream silently retained first/default source")
	}
}

func TestGPUPlanningMetadataFailureIsStrictAndReleasesAdmission(t *testing.T) {
	mgr, input, _ := selectedMetadataFixture(t)
	binary := filepath.Join(filepath.Dir(input), "ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' 'Failed setup for format vaapi: hwaccel initialisation returned error 23' >&2\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := mgr.generationPreviewVideoFile(ctx, input)
	var failure *ffmpeg.IntelGenerationError
	if err == nil || !errors.As(err, &failure) || failure.Diagnostic.Selected != "vaapi" || failure.Diagnostic.Stage != "metadata" || !strings.Contains(err.Error(), "error 23") {
		t.Fatalf("GPU source failure lost strict diagnostics: %v", err)
	}
	release, err := mgr.Config.GetIntelGenerationBudget().Acquire(ctx, generationbudget.GPU)
	if err != nil {
		t.Fatal("failed source metadata leaked permits", err)
	}
	release()
}

func TestGPUPlanningMetadataCancellationStopsCommandAndReleasesAdmission(t *testing.T) {
	mgr, input, _ := selectedMetadataFixture(t)
	binary := filepath.Join(filepath.Dir(input), "ffmpeg")
	started := filepath.Join(filepath.Dir(input), "GPU-metadata-started")
	script := "#!/bin/sh\nprintf 'ready' > '" + started + "'\nexec sleep 30\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := mgr.generationPreviewVideoFile(ctx, input)
		result <- err
	}()
	waitMetadataSignal(t, started)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled metadata command returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled metadata subprocess did not finish")
	}
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	release, err := mgr.Config.GetIntelGenerationBudget().Acquire(fresh, generationbudget.GPU)
	if err != nil {
		t.Fatal("cancelled metadata command leaked shared admission", err)
	}
	release()
	// Deletion's cancellation entry must observe completed source ownership.
	deleted := make(chan struct{})
	go func() {
		mgr.ReadLockManager.Cancel(input)
		close(deleted)
	}()
	select {
	case <-deleted:
	case <-fresh.Done():
		t.Fatal("cancelled metadata command retained unfinished source ownership")
	}
}
