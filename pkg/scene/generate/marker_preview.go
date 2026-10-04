package generate

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/logger"
)

const (
	markerPreviewWidth        = 640
	maxMarkerPreviewDuration  = 20
	markerPreviewAudioBitrate = "64k"

	markerImageDuration = 5
	markerWebpFPS       = 12

	markerScreenshotQuality = 2
)

func (g Generator) MarkerPreviewVideo(ctx context.Context, input string, hash string, seconds float64, endSeconds *float64, includeAudio bool, vrMode string) error {
	done := make(chan struct{})
	lockCtx := g.LockManager.ReadLockWithCompletion(ctx, input, done)
	defer lockCtx.Cancel()
	defer close(done)

	output := g.MarkerPaths.GetVideoPreviewPath(hash, int(seconds))
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	duration := MarkerPreviewDuration(seconds, endSeconds)
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return fmt.Errorf("marker preview requires a finite nonnegative start and positive duration")
	}

	if err := g.generateFile(lockCtx, g.MarkerPaths, mp4Pattern, output, g.markerPreviewVideo(input, sceneMarkerOptions{
		Seconds:  seconds,
		Duration: duration,
		Audio:    includeAudio,
		VRMode:   vrMode,
	})); err != nil {
		return err
	}

	logger.Debug("created marker video: ", output)

	return nil
}

type sceneMarkerOptions struct {
	Seconds  float64
	Duration float64
	Audio    bool
	VRMode   string
}

func (g Generator) markerPreviewVideo(input string, options sceneMarkerOptions) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		var videoFilter ffmpeg.VideoFilter
		switch options.VRMode {
		case "LR180":
			videoFilter = videoFilter.Append("v360=input=hequirect:output=flat:in_stereo=sbs:out_stereo=2d:d_fov=120:w=1280:h=720")
		case "TB360":
			videoFilter = videoFilter.Append("v360=input=equirect:output=flat:in_stereo=tb:out_stereo=2d:d_fov=120:w=1280:h=720")
		case "MONO360":
			videoFilter = videoFilter.Append("v360=input=equirect:output=flat:in_stereo=2d:out_stereo=2d:d_fov=120:w=1280:h=720")
		case "FISHEYE190":
			videoFilter = videoFilter.Append("v360=input=fisheye:ih_fov=190:iv_fov=190:in_stereo=sbs:out_stereo=2d:output=flat:d_fov=120:w=1280:h=720")
		}
		videoFilter = videoFilter.ScaleWidth(markerPreviewWidth)

		var videoArgs ffmpeg.Args
		videoArgs = videoArgs.VideoFilter(videoFilter)

		videoArgs = append(videoArgs,
			"-pix_fmt", "yuv420p",
			"-profile:v", "high",
			"-level", "4.2",
			"-preset", "veryslow",
			"-crf", "24",
			"-movflags", "+faststart",
			"-threads", "4",
			"-sws_flags", "lanczos",
			"-strict", "-2",
		)

		trimOptions := transcoder.TranscodeOptions{
			Duration:   options.Duration,
			StartTime:  options.Seconds,
			OutputPath: tmpFn,
			VideoCodec: ffmpeg.VideoCodecLibX264,
			VideoArgs:  videoArgs,
		}

		if options.Audio {
			var audioArgs ffmpeg.Args
			audioArgs = audioArgs.AudioBitrate(markerPreviewAudioBitrate)

			trimOptions.AudioCodec = ffmpeg.AudioCodecAAC
			trimOptions.AudioArgs = audioArgs
		}

		args := transcoder.Transcode(input, trimOptions)

		return g.generateIntelMarker(lockCtx, input, tmpFn, options, args)
	}
}

// The Intel quality controls are an initial QP/ICQ 24 candidate, not an
// equivalence to x264 CRF 24. Visual/resource acceptance is required on each GPU.
func markerIntelArgs(input, output string, options sceneMarkerOptions, plan ffmpeg.IntelGenerationPlan) ffmpeg.Args {
	videoArgs := ffmpeg.Args{"-vf", plan.Filter, "-profile:v", "high", "-level:v", "4.2", "-movflags", "+faststart", "-threads", "1"}
	if plan.Config.Backend == "qsv" {
		videoArgs = append(videoArgs, "-global_quality", "24", "-preset", "medium")
	} else {
		videoArgs = append(videoArgs, "-qp", "24")
	}
	o := transcoder.TranscodeOptions{StartTime: options.Seconds, Duration: options.Duration, OutputPath: output,
		VideoCodec: ffmpeg.VideoCodec{Name: "h264_" + plan.Config.Backend, CodeName: "h264_" + plan.Config.Backend}, VideoArgs: videoArgs,
		ExtraInputArgs:  append(append([]string{}, plan.InputArgs...), "-threads", "1"),
		ExtraOutputArgs: []string{"-map", fmt.Sprintf("0:%d", plan.Source.StreamIndex)},
	}
	if options.Audio {
		o.AudioCodec = ffmpeg.AudioCodecAAC
		o.AudioArgs = ffmpeg.Args{}.AudioBitrate(markerPreviewAudioBitrate)
		o.ExtraOutputArgs = append(o.ExtraOutputArgs, "-map", "0:a:0?")
	}
	return transcoder.Transcode(input, o)
}

