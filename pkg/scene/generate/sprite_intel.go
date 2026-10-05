package generate

import (
	"context"
	"fmt"
	"image"
	"math"
	"math/big"
	"strconv"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// IntelSpriteTiles is an opt-in, time-seeking path. It keeps the exact requested
// timestamps. VAAPI downloads before canonical CPU scaling.
// Any failed tile discards the hardware sheet and runs the complete canonical
// software sheet once.
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
		return captureSpriteSequence(ctx, times, g.spriteWorkers(generationbudget.CPU, len(times)), spriteScreenshotWidth, 0, func(ctx context.Context, at float64) (image.Image, error) {
			return g.SpriteScreenshot(ctx, input, at, "")
		})
	}
	if g.IntelSprites == nil || !g.IntelSprites.Enabled() {
		images, err := software(ctx)
		return images, d, err
	}
	g = g.WithIntelGenerationBudget()
	done := make(chan struct{})
	lockCtx := g.LockManager.ReadLockWithCompletion(ctx, input, done)
	defer lockCtx.Cancel()
	defer close(done)
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
	source, err := g.Probe.IntelSpriteSource(workCtx, input, g.IntelSprites.Backend)
	release()
	if err != nil {
		return fallback("metadata", err)
	}
	if err := intelSpriteEligibility(source, g.IntelSprites.Backend); err != nil {
		return fallback("eligibility", err)
	}
	plan, err := ffmpeg.NewIntelSpritePlan(*g.IntelSprites, source, input, times[0], spriteScreenshotWidth)
	if err != nil {
		return fallback("plan", err)
	}
	height := int(math.Round(float64(source.Height)*spriteScreenshotWidth/float64(source.Width)/2)) * 2
	if height < 2 {
		height = 2
	}
	d, err = ffmpeg.RunIntelGenerationWork(workCtx, plan,
		func(ctx context.Context) error {
			var captureErr error
			images, captureErr = captureSpriteSequence(ctx, times, g.spriteWorkers(generationbudget.GPU, len(times)), spriteScreenshotWidth, height, func(ctx context.Context, at float64) (image.Image, error) {
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
			return g.generateWithContext(ctx, lockCtx, args)
		})
	return images, d, err
}

func intelSpriteEligibility(source ffmpeg.IntelSource, backend string) error {
	if err := source.ValidateSprite(backend); err != nil {
		return err
	}
	knownSquare := source.SampleAspectRatio == "1:1" || source.SampleAspectRatio == "1/1" || source.SampleAspectRatio == "1"
	canonicalGeometry := source.UsesCanonicalSpriteScale(backend) && source.HasSquareOrUnspecifiedSampleAspectRatio()
	if !knownSquare && !canonicalGeometry {
		return fmt.Errorf("sprite sample aspect ratio %q (display aspect ratio %q) is not validated", source.SampleAspectRatio, source.DisplayAspectRatio)
	}
	frameRate, ok := new(big.Rat).SetString(source.FrameRate)
	averageRate, avgOK := new(big.Rat).SetString(source.AverageFrameRate)
	if !ok || !avgOK || frameRate.Sign() <= 0 || averageRate.Sign() <= 0 {
		return fmt.Errorf("unknown frame rate requires canonical software sprites")
	}
	// Unequal declarations alone do not establish VFR. This path uses each
	// requested timestamp in an independent accurate input seek, with no FPS
	// resampling or frame-index arithmetic. Keep the prior cadence gate for
	// all existing 8-bit/QSV eligibility pending their own cadence evidence.
	if !(source.PixelFormat == "yuv420p10le" && source.UsesCanonicalSpriteScale(backend)) && frameRate.Cmp(averageRate) != 0 {
		return fmt.Errorf("unequal frame-rate declarations require canonical software sprites outside the Main 10 VAAPI path")
	}
	duration, err := strconv.ParseFloat(source.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 5 {
		return fmt.Errorf("short or unknown-duration input requires canonical software sprites")
	}
	return nil
}
