package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"github.com/stashapp/stash/pkg/diagnostics"
	"strings"
	"testing"
)

func TestIntelPreviewReplayProbeStagesUseActualPlanAndSeek(t *testing.T) {
	for _, stage := range []string{"decode", "filter", "encode", "device"} {
		t.Run(stage, func(t *testing.T) {
			source := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 720, Height: 406, StreamIndex: 2, SampleAspectRatio: "406:405", StartTime: "123", RuntimeFingerprint: strings.Repeat("b", 64)}
			plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, source, "private source", 7.125, 640)
			if err != nil {
				t.Fatal(err)
			}
			command := PreviewReplayCommand(plan, 7.125, 640, "")
			plan.PreviewReplay = &command
			var records []diagnostics.PreviewReplayRecord
			ctx := diagnostics.WithPreviewSource(context.Background(), 1, 2, "", func(r diagnostics.PreviewReplayRecord) error { records = append(records, r); return nil })
			want := errors.New("controlled probe failure")
			index := 0
			run := func(_ context.Context, args Args) error {
				p := plan.Probes[index]
				index++
				if !strings.Contains(strings.Join(args, " "), "-ss 7.125 -i private source -map 0:2 -an -frames:v 1") {
					t.Fatal("test did not exercise actual command", args)
				}
				if p.Stage == stage {
					return want
				}
				return nil
			}
			check := func(string) error {
				if stage == "device" {
					return want
				}
				return nil
			}
			_, err = runIntelGenerationWork(ctx, plan, func(context.Context) error { t.Fatal("hardware after failed stage"); return nil }, nil, run, check)
			r, ok := diagnostics.PreviewFailureSnapshot(err)
			if !ok || !errors.Is(err, want) || len(records) != 1 || r.Command.Stage != stage {
				t.Fatal("stage/source/error", r, err, records)
			}
			if stage == "device" {
				if r.Command.SeekSeconds != nil || r.Command.FilterConfigured {
					t.Fatal("device invented execution", r.Command)
				}
			} else {
				if *r.Command.SeekSeconds != 7.125 || *r.Command.FrameLimit != 1 || *r.Command.StreamIndex != 2 || r.Command.DurationSeconds != nil {
					t.Fatal("probe facts", r.Command)
				}
				if r.Command.FilterConfigured != (stage != "decode") {
					t.Fatal("decode incorrectly applied filter")
				}
			}
			if r.Command.InputPixels.Width != 720 || r.Command.InputPixels.PixelFormat != "yuv420p" || r.Command.InputPixels.IsRGB || *r.Command.InputPixels.SARNum != 406 || *r.Command.InputPixels.SARDen != 405 || r.Command.RuntimeFingerprint != source.RuntimeFingerprint {
				t.Fatal("plan source facts", r.Command)
			}
		})
	}
}

func TestIntelPreviewReplaySeekIsSeparateFromDemuxOrigin(t *testing.T) {
	source := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StartTime: "123", StreamIndex: 4}
	for _, start := range []float64{0, -5, 2.25} {
		plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input", start, 640)
		if err != nil {
			t.Fatal(err)
		}
		command := PreviewReplayCommand(plan, start, 640, "")
		want := start
		if want < 0 {
			want = 0
		}
		if *command.SeekSeconds != want || *command.TargetHeight != 360 {
			t.Fatal(command)
		}
		args := strings.Join(plan.Probes[0].Args, " ")
		if start > 0 && !strings.Contains(args, fmt.Sprintf("-ss %v", start)) {
			t.Fatal(args)
		}
		if start <= 0 && strings.Contains(args, "-ss") {
			t.Fatal(args)
		}
	}
}

func TestIntelPreviewReplayProjectionProbeRetainsActualStage(t *testing.T) {
	for _, mode := range []string{"LR180", "TB360", "MONO360", "FISHEYE190"} {
		t.Run(mode, func(t *testing.T) {
			source := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 8192, Height: 4096, BitDepth: 8, StreamIndex: 2, ColorSpace: "bt709", ColorPrimaries: "bt709", ColorTransfer: "bt709", ColorRange: "tv"}
			plan, err := NewIntelProjectedPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "synthetic", 7.125, 640, mode)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Probes[1].Stage != "projection" {
				t.Fatal("fixture stage", plan.Probes)
			}
			command := PreviewReplayCommand(plan, 7.125, 640, mode)
			plan.PreviewReplay = &command
			var saved []diagnostics.PreviewReplayRecord
			ctx := diagnostics.WithPreviewSource(context.Background(), 1, 2, "", func(record diagnostics.PreviewReplayRecord) error { saved = append(saved, record); return nil })
			calls := 0
			refusal := errors.New("controlled projection refusal")
			_, err = runIntelGenerationWork(ctx, plan, func(context.Context) error { t.Fatal("render reached"); return nil }, nil, func(_ context.Context, args Args) error {
				calls++
				if calls == 2 {
					if !strings.Contains(strings.Join(args, " "), "-ss 7.125 -i synthetic -map 0:2 -an -frames:v 1 -vf ") {
						t.Fatal("unexpected projection command", args)
					}
					return refusal
				}
				return nil
			}, func(string) error { return nil })
			replay, ok := diagnostics.PreviewFailureSnapshot(err)
			if !errors.Is(err, refusal) || !ok || replay.Command.Stage != "projection" || replay.Command.Projection != mode || !replay.Command.FilterConfigured || len(saved) != 1 || saved[0].Replay.Command.Stage != "projection" {
				t.Fatalf("known projection stage lost: replay=%+v saved=%+v error=%v", replay, saved, err)
			}
		})
	}
}