func (g Generator) generateIntelMarker(lockCtx *fsutil.LockContext, input, output string, options sceneMarkerOptions, software ffmpeg.Args) error {
	if err := lockCtx.Err(); err != nil {
		selected := "software"
		if g.IntelMarker != nil {
			selected = g.IntelMarker.Backend
		}
		if g.IntelDiagnostic != nil {
			g.IntelDiagnostic(ffmpeg.IntelGenerationDiagnostic{Selected: selected, Actual: "none", Stage: "cancellation", Reason: err.Error()})
		}
		return err
	}
	if g.IntelMarker == nil || !g.IntelMarker.Enabled() {
		err := g.generate(lockCtx, software)
		if g.IntelDiagnostic != nil {
			g.IntelDiagnostic(ffmpeg.IntelGenerationDiagnostic{Selected: "software", Actual: "software"})
		}
		return err
	}
	softwareAttempt := func(ctx context.Context) error {
		if err := g.generateWithContext(ctx, lockCtx, software); err != nil {
			return err
		}
		return g.validateIntelMarkerOutput(ctx, output)
	}
	fallback := func(stage string, err error) error {
		if lockCtx.Err() != nil {
			return lockCtx.Err()
		}
		d := ffmpeg.IntelGenerationDiagnostic{Selected: g.IntelMarker.Backend, Actual: "software", Stage: stage, Reason: err.Error()}
		fallbackErr := softwareAttempt(lockCtx)
		var outputErr *ffmpeg.GenerationOutputError
		if errors.As(fallbackErr, &outputErr) {
			d.Stage = "output"
			d.Reason = fallbackErr.Error()
		}
		logger.Warnf("marker generation selected=%s actual=software stage=%s reason=%s", g.IntelMarker.Backend, d.Stage, d.Reason)
		if g.IntelDiagnostic != nil {
			g.IntelDiagnostic(d)
		}
		return fallbackErr
	}
	if g.IntelMarker.Backend == "qsv" {
		return fallback("quality", fmt.Errorf("QSV marker quality mapping has not passed representative visual acceptance; software generation required"))
	}
	if options.VRMode != "" {
		return fallback("eligibility", fmt.Errorf("VR projection requires software generation"))
	}
	release, err := g.generationBudget().Acquire(lockCtx, generationbudget.CPU)
	if err != nil {
		return err
	}
	source, err := g.Probe.IntelSource(lockCtx, input)
	release()
	if err != nil {
		return fallback("metadata", err)
	}
	if source.SampleAspectRatio != "1:1" && source.SampleAspectRatio != "1/1" && source.SampleAspectRatio != "1" {
		return fallback("eligibility", fmt.Errorf("sample aspect ratio %q requires software generation until display geometry is validated", source.SampleAspectRatio))
	}
	plan, err := ffmpeg.NewIntelGenerationPlan(*g.IntelMarker, source, input, options.Seconds, markerPreviewWidth, false)
	if err != nil {
		return fallback("eligibility", err)
	}
	runner := func(ctx context.Context, args ffmpeg.Args) error {
		return g.generateWithContext(ctx, lockCtx, args)
	}
	diagnostic, err := ffmpeg.RunIntelGenerationWork(lockCtx, plan,
		func(ctx context.Context) error {
			if err := runner(ctx, markerIntelArgs(input, output, options, plan)); err != nil {
				return err
			}
			return g.validateIntelMarkerOutput(ctx, output)
		}, softwareAttempt, runner)
	if g.IntelDiagnostic != nil {
		g.IntelDiagnostic(diagnostic)
	}
	if diagnostic.Reason != "" {
		logger.Warnf("marker generation selected=%s actual=%s stage=%s reason=%s", diagnostic.Selected, diagnostic.Actual, diagnostic.Stage, diagnostic.Reason)
	} else {
		logger.Debugf("marker generation selected=%s actual=%s", diagnostic.Selected, diagnostic.Actual)
	}
	return err
}

