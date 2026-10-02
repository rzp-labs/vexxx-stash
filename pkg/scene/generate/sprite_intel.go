package generate

import (
	"context"
	"fmt"
	"image"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"

	"github.com/disintegration/imaging"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// SaveSprite bounds CPU composition/encoding and publishes only a complete
// image. A failed or cancelled encode removes its temporary output.
func (g Generator) SaveSprite(ctx context.Context, images []image.Image, output string) error {
	if len(images) == 0 {
		return fmt.Errorf("sprite images are empty")
	}
	release, err := g.generationBudget().Acquire(ctx, generationbudget.CPU)
	if err != nil {
		return err
	}
	defer release()
	// A destination-adjacent file guarantees a same-filesystem rename. SafeMove
	// can copy into the destination on EXDEV, which would expose partial output.
	tmp, err := os.CreateTemp(filepath.Dir(output), "."+filepath.Base(output)+"-*"+filepath.Ext(output))
	if err != nil {
		return fmt.Errorf("creating sprite temporary file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing sprite temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	montage := g.CombineSpriteImages(images)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := imaging.Save(montage, tmp.Name()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), output); err != nil {
		return fmt.Errorf("publishing sprite image: %w", err)
	}
	return nil
}

// IntelSpriteTiles is an opt-in, time-seeking path. It keeps the exact requested
// timestamps and downloads only scaled frames. Any failed tile discards the
// hardware sheet and runs the complete canonical software sheet once.
func (g Generator) IntelSpriteTiles(ctx context.Context, input string, times []float64) (images []image.Image, d ffmpeg.IntelGenerationDiagnostic, err error) {
	ctx, cancelCaller := context.WithCancel(ctx)
	defer cancelCaller()
	d = ffmpeg.IntelGenerationDiagnostic{Actual: "software"}
	defer func() {
		if g.IntelDiagnostic != nil {
			g.IntelDiagnostic(d)
		}
	}()
	if g.IntelSprites != nil {
		d.Selected = g.IntelSprites.Backend
	}
	if err := ctx.Err(); err != nil {
		return nil, d, err
	}
	if len(times) == 0 {
		return nil, d, fmt.Errorf("sprite timestamps are empty")
	}
	for i, at := range times {
		if math.IsNaN(at) || math.IsInf(at, 0) || at < 0 || (i > 0 && at < times[i-1]) {
			return nil, d, fmt.Errorf("invalid sprite timestamp at index %d", i)
		}
	}
	software := func(ctx context.Context) ([]image.Image, error) {
		return captureSpriteSequence(ctx, times, spriteScreenshotWidth, 0, func(ctx context.Context, at float64) (image.Image, error) {
			return g.SpriteScreenshot(ctx, input, at, "")
		})
	}
	if g.IntelSprites == nil || !g.IntelSprites.Enabled() {
		images, err := software(ctx)
		return images, d, err
	}
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()
	// Child ReadLocks propagate Cancel when their parent implements Cancellable.
	// A normal context wrapper preserves the signal without letting a completed
	// tile cancel the enclosing sheet's read lock.
	workCtx, cancel := context.WithCancel(lockCtx)
	defer cancel()
	fallback := func(stage string, cause error) ([]image.Image, ffmpeg.IntelGenerationDiagnostic, error) {
		d.Stage, d.Reason = stage, cause.Error()
		if err := workCtx.Err(); err != nil {
			return nil, d, err
		}
		images, err := software(workCtx)
		return images, d, err
	}
	release, err := g.generationBudget().Acquire(workCtx, generationbudget.CPU)
	if err != nil {
		return nil, d, err
	}
	source, err := g.Probe.IntelSource(workCtx, input)
	release()
	if err != nil {
		return fallback("metadata", err)
	}
	if err := intelSpriteEligibility(source); err != nil {
		return fallback("eligibility", err)
	}
	plan, err := ffmpeg.NewIntelGenerationPlan(*g.IntelSprites, source, input, times[0], spriteScreenshotWidth, true)
	if err != nil {
		return fallback("plan", err)
	}
	// BMP is a CPU image encoder. A hardware H.264 encode probe is unrelated.
	probes := plan.Probes[:0]
	for _, probe := range plan.Probes {
		if probe.Stage != "encode" {
			probes = append(probes, probe)
		}
	}
	plan.Probes = probes
	height := int(math.Round(float64(source.Height)*spriteScreenshotWidth/float64(source.Width)/2)) * 2
	if height < 2 {
		height = 2
	}
	d, err = ffmpeg.RunIntelGenerationWork(workCtx, plan,
		func(ctx context.Context) error {
			var captureErr error
			images, captureErr = captureSpriteSequence(ctx, times, spriteScreenshotWidth, height, func(ctx context.Context, at float64) (image.Image, error) {
				tileCtx := g.LockManager.ReadLock(ctx, input)
				defer tileCtx.Cancel()
				return g.generateImage(tileCtx, transcoder.IntelSpriteScreenshot(input, at, plan))
			})
			return captureErr
		},
		func(ctx context.Context) error {
			var captureErr error
			images, captureErr = software(ctx)
			return captureErr
		},
		func(ctx context.Context, args ffmpeg.Args) error {
			return g.generate(&fsutil.LockContext{Context: ctx}, args)
		})
	return images, d, err
}

func intelSpriteEligibility(source ffmpeg.IntelSource) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if source.SampleAspectRatio != "1:1" && source.SampleAspectRatio != "1/1" && source.SampleAspectRatio != "1" {
		return fmt.Errorf("sprite sample aspect ratio %q is not validated", source.SampleAspectRatio)
	}
	frameRate, ok := new(big.Rat).SetString(source.FrameRate)
	averageRate, avgOK := new(big.Rat).SetString(source.AverageFrameRate)
	if !ok || !avgOK || frameRate.Sign() <= 0 || frameRate.Cmp(averageRate) != 0 {
		return fmt.Errorf("variable or unknown frame rate requires canonical software sprites")
	}
	duration, err := strconv.ParseFloat(source.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 5 {
		return fmt.Errorf("short or unknown-duration input requires canonical software sprites")
	}
	return nil
}

// captureSpriteSequence preserves order and rejects partial or inconsistent
// hardware results before a sheet is composed or a VTT is published.
func captureSpriteSequence(ctx context.Context, times []float64, width, height int, capture func(context.Context, float64) (image.Image, error)) ([]image.Image, error) {
	images := make([]image.Image, 0, len(times))
	for i, at := range times {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		img, err := capture(ctx, at)
		if err != nil {
			return nil, fmt.Errorf("sprite screenshot at index %d: %w", i, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if img == nil {
			return nil, fmt.Errorf("sprite screenshot at index %d is empty", i)
		}
		size := img.Bounds().Size()
		if i == 0 && height == 0 {
			height = size.Y
		}
		if size.X != width || size.Y != height || height <= 0 {
			return nil, fmt.Errorf("sprite screenshot at index %d has unexpected dimensions %v (want %dx%d)", i, size, width, height)
		}
		images = append(images, img)
	}
	return images, nil
}
