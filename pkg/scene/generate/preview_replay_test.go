package generate

import (
	"context"
	"errors"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestPreviewReplayMetadataAndEligibilityProducers(t *testing.T) {
	for _, kind := range []string{"header", "metadata", "eligibility"} {
		t.Run(kind, func(t *testing.T) {
			script := "echo 'driver failure' >&2\nexit 23\n"
			if kind == "eligibility" {
				frame := intelMetadataTestRecord()
				frame["color_primaries"] = "bt709"
				script = "printf '%s\\n' '" + string(intelMetadataTestOutput(frame)) + "'\n"
			}
			g := metadataTestGenerator(t, script, generationbudget.Settings{MaxProcesses: 2, MaxGPUProcesses: 1})
			if kind == "header" {
				if err := os.WriteFile(g.Probe.Path(), []byte("#!/bin/sh\necho headerfailure >&2\nexit 23\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			g.IntelPreviews = &ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
			if kind == "eligibility" {
				g.IntelPreviews.Backend = "qsv"
			}
			var saved []diagnostics.PreviewReplayRecord
			ctx := diagnostics.WithPreviewRequest(diagnostics.WithPreviewSource(context.Background(), 12, 34, "", func(r diagnostics.PreviewReplayRecord) error { saved = append(saved, r); return nil }), PreviewReplayRequest(PreviewOptions{Segments: 3, SegmentDuration: 0.75, ExcludeStart: "10%"}, nil))
			lock := g.LockManager.ReadLock(ctx, "synthetic")
			defer lock.Cancel()
			err := g.scenePreviewVideo("synthetic", 10, PreviewOptions{Segments: 3, SegmentDuration: 0.75}, "", false, false)(lock, "output")
			r, ok := diagnostics.PreviewFailureSnapshot(err)
			if !ok || r.Command.Stage != kind || len(saved) != 1 {
				t.Fatal("producer failure facts", kind, r, err, saved)
			}
			if kind == "metadata" {
				if r.Command.Started == nil || !*r.Command.Started {
					t.Fatal("failed execution lost actual start state", r.Command)
				}
				if r.Command.SeekSeconds == nil || *r.Command.SeekSeconds != 0 || r.Command.FrameLimit == nil || *r.Command.FrameLimit != 1 || r.Command.StreamIndex != nil || r.Command.InputPixels != nil {
					t.Fatal("metadata guessed selected source", r.Command)
				}
			} else if r.Command.SeekSeconds != nil {
				t.Fatal("pre-command stage invented seek", r.Command)
			}
			if kind == "eligibility" && (r.Command.InputPixels == nil || r.Command.InputPixels.Width != 8192) {
				t.Fatal("eligibility lost actual pixels", r.Command)
			}
		})
	}
}

func TestPreviewReplayMetadataParseFailureRetainsSuccessfulCommandStart(t *testing.T) {
	for name, output := range map[string]string{
		"missing record":   "non-metadata output",
		"multiple records": intelMetadataPrefix + "{}\n" + intelMetadataPrefix + "{}",
		"malformed JSON":   intelMetadataPrefix + "{",
		"missing fields":   intelMetadataPrefix + "{}",
	} {
		t.Run(name, func(t *testing.T) {
			g := metadataTestGenerator(t, "cat <<'METADATA'\n"+output+"\nMETADATA\n", generationbudget.Settings{MaxProcesses: 2, MaxGPUProcesses: 1})
			var saved []diagnostics.PreviewReplayRecord
			ctx := diagnostics.WithPreviewSource(context.Background(), 12, 34, "", func(r diagnostics.PreviewReplayRecord) error { saved = append(saved, r); return nil })
			lock := g.LockManager.ReadLock(ctx, "synthetic")
			defer lock.Cancel()
			_, err := g.intelSourceMetadata(ctx, lock, "synthetic", ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, false)
			replay, ok := diagnostics.PreviewFailureSnapshot(err)
			if !ok || replay.Command.Stage != "metadata" || replay.Command.Started == nil || !*replay.Command.Started {
				t.Fatal("successful command start lost on metadata parsing failure", replay, err)
			}
			if len(saved) != 1 || saved[0].Replay.Reference != replay.Reference || saved[0].Replay.Command.Started == nil || !*saved[0].Replay.Command.Started {
				t.Fatal("local replay lost successful command start", saved)
			}
			_, parseErr := mergeIntelFrameMetadata(ffmpeg.IntelSource{}, []byte(output))
			var wrapped *diagnostics.PreviewReplayError
			if !errors.As(err, &wrapped) || wrapped.Err == nil || wrapped.Err.Error() != parseErr.Error() {
				t.Fatal("metadata parsing error changed", err, parseErr)
			}
		})
	}
}

func TestPreviewReplayRenderAndConcatUseActualArguments(t *testing.T) {
	g := metadataTestGenerator(t, "for output do :;done\nprintf '%s\\n' \"$@\" > \"$output.args\"\necho encoderfailure >&2\nexit 23\n", generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 4})
	source := ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StreamIndex: 3}
	plan, err := ffmpeg.NewIntelPreviewPlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, source, "private input", 7.5, 640)
	if err != nil {
		t.Fatal(err)
	}
	command := ffmpeg.PreviewReplayCommand(plan, 7.5, PreviewWidth, "")
	plan.PreviewReplay = &command
	g.previewIntelPlan = &plan
	var saved []diagnostics.PreviewReplayRecord
	var mu sync.Mutex
	ctx := diagnostics.WithPreviewRequest(diagnostics.WithPreviewSource(context.Background(), 1, 2, "", func(r diagnostics.PreviewReplayRecord) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, r)
		return nil
	}), PreviewReplayRequest(PreviewOptions{Segments: 3, SegmentDuration: 0.25, ExcludeStart: "10%"}, nil))
	lock := g.LockManager.ReadLock(ctx, "synthetic")
	defer lock.Cancel()
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			opts := previewChunkOptions{StartTime: 7.5 + float64(i)*3, Duration: 0.75, OutputPath: filepath.Join(dir, strconv.Itoa(i)+".mp4"), Audio: i%2 == 0}
			err := g.previewVideoChunk(lock, ctx, "private input", opts, "", false, true)
			r, ok := diagnostics.PreviewFailureSnapshot(err)
			if !ok || r.Command.Stage != "render" || *r.Command.SeekSeconds != opts.StartTime || *r.Command.DurationSeconds != 0.75 || r.Command.StreamIndex != nil || !*r.Command.Vsync2 || r.Command.Started == nil || !*r.Command.Started {
				t.Error("segment facts", r, err)
				return
			}
			data, readErr := os.ReadFile(opts.OutputPath + ".args")
			if readErr != nil {
				t.Error(readErr)
				return
			}
			args := "\n" + string(data)
			if !strings.Contains(args, "\n-ss\n"+strconv.FormatFloat(*r.Command.SeekSeconds, 'f', -1, 64)+"\n") || !strings.Contains(args, "\n-t\n0.75\n") || !strings.Contains(args, "\n-vsync\n2\n") {
				t.Error("reported values differ from executed command", string(data), r)
			}
		}(i)
	}
	wg.Wait()
	if len(saved) != 3 {
		t.Fatal("parallel failures lost", saved)
	}
	for i := 1; i < len(saved); i++ {
		if saved[i].Replay.SourceReference != saved[0].Replay.SourceReference || saved[i].Replay.Reference == saved[0].Replay.Reference {
			t.Fatal("source/execution references", saved)
		}
	}
	out := filepath.Join(dir, "concat.mp4")
	err = g.previewVideoChunkCombine(lock, "private concat", out)
	r, ok := diagnostics.PreviewFailureSnapshot(err)
	if !ok || r.Command.Stage != "concat" || r.Command.SeekSeconds != nil || r.Command.DurationSeconds != nil || r.Command.FilterConfigured {
		t.Fatal("concat facts", r, err)
	}
	data, _ := os.ReadFile(out + ".args")
	if strings.Contains(string(data), "-ss\n") || strings.Contains(string(data), "-t\n") {
		t.Fatal("concat fixture unexpectedly seeks", string(data))
	}
}