// This runs after each candidate attempt releases its subprocess permits and
// before generateFile can atomically move the output into its final location.
func (g Generator) validateIntelMarkerOutput(ctx context.Context, output string) error {
	release, err := g.generationBudget().Acquire(ctx, generationbudget.CPU)
	if err != nil {
		return &ffmpeg.GenerationOutputError{Err: err}
	}
	defer release()
	return g.Probe.ValidateVideoOutput(ctx, output)
}

func (g Generator) SceneMarkerWebp(ctx context.Context, input string, hash string, seconds float64, vrMode string) error {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	output := g.MarkerPaths.GetWebpPreviewPath(hash, int(seconds))
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	if err := g.generateFile(lockCtx, g.MarkerPaths, webpPattern, output, g.sceneMarkerWebp(input, sceneMarkerOptions{
		Seconds: seconds,
		VRMode:  vrMode,
	})); err != nil {
		return err
	}

	logger.Debug("created marker image: ", output)

	return nil
}

func (g Generator) sceneMarkerWebp(input string, options sceneMarkerOptions) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		var videoFilter ffmpeg.VideoFilter
		switch options.VRMode {
		case "LR180":
			videoFilter = videoFilter.Append("v360=input=hequirect:output=flat:in_stereo=sbs:out_stereo=2d:d_fov=120:w=1280:h=720")
		case "TB360":
			videoFilter = videoFilter.Append("v360=input=equirect:output=flat:in_stereo=tb:out_stereo=2d:d_fov=120:w=1280:h=720")
		case "MONO360":
			videoFilter = videoFilter.Append("v360=input=equirect:output=flat:in_stereo=2d:out_stereo=2d:d_fov=120:w=1280:h=720")
		case "FISHEYE190":
			videoFilter = videoFilter.Append("v360=input=fisheye:ih_fov=190:iv_fov=190:in_stereo=sbs:out_stereo=2d:output=flat:d_fov=120:w=1280:h=720")
		}
		videoFilter = videoFilter.ScaleWidth(markerPreviewWidth)
		videoFilter = videoFilter.Fps(markerWebpFPS)

		var videoArgs ffmpeg.Args
		videoArgs = videoArgs.VideoFilter(videoFilter)
		videoArgs = append(videoArgs,
			"-lossless", "1",
			"-q:v", "70",
			"-compression_level", "6",
			// libwebp presets reset lossless and compression_level during
			// encoder initialization, regardless of CLI option order. Disable
			// the preset to honor the explicit lossless=1 and method=6 contract.
			"-preset", "none",
			"-loop", "0",
			"-threads", "4",
		)

		trimOptions := transcoder.TranscodeOptions{
			Duration:   markerImageDuration,
			StartTime:  float64(options.Seconds),
			OutputPath: tmpFn,
			VideoCodec: ffmpeg.VideoCodecLibWebP,
			VideoArgs:  videoArgs,
		}

		args := transcoder.Transcode(input, trimOptions)

		return g.generate(lockCtx, args)
	}
}

func (g Generator) SceneMarkerScreenshot(ctx context.Context, input string, hash string, seconds float64, width int, vrMode string) error {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	output := g.MarkerPaths.GetScreenshotPath(hash, int(seconds))
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	if err := g.generateFile(lockCtx, g.MarkerPaths, jpgPattern, output, g.sceneMarkerScreenshot(input, SceneMarkerScreenshotOptions{
		Seconds: seconds,
		Width:   width,
		VRMode:  vrMode,
	})); err != nil {
		return err
	}

	logger.Debug("created marker screenshot: ", output)

	return nil
}

type SceneMarkerScreenshotOptions struct {
	Seconds float64
	Width   int
	VRMode  string
}

func (g Generator) sceneMarkerScreenshot(input string, options SceneMarkerScreenshotOptions) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		ssOptions := transcoder.ScreenshotOptions{
			OutputPath: tmpFn,
			OutputType: transcoder.ScreenshotOutputTypeImage2,
			Quality:    markerScreenshotQuality,
			Width:      options.Width,
			VRMode:     options.VRMode,
		}

		args := transcoder.ScreenshotTime(input, options.Seconds, ssOptions)

		return g.generate(lockCtx, args)
	}
}
