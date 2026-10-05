package generate

import (
	"context"
	"errors"
	"fmt"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/logger"
)

func previewIntelArgs(input string, options previewChunkOptions, plan ffmpeg.IntelGenerationPlan, useVsync2 bool) ffmpeg.Args {
	// QP21 is an initial scene candidate, not an equivalence to x264 CRF21.
	video := ffmpeg.Args{"-vf", plan.Filter, "-profile:v", "high", "-level:v", "4.2", "-qp", "21"}
	if useVsync2 {
		video = append(video, "-vsync", "2")
	}
	// Retain known source color tags. Do not invent tags for unspecified media.
	for _, tag := range []struct{ key, value string }{
		{"-color_primaries", plan.Source.ColorPrimaries}, {"-color_trc", plan.Source.ColorTransfer},
		{"-colorspace", plan.Source.ColorSpace}, {"-color_range", plan.Source.ColorRange},
	} {
		if tag.value != "" && tag.value != "unknown" && tag.value != "unspecified" {
			video = append(video, tag.key, tag.value)
		}
	}
	o := transcoder.TranscodeOptions{OutputPath: options.OutputPath, StartTime: options.StartTime, Duration: options.Duration,
		XError: true, VideoCodec: ffmpeg.VideoCodec{Name: "h264_vaapi", CodeName: "h264_vaapi"}, VideoArgs: video,
		ExtraInputArgs: plan.InputArgs, ExtraOutputArgs: []string{"-map", fmt.Sprintf("0:%d", plan.Source.StreamIndex)}}
	if options.Audio {
		o.AudioCodec = ffmpeg.AudioCodecAAC
		o.AudioArgs = ffmpeg.Args{}.AudioBitrate(scenePreviewAudioBitrate)
		o.ExtraOutputArgs = append(o.ExtraOutputArgs, "-map", "0:a:0?")
	}
	return transcoder.Transcode(input, o)
}

func (g Generator) reportPreview(d ffmpeg.IntelGenerationDiagnostic) {
	if g.IntelDiagnostic != nil {
		g.IntelDiagnostic(d)
	}
	logger.Infof("scene preview selected=%s actual=%s stage=%s reason=%s", d.Selected, d.Actual, d.Stage, d.Reason)
}

func (g Generator) scenePreviewVideo(input string, duration float64, options PreviewOptions, vr string, fallback, vsync2 bool) generateFn {
	return func(lockCtx *fsutil.LockContext, output string) error {
		if err := lockCtx.Err(); err != nil {
			return err
		}
		if g.IntelPreviews == nil || !g.IntelPreviews.Enabled() {
			g.reportPreview(ffmpeg.IntelGenerationDiagnostic{Selected: "software", Actual: "software"})
			return g.previewVideo(input, duration, options, vr, fallback, vsync2)(lockCtx, output)
		}
		// The entire CPU retry uses existing canonical slow seeking. No partial
		// hardware chunks are mixed with software chunks in the final concat.
		software := func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			cpu := g
			cpu.previewIntelPlan = nil
			if err := cpu.previewVideo(input, duration, options, vr, true, vsync2)(lockCtx, output); err != nil {
				return err
			}
			return cpu.validateIntelMarkerOutput(ctx, output)
		}
		fallbackToCPU := func(stage string, reason error) error {
			if err := lockCtx.Err(); err != nil {
				return err
			}
			err := software(lockCtx)
			var outputErr *ffmpeg.GenerationOutputError
			if errors.As(err, &outputErr) {
				stage = "output"
				reason = err
			}
			g.reportPreview(ffmpeg.IntelGenerationDiagnostic{Selected: g.IntelPreviews.Backend, Actual: "software", Stage: stage, Reason: reason.Error()})
			return err
		}
		if vr != "" {
			return fallbackToCPU("eligibility", fmt.Errorf("VR projection requires software scene previews"))
		}
		release, err := g.generationBudget().Acquire(lockCtx, generationbudget.CPU)
		if err != nil {
			return err
		}
		source, err := g.Probe.IntelPreviewSource(lockCtx, input)
		release()
		if err != nil {
			return fallbackToCPU("eligibility", err)
		}
		start := 0.0
		if PreviewIsSingleSegment(options, duration) {
			if options.LimitStart != nil {
				start = *options.LimitStart
			}
		} else {
			_, start = PreviewStepSizeAndOffset(options, duration)
		}
		plan, err := ffmpeg.NewIntelPreviewPlan(*g.IntelPreviews, source, input, start, PreviewWidth)
		if err != nil {
			return fallbackToCPU("eligibility", err)
		}
		runner := func(ctx context.Context, args ffmpeg.Args) error { return g.generateWithContext(ctx, lockCtx, args) }
		d, err := ffmpeg.RunIntelGenerationWork(lockCtx, plan, func(ctx context.Context) error {
			hw := g
			hw.previewIntelPlan = &plan
			if err := hw.previewVideo(input, duration, options, vr, false, vsync2)(lockCtx, output); err != nil {
				return err
			}
			return hw.validateIntelMarkerOutput(ctx, output)
		}, software, runner)
		g.reportPreview(d)
		return err
	}
}
