package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// generationMetadataStage acquires only for one metadata subprocess. Source
// probes release before frame counting or extraction acquires its own permit.
// This policy belongs to generation, never to the shared scan/playback probe.
func (s *Manager) generationMetadataStage(ctx context.Context, run func(context.Context, int) error) error {
	return s.generationMetadataStageWithTimeout(ctx, 10*time.Second, run)
}

func (s *Manager) generationMetadataStageWithTimeout(ctx context.Context, timeout time.Duration, run func(context.Context, int) error) error {
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
		// The execution limit starts after admission, never while queueing.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
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
