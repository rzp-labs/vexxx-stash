package generate

import (
	"context"
	"fmt"
	"image"
	"sync"
	"sync/atomic"

	"github.com/stashapp/stash/pkg/generationbudget"
	"golang.org/x/sync/errgroup"
)

// The active configuration is the throttle. Leaf subprocesses still acquire
// the shared budget, so multiple sheets cannot multiply the process ceilings.
// Unbudgeted ordinary CPU generation retains its serial extraction behavior.
func (g Generator) spriteWorkers(class generationbudget.Class, count int) int {
	workers := 1
	if budget := g.generationBudget(); budget != nil {
		settings := budget.Settings()
		workers = settings.MaxProcesses
		if class == generationbudget.GPU {
			workers = min(workers, settings.MaxGPUProcesses)
		}
	}
	return min(workers, count)
}

// SpriteScreenshots keeps independent canonical time seeks, in requested order.
func (g Generator) SpriteScreenshots(ctx context.Context, input string, times []float64, vrMode string) ([]image.Image, error) {
	return g.softwareSpriteSequence(ctx, input, len(times), func(ctx context.Context, i int) (image.Image, error) {
		return g.SpriteScreenshot(ctx, input, times[i], vrMode)
	})
}

// SpriteScreenshotsSlow keeps canonical frame seeks, including duplicate frames.
func (g Generator) SpriteScreenshotsSlow(ctx context.Context, input string, frames []int, vrMode string) ([]image.Image, error) {
	return g.softwareSpriteSequence(ctx, input, len(frames), func(ctx context.Context, i int) (image.Image, error) {
		return g.SpriteScreenshotSlow(ctx, input, frames[i], vrMode)
	})
}

func (g Generator) softwareSpriteSequence(ctx context.Context, input string, count int, capture func(context.Context, int) (image.Image, error)) ([]image.Image, error) {
	done := make(chan struct{})
	lockCtx := g.LockManager.ReadLockWithCompletion(ctx, input, done)
	defer lockCtx.Cancel()
	defer close(done)
	return captureSpriteFrames(lockCtx, count, g.spriteWorkers(generationbudget.CPU, count), 0, 0, capture)
}

func captureSpriteSequence(ctx context.Context, times []float64, workers, width, height int, capture func(context.Context, float64) (image.Image, error)) ([]image.Image, error) {
	return captureSpriteFrames(ctx, len(times), workers, width, height, func(ctx context.Context, i int) (image.Image, error) {
		return capture(ctx, times[i])
	})
}

// Each worker owns its output index. The first failure cancels siblings, and
// Wait drains them before returning: no failed attempt can race a fallback or
// leave partial images available for composition/publication.
func captureSpriteFrames(ctx context.Context, count, workers, width, height int, capture func(context.Context, int) (image.Image, error)) ([]image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	images := make([]image.Image, count)
	group, workCtx := errgroup.WithContext(ctx)
	var next atomic.Int64
	var dimensions sync.Mutex
	for w := 0; w < min(max(workers, 1), count); w++ {
		group.Go(func() error {
			for {
				if err := workCtx.Err(); err != nil {
					return err
				}
				i := int(next.Add(1) - 1)
				if i >= count {
					return nil
				}
				img, err := capture(workCtx, i)
				if err != nil {
					return fmt.Errorf("sprite screenshot at index %d: %w", i, err)
				}
				if err := workCtx.Err(); err != nil {
					return err
				}
				if img == nil {
					return fmt.Errorf("sprite screenshot at index %d is empty", i)
				}
				// Intel validates tile dimensions; ordinary CPU sprites retain their
				// legacy handling of sources whose dimensions change mid-stream.
				if width > 0 {
					size := img.Bounds().Size()
					dimensions.Lock()
					if height == 0 {
						height = size.Y
					}
					expectedHeight := height
					dimensions.Unlock()
					if size.X != width || size.Y != expectedHeight || expectedHeight <= 0 {
						return fmt.Errorf("sprite screenshot at index %d has unexpected dimensions %v (want %dx%d)", i, size, width, expectedHeight)
					}
				}
				images[i] = img
			}
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return images, nil
}
