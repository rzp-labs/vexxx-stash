package generate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

const (
	scenePreviewWidth        = 640
	scenePreviewAudioBitrate = "128k"

	scenePreviewImageFPS = 12

	minSegmentDuration = 0.75
)

type PreviewOptions struct {
	Segments        int
	SegmentDuration float64
	ExcludeStart    string
	ExcludeEnd      string

	LimitStart *float64
	LimitEnd   *float64

	Preset string

	Audio bool
}

func getExcludeValue(videoDuration float64, v string) float64 {
	if strings.HasSuffix(v, "%") && len(v) > 1 {
		// proportion of video duration
		v = v[0 : len(v)-1]
		prop, _ := strconv.ParseFloat(v, 64)
		return prop / 100.0 * videoDuration
	}

	prop, _ := strconv.ParseFloat(v, 64)
	return prop
}

// getStepSizeAndOffset calculates the step size for preview generation and
// the starting offset.
//
// Step size is calculated based on the duration of the video file, minus the
// excluded duration. The offset is based on the ExcludeStart. If the total
// excluded duration exceeds the duration of the video, then offset is 0, and
// the video duration is used to calculate the step size.
func (g PreviewOptions) getStepSizeAndOffset(videoDuration float64) (stepSize float64, offset float64) {
	if g.LimitStart != nil && g.LimitEnd != nil {
		offset = *g.LimitStart
		duration := *g.LimitEnd - *g.LimitStart

		stepSize = duration / float64(g.Segments)
		return
	}

	excludeStart := getExcludeValue(videoDuration, g.ExcludeStart)
	excludeEnd := getExcludeValue(videoDuration, g.ExcludeEnd)

	duration := videoDuration
	if videoDuration > excludeStart+excludeEnd {
		duration = duration - excludeStart - excludeEnd
		offset = excludeStart
	}

	stepSize = duration / float64(g.Segments)
	return
}

func (g Generator) PreviewVideo(ctx context.Context, input string, videoDuration float64, hash string, options PreviewOptions, vrMode string, fallback bool, useVsync2 bool) error {
	done := make(chan struct{})
	lockCtx := g.LockManager.ReadLockWithCompletion(ctx, input, done)
	defer lockCtx.Cancel()
	defer close(done)

	output := g.ScenePaths.GetVideoPreviewPath(hash)
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	logger.Infof("[generator] generating video preview for %s", input)

	if g.IntelPreviews != nil && g.IntelPreviews.Enabled() {
		g = g.WithIntelGenerationBudget()
	}
	if err := g.generateFile(lockCtx, g.ScenePaths, mp4Pattern, output, g.scenePreviewVideo(input, videoDuration, options, vrMode, fallback, useVsync2)); err != nil {
		return err
	}

	logger.Debug("created video preview: ", output)

	return nil
}

func (g *Generator) previewVideo(input string, videoDuration float64, options PreviewOptions, vrMode string, fallback bool, useVsync2 bool) generateFn {
	if options.Segments < 1 {
		return func(*fsutil.LockContext, string) error {
			return fmt.Errorf("scene preview requires at least one segment")
		}
	}
	// #2496 - generate a single preview video for videos shorter than segments * segment duration
	if videoDuration < options.SegmentDuration*float64(options.Segments) {
		return g.previewVideoSingle(input, videoDuration, options, vrMode, fallback, useVsync2)
	}

	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		// a list of tmp files used during the preview generation
		var tmpFiles []string

		// remove tmpFiles when done
		defer func() { removeFiles(tmpFiles) }()

		stepSize, offset := options.getStepSizeAndOffset(videoDuration)

		segmentDuration := options.SegmentDuration
		// TODO - move this out into calling function
		// a very short duration can create files without a video stream
		if segmentDuration < minSegmentDuration {
			segmentDuration = minSegmentDuration
			logger.Warnf("[generator] Segment duration (%f) too short. Using %f instead.", options.SegmentDuration, minSegmentDuration)
		}

		chunks := make([]previewChunkOptions, options.Segments)
		for i := range chunks {
			chunkFile, err := g.tempFile(g.ScenePaths, mp4Pattern)
			if err != nil {
				return fmt.Errorf("generating video preview chunk file: %w", err)
			}
			tmpFiles = append(tmpFiles, chunkFile.Name())
			chunks[i] = previewChunkOptions{StartTime: offset + float64(i)*stepSize, Duration: segmentDuration,
				OutputPath: chunkFile.Name(), Audio: options.Audio, Preset: options.Preset}
		}
		workers := 1 // Legacy software defaults remain sequential.
		if budget := g.generationBudget(); budget != nil {
			workers = budget.Settings().MaxProcesses
			if g.previewIntelPlan != nil {
				workers = budget.Settings().MaxGPUProcesses
			}
		}
		if err := runPreviewChunks(lockCtx, len(chunks), workers, func(ctx context.Context, i int) error {
			return g.previewVideoChunk(lockCtx, ctx, input, chunks[i], vrMode, fallback, useVsync2)
		}); err != nil {
			return err
		}

		// generate concat file based on generated video chunks
		concatFilePath, err := g.generateConcatFile(tmpFiles)
		if concatFilePath != "" {
			tmpFiles = append(tmpFiles, concatFilePath)
		}

		if err != nil {
			return err
		}

		return g.previewVideoChunkCombine(lockCtx, concatFilePath, tmpFn)
	}
}

