package manager

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/scene/generate"
)

// generationMetadataStage acquires only for one metadata subprocess. Source
// probes release before frame counting or extraction acquires its own permit.
// This policy belongs to generation, never to the shared scan/playback probe.
// Preserve the caller's cancellation/deadline: full-file metadata and frame
// counts may legitimately outlast the separate hardware capability probes.
func (s *Manager) generationMetadataStage(ctx context.Context, run func(context.Context, int) error) error {
	var budget *generationbudget.Budget
	if s.Config != nil {
		budget = s.Config.GetGenerationBudget()
	}
	release, err := budget.Acquire(ctx, generationbudget.CPU)
	if err != nil {
		return fmt.Errorf("waiting for generation metadata budget: %w", err)
	}
	defer release()
	threads := 0
	if budget != nil {
		threads = budget.Settings().Threads
	}
	return run(ctx, threads)
}

func (s *Manager) generationVideoFile(ctx context.Context, path string) (*ffmpeg.VideoFile, error) {
	var result *ffmpeg.VideoFile
	err := s.generationMetadataStage(ctx, func(ctx context.Context, threads int) error {
		if s.FFProbe == nil {
			return fmt.Errorf("ffprobe unavailable for generation metadata")
		}
		var err error
		result, err = s.FFProbe.NewVideoFileContext(ctx, path, threads)
		return err
	})
	return result, err
}

func (s *Manager) generationFrameRate(ctx context.Context, file *ffmpeg.VideoFile) (*ffmpeg.FrameInfo, error) {
	var result *ffmpeg.FrameInfo
	err := s.generationMetadataStage(ctx, func(ctx context.Context, threads int) error {
		if s.FFMpeg == nil {
			return fmt.Errorf("ffmpeg unavailable for generation frame count")
		}
		var err error
		result, err = s.FFMpeg.CalculateFrameRateWithThreads(ctx, file, threads)
		return err
	})
	return result, err
}

func (s *Manager) generationReadFrameCount(ctx context.Context, path string) (int64, error) {
	var result int64
	err := s.generationMetadataStage(ctx, func(ctx context.Context, threads int) error {
		if s.FFProbe == nil {
			return fmt.Errorf("ffprobe unavailable for generation frame count")
		}
		var err error
		result, err = s.FFProbe.GetReadFrameCountContext(ctx, path, threads)
		return err
	})
	return result, err
}

func (s *Manager) generationPreviewVideoFile(ctx context.Context, path string) (*ffmpeg.VideoFile, error) {
	if s.Config == nil || s.Config.GetIntelPreviewGeneration() == nil {
		return s.generationVideoFile(ctx, path)
	}
	return s.generationHardwareVideoFile(ctx, path, *s.Config.GetIntelPreviewGeneration())
}

func (s *Manager) generationSpriteVideoFile(ctx context.Context, path string) (*ffmpeg.VideoFile, error) {
	if s.Config == nil || s.Config.GetIntelSpriteGeneration() == nil {
		return s.generationVideoFile(ctx, path)
	}
	return s.generationHardwareVideoFile(ctx, path, *s.Config.GetIntelSpriteGeneration())
}

func (s *Manager) generationHardwareVideoFile(ctx context.Context, path string, intel ffmpeg.IntelGenerationConfig) (*ffmpeg.VideoFile, error) {
	budget := s.Config.GetIntelGenerationBudget()
	release, err := budget.Acquire(ctx, generationbudget.CPU)
	if err != nil {
		return nil, err
	}
	if s.FFProbe == nil {
		release()
		return nil, fmt.Errorf("ffprobe unavailable for GPU generation metadata")
	}
	base, err := s.FFProbe.NewVideoFileMetadataContext(ctx, path, budget.Settings().Threads)
	release()
	if err != nil {
		return nil, err
	}
	g := generate.Generator{Encoder: s.FFMpeg, Probe: s.FFProbe, LockManager: s.ReadLockManager, FFMpegConfig: s.Config}
	source, err := g.IntelSourceMetadata(ctx, path, intel)
	if err != nil {
		return nil, err
	}
	if err := applySelectedGPUVideoSource(base, source); err != nil {
		return nil, err
	}
	return base, nil
}

func applySelectedGPUVideoSource(file *ffmpeg.VideoFile, source ffmpeg.IntelSource) error {
	var selected *ffmpeg.FFProbeStream
	for index := range file.JSON.Streams {
		stream := &file.JSON.Streams[index]
		if stream.CodecType == "video" && stream.Index == source.StreamIndex {
			selected = stream
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("GPU-selected stream %d missing from source headers", source.StreamIndex)
	}
	file.VideoStream, file.VideoCodec = selected, source.Codec
	file.VideoBitrate, _ = strconv.ParseInt(selected.BitRate, 10, 64)
	file.FrameCount = 0
	if count, err := strconv.ParseInt(selected.NbFrames, 10, 64); err == nil && count > 0 {
		file.FrameCount = count
	}
	if count, err := strconv.ParseInt(selected.NbReadFrames, 10, 64); err == nil && count > 0 {
		file.FrameCount = count
	}
	selected.CodecName, selected.PixFmt = source.Codec, source.PixelFormat
	selected.Profile = source.Profile
	selected.Width, selected.Height = source.Width, source.Height
	selected.RFrameRate, selected.AvgFrameRate = source.FrameRate, source.AverageFrameRate
	selected.Duration = source.Duration
	selected.SampleAspectRatio, selected.DisplayAspectRatio = source.SampleAspectRatio, source.DisplayAspectRatio
	selected.ColorRange, selected.ColorSpace = source.ColorRange, source.ColorSpace
	selected.ColorPrimaries, selected.ColorTransfer = source.ColorPrimaries, source.ColorTransfer
	file.Width, file.Height = ffmpeg.IntelDisplayDimensions(source)
	file.Rotation = int64(source.Rotation)
	file.StartTime, _ = strconv.ParseFloat(source.StartTime, 64)
	if math.IsInf(file.StartTime, 0) || math.IsNaN(file.StartTime) {
		file.StartTime = 0
	}
	file.FrameRate = 0
	for _, value := range []string{source.AverageFrameRate, source.FrameRate} {
		if rate, ok := new(big.Rat).SetString(value); ok && rate.Sign() > 0 {
			candidate, _ := rate.Float64()
			if candidate > 0 && !math.IsInf(candidate, 0) && !math.IsNaN(candidate) {
				// Preserve the existing VideoFile average-rate rounding contract.
				candidate = math.Round(candidate*100) / 100
				if !math.IsInf(candidate, 0) {
					file.FrameRate = candidate
					break
				}
			}
		}
	}
	file.VideoStreamDuration, _ = strconv.ParseFloat(source.Duration, 64)
	if file.VideoStreamDuration <= 0 || math.IsInf(file.VideoStreamDuration, 0) || math.IsNaN(file.VideoStreamDuration) {
		file.VideoStreamDuration = 0
		if file.FileDuration > 0 && !math.IsInf(file.FileDuration, 0) && !math.IsNaN(file.FileDuration) {
			file.VideoStreamDuration = file.FileDuration
		}
	}
	return nil
}