func TestPreviewReplayOutputValidationAndSuccessCancellation(t *testing.T) {
	g := metadataTestGenerator(t, "exit 0\n", generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
	g.Probe = nil
	var saved int
	ctx := diagnostics.WithPreviewSource(context.Background(), 1, 2, "", func(diagnostics.PreviewReplayRecord) error { saved++; return nil })
	err := g.validatePreviewOutput(ctx, "private output", diagnostics.PreviewCommand{})
	r, ok := diagnostics.PreviewFailureSnapshot(err)
	if !ok || r.Command.Stage != "output" || r.Command.SeekSeconds != nil || r.Command.StreamIndex != nil || r.Command.VideoSelection != "first_video" {
		t.Fatal("output facts", r, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err = g.validatePreviewOutput(canceled, "private output", diagnostics.PreviewCommand{})
	if !errors.Is(err, context.Canceled) || saved != 1 {
		t.Fatal("cancellation retained or lost", err, saved)
	}
	if diagnostics.WithPreviewFailure(ctx, nil, diagnostics.PreviewCommand{}) != nil || saved != 1 {
		t.Fatal("success retained")
	}
}

func TestPreviewReplayRequestKeepsOptionsSeparateFromClamping(t *testing.T) {
	start, end := 4.0, 10.0
	duration := 6.0
	opts := PreviewOptions{Segments: 8, SegmentDuration: 0.1, ExcludeStart: "12.5%", ExcludeEnd: "2.25", LimitStart: &start, LimitEnd: &end, Audio: true, Preset: "private"}
	request := PreviewReplayRequest(opts, &duration)
	if *request.SegmentSeconds != 0.1 || *request.ExcludeStart != 12.5 || request.ExcludeStartUnit != "percent" || *request.ExcludeEnd != 2.25 || request.ExcludeEndUnit != "seconds" || *request.DurationSeconds != 6 {
		t.Fatal(request)
	}
	if PreviewSegmentDuration(opts) != 0.75 {
		t.Fatal("fixture no longer exercises clamping")
	}
}

func TestPreviewReplayAdaptiveOutputFailureDecoratesLeafBeforeJoin(t *testing.T) {
	g := metadataTestGenerator(t, "for output do :;done\nprintf synthetic > \"$output\"\n", generationbudget.Settings{MaxProcesses: 2, MaxGPUProcesses: 1})
	budget, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
		return generationbudget.Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1}
	}, func(int) generationbudget.ProcessResources {
		return generationbudget.ProcessResources{Memory: 32 << 20, GPU: 64 << 20}
	})
	if err != nil {
		t.Fatal(err)
	}
	g.Budget = budget
	if err := os.WriteFile(g.Probe.Path(), []byte("#!/bin/sh\nprintf '%s' '{\"streams\":[{\"codec_type\":\"video\",\"codec_name\":\"h264\",\"nb_read_packets\":\"0\"}]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source := ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StreamIndex: 3, BitDepth: 8}
	plan, err := ffmpeg.NewIntelPreviewPlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, source, "synthetic", 7.5, PreviewWidth)
	if err != nil {
		t.Fatal(err)
	}
	command := ffmpeg.PreviewReplayCommand(plan, 7.5, PreviewWidth, "")
	plan.PreviewReplay = &command
	g.previewIntelPlan = &plan
	g = g.withGenerationWorkload(plan, "scene-preview", "synthetic")
	if !budget.CanTuneGPU() {
		t.Fatal("fixture is not adaptive")
	}
	paths := previewTestPaths{markerTestPaths{t.TempDir()}}
	g.ScenePaths = paths
	var saved []diagnostics.PreviewReplayRecord
	var mu sync.Mutex
	ctx := diagnostics.WithPreviewRequest(diagnostics.WithPreviewSource(context.Background(), 17, 29, "", func(r diagnostics.PreviewReplayRecord) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, r)
		return nil
	}), PreviewReplayRequest(PreviewOptions{Segments: 2, SegmentDuration: 0.75}, nil))
	lock := g.LockManager.ReadLock(ctx, "synthetic")
	defer lock.Cancel()
	err = g.previewVideo("synthetic", 30, PreviewOptions{Segments: 2, SegmentDuration: 0.75}, "", false, false)(lock, paths.GetVideoPreviewPath(""))
	var outputError *ffmpeg.GenerationOutputError
	if !errors.As(err, &outputError) {
		t.Fatal("original validation error lost", err)
	}
	joined := err
	err = diagnostics.WithPreviewFailure(lock, err, diagnostics.PreviewCommand{Stage: "generation"})
	if err != joined {
		t.Fatal("generic wrapper must not replace joined leaves")
	}
	entries := diagnostics.Split(err).Entries
	// The existing pool returns its first failure even when a concurrent
	// sibling independently completes the same failing validation.
	if len(entries) != 1 || len(saved) < 1 || len(saved) > 2 {
		t.Fatal("failure leaf or retention duplicated/lost", entries, saved)
	}
	replay, ok := diagnostics.PreviewFailureSnapshot(entries[0].Err)
	found := false
	for _, record := range saved {
		if record.SceneID != 17 || record.FileID != 29 || record.Replay.Command.Stage != "output" {
			t.Fatal("validation mapped a different source/stage", record)
		}
		if record.Replay.Reference == replay.Reference {
			found = true
		}
	}
	if !ok || !found || replay.Command.Stage != "output" || replay.Command.SeekSeconds != nil || replay.Command.VideoSelection != "first_video" || replay.Command.FilterConfigured || !replay.SourceResolved {
		t.Fatalf("adaptive output leaf lacks its own mapping: %+v saved=%+v error=%v", replay, saved, err)
	}
	if !errors.As(entries[0].Err, &outputError) || budget.HasMeasuredCapacity(*g.capacityWorkload) {
		t.Fatal("validation identity or failed sample policy changed", err)
	}
	if _, err := os.Stat(paths.GetVideoPreviewPath("")); !os.IsNotExist(err) {
		t.Fatal("invalid output published", err)
	}
}