func (g *Generator) previewVideoSingle(input string, videoDuration float64, options PreviewOptions, vrMode string, fallback bool, useVsync2 bool) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		startTime := 0.0
		if options.LimitStart != nil {
			startTime = *options.LimitStart
		}

		chunkOptions := previewChunkOptions{
			StartTime:  startTime,
			Duration:   videoDuration,
			OutputPath: tmpFn,
			Audio:      options.Audio,
			Preset:     options.Preset,
		}

		return g.previewVideoChunk(lockCtx, lockCtx, input, chunkOptions, vrMode, fallback, useVsync2)
	}
}

type previewChunkOptions struct {
	StartTime  float64
	Duration   float64
	OutputPath string
	Audio      bool
	Preset     string
}

func (g Generator) previewVideoChunk(lockCtx *fsutil.LockContext, ctx context.Context, fn string, options previewChunkOptions, vrMode string, fallback bool, useVsync2 bool) error {
	var videoFilter ffmpeg.VideoFilter
	if vrMode == "LR180" {
		videoFilter = videoFilter.Append("v360=input=hequirect:output=flat:in_stereo=sbs:out_stereo=2d:d_fov=120:w=1280:h=720")
	} else if vrMode == "TB360" {
		videoFilter = videoFilter.Append("v360=input=equirect:output=flat:in_stereo=tb:out_stereo=2d:d_fov=120:w=1280:h=720")
	} else if vrMode == "MONO360" {
		videoFilter = videoFilter.Append("v360=input=equirect:output=flat:in_stereo=2d:out_stereo=2d:d_fov=120:w=1280:h=720")
	} else if vrMode == "FISHEYE190" {
		videoFilter = videoFilter.Append("v360=input=fisheye:ih_fov=190:iv_fov=190:in_stereo=sbs:out_stereo=2d:output=flat:d_fov=120:w=1280:h=720")
	}
	videoFilter = videoFilter.ScaleWidth(scenePreviewWidth)

	var videoArgs ffmpeg.Args
	videoArgs = videoArgs.VideoFilter(videoFilter)

	videoArgs = append(videoArgs,
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-level", "4.2",
		"-preset", options.Preset,
		"-crf", "21",
		"-threads", "4",
		"-strict", "-2",
	)

	if useVsync2 {
		videoArgs = append(videoArgs, "-vsync", "2")
	}

	trimOptions := transcoder.TranscodeOptions{
		OutputPath: options.OutputPath,
		StartTime:  options.StartTime,
		Duration:   options.Duration,

		XError:   !fallback,
		SlowSeek: fallback,

		VideoCodec: ffmpeg.VideoCodecLibX264,
		VideoArgs:  videoArgs,

		// The canonical software branch excludes playback hardware arguments.
		// Scene VAAPI generation is selected independently and shares the
		// configured generation process budget.
	}

	if options.Audio {
		var audioArgs ffmpeg.Args
		audioArgs = audioArgs.AudioBitrate(scenePreviewAudioBitrate)

		trimOptions.AudioCodec = ffmpeg.AudioCodecAAC
		trimOptions.AudioArgs = audioArgs
	}

	if g.previewIntelPlan != nil {
		return g.generateWithContext(ctx, lockCtx, previewIntelArgs(fn, options, *g.previewIntelPlan, useVsync2))
	}
	args := transcoder.Transcode(fn, trimOptions)

	return g.generateWithContext(ctx, lockCtx, args)
}

func (g Generator) generateConcatFile(chunkFiles []string) (fn string, err error) {
	concatFile, err := g.ScenePaths.TempFile(txtPattern)
	if err != nil {
		return "", fmt.Errorf("creating concat file: %w", err)
	}
	defer concatFile.Close()

	w := bufio.NewWriter(concatFile)
	for _, f := range chunkFiles {
		// files in concat file should be relative to concat
		relFile := filepath.Base(f)
		if _, err := w.WriteString(fmt.Sprintf("file '%s'\n", relFile)); err != nil {
			return concatFile.Name(), fmt.Errorf("writing concat file: %w", err)
		}
	}
	return concatFile.Name(), w.Flush()
}

func (g Generator) previewVideoChunkCombine(lockCtx *fsutil.LockContext, concatFilePath string, outputPath string) error {
	spliceOptions := transcoder.SpliceOptions{
		OutputPath: outputPath,
	}

	args := transcoder.Splice(concatFilePath, spliceOptions)

	return g.generate(lockCtx, args)
}

func removeFiles(list []string) {
	for _, f := range list {
		if err := os.Remove(f); err != nil {
			logger.Warnf("[generator] Delete error: %s", err)
		}
	}
}

// PreviewWebp generates a webp file based on the preview video input.
// TODO - this should really generate a new webp using chunks.
func (g Generator) PreviewWebp(ctx context.Context, input string, hash string) error {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	output := g.ScenePaths.GetWebpPreviewPath(hash)
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	logger.Infof("[generator] generating webp preview for %s", input)
	g.reportPreview(ffmpeg.IntelGenerationDiagnostic{Selected: "software", Actual: "software", Stage: "webp", Reason: "lossless WebP encoding is CPU work"})

	src := g.ScenePaths.GetVideoPreviewPath(hash)

	if err := g.generateFile(lockCtx, g.ScenePaths, webpPattern, output, g.previewVideoToImage(src)); err != nil {
		return err
	}

	logger.Debug("created video preview: ", output)

	return nil
}

func (g Generator) previewVideoToImage(input string) generateFn {
	return func(lockCtx *fsutil.LockContext, tmpFn string) error {
		var videoFilter ffmpeg.VideoFilter
		videoFilter = videoFilter.ScaleWidth(scenePreviewWidth)
		videoFilter = videoFilter.Fps(scenePreviewImageFPS)

		var videoArgs ffmpeg.Args
		videoArgs = videoArgs.VideoFilter(videoFilter)

		videoArgs = append(videoArgs,
			"-lossless", "1",
			"-q:v", "70",
			"-compression_level", "6",
			// A named libwebp preset overrides lossless and compression level.
			// Keep both explicitly requested settings effective.
			"-preset", "none",
			"-loop", "0",
			"-threads", "4",
		)

		encodeOptions := transcoder.TranscodeOptions{
			OutputPath: tmpFn,

			VideoCodec: ffmpeg.VideoCodecLibWebP,
			VideoArgs:  videoArgs,

			ExtraInputArgs:  g.FFMpegConfig.GetTranscodeInputArgs(),
			ExtraOutputArgs: g.FFMpegConfig.GetTranscodeOutputArgs(),
		}

		args := transcoder.Transcode(input, encodeOptions)

		return g.generate(lockCtx, args)
	}
}

// Queue coordinators hold no permits. Each subprocess takes the existing shared
// budget; cancellation stops admission and drains every worker before cleanup.
func runPreviewChunks(ctx context.Context, count, workers int, run func(context.Context, int) error) error {
	if count < 1 {
		return fmt.Errorf("scene preview requires at least one segment")
	}
	if workers < 1 {
		workers = 1
	}
	if workers > count {
		workers = count
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if workCtx.Err() != nil {
					continue
				}
				if err := run(workCtx, i); err != nil {
					mu.Lock()
					if len(errs) == 0 {
						errs = append(errs, fmt.Errorf("preview segment %d: %w", i, err))
					}
					mu.Unlock()
					cancel()
				}
			}
		}()
	}
send:
	for i := 0; i < count; i++ {
		select {
		case jobs <- i:
		case <-workCtx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(errs...)
}
