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
	"strings"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/logger"
)

// IntelSpriteTiles is retained for explicitly selected software callers. GPU
// requests must use IntelSpriteSheet: returning image.Image would download raw
// pixels and permit CPU composition/encoding, violating the selected backend.
func (g Generator) IntelSpriteTiles(ctx context.Context, input string, times []float64) ([]image.Image, ffmpeg.IntelGenerationDiagnostic, error) {
	d := ffmpeg.IntelGenerationDiagnostic{Actual: "software"}
	if g.IntelSprites != nil {
		d.Selected = g.IntelSprites.Backend
	}
	if g.IntelSprites != nil && g.IntelSprites.Enabled() {
		d.Actual, d.Stage, d.Reason = "none", "API", "GPU sprites require IntelSpriteSheet; CPU tile export is disabled"
		if g.IntelDiagnostic != nil {
			g.IntelDiagnostic(d)
		}
		return nil, d, fmt.Errorf("%s", d.Reason)
	}
	if len(times) == 0 {
		return nil, d, fmt.Errorf("sprite timestamps are empty")
	}
	for i, at := range times {
		if math.IsNaN(at) || math.IsInf(at, 0) || at < 0 || (i > 0 && at < times[i-1]) {
			return nil, d, fmt.Errorf("invalid sprite timestamp at index %d", i)
		}
	}
	images, err := captureSpriteSequence(ctx, times, g.spriteWorkers(generationbudget.CPU, len(times)), spriteScreenshotWidth, 0, func(ctx context.Context, at float64) (image.Image, error) {
		return g.SpriteScreenshot(ctx, input, at, "")
	})
	return images, d, err
}

// IntelSpriteSheet keeps decoded pixels resident on the GPU through JPEG
// publication. Metadata probes, seek-list I/O and atomic rename remain host
// orchestration. No selected-GPU failure starts a software renderer.
func (g Generator) IntelSpriteSheet(ctx context.Context, input string, times []float64, columns, rows int, output string) (ffmpeg.IntelGenerationDiagnostic, error) {
	return g.intelSpriteSheet(ctx, input, times, nil, columns, rows, output, "")
}

// IntelSpriteSheetProjected retains a stored VR projection before GPU tile
// scaling and composition. Unknown modes fail without exporting CPU pixels.
func (g Generator) IntelSpriteSheetProjected(ctx context.Context, input string, times []float64, columns, rows int, output, vrMode string) (ffmpeg.IntelGenerationDiagnostic, error) {
	return g.intelSpriteSheet(ctx, input, times, nil, columns, rows, output, vrMode)
}

// IntelSpriteSheetFrames preserves the canonical frame indices used for short
// clips. Frame selection is metadata-only and never downloads GPU surfaces.
func (g Generator) IntelSpriteSheetFrames(ctx context.Context, input string, frames []int, columns, rows int, output string) (ffmpeg.IntelGenerationDiagnostic, error) {
	return g.intelSpriteSheet(ctx, input, nil, frames, columns, rows, output, "")
}

func (g Generator) IntelSpriteSheetFramesProjected(ctx context.Context, input string, frames []int, columns, rows int, output, vrMode string) (ffmpeg.IntelGenerationDiagnostic, error) {
	return g.intelSpriteSheet(ctx, input, nil, frames, columns, rows, output, vrMode)
}

func (g Generator) intelSpriteSheet(ctx context.Context, input string, times []float64, frames []int, columns, rows int, output, vrMode string) (d ffmpeg.IntelGenerationDiagnostic, err error) {
	count := len(times)
	if frames != nil {
		count = len(frames)
	}
	d = ffmpeg.IntelGenerationDiagnostic{Actual: "none"}
	if g.IntelSprites != nil {
		d.Selected = g.IntelSprites.Backend
	}
	defer func() {
		if g.IntelDiagnostic != nil {
			g.IntelDiagnostic(d)
		}
	}()
	fail := func(stage string, cause error) (ffmpeg.IntelGenerationDiagnostic, error) {
		d.Stage, d.Reason = stage, cause.Error()
		if ctx.Err() != nil {
			return d, ctx.Err()
		}
		return d, fmt.Errorf("GPU sprite %s: %w", stage, cause)
	}
	if err := ctx.Err(); err != nil {
		return fail("cancellation", err)
	}
	if g.IntelSprites == nil || !g.IntelSprites.Enabled() {
		return fail("backend", fmt.Errorf("GPU sprite backend was not selected"))
	}
	if columns <= 0 || rows <= 0 || count == 0 || count > columns*rows {
		return fail("plan", fmt.Errorf("invalid GPU sprite grid"))
	}
	input, err = filepath.Abs(input)
	if err != nil {
		return fail("plan", err)
	}
	if frames == nil {
		if _, err = ffmpeg.IntelSpriteSeekList(input, ffmpeg.IntelSource{}, times); err != nil {
			return fail("plan", err)
		}
	} else {
		for i, frame := range frames {
			if frame < 0 || (i > 0 && frame < frames[i-1]) {
				return fail("plan", fmt.Errorf("invalid GPU sprite frame at index %d", i))
			}
		}
	}
	g = g.WithIntelGenerationBudget()
	done := make(chan struct{})
	lockCtx := g.LockManager.ReadLockWithCompletion(ctx, input, done)
	defer lockCtx.Cancel()
	defer close(done)
	workCtx, cancel := context.WithCancel(lockCtx)
	defer cancel()
	source, err := g.intelSourceMetadata(workCtx, lockCtx, input, *g.IntelSprites, false)
	if err != nil {
		return fail("metadata", err)
	}
	if err = intelSpriteEligibility(source, g.IntelSprites.Backend); err != nil {
		return fail("eligibility", err)
	}
	start := 0.0
	if frames == nil {
		start = times[0]
	}
	width := spriteWidth(vrMode)
	plan, err := ffmpeg.NewIntelProjectedSpritePlan(*g.IntelSprites, source, input, start, width, vrMode)
	if err != nil {
		return fail("plan", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(output), "."+filepath.Base(output)+"-*.jpg")
	if err != nil {
		return fail("output", err)
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Close(); err != nil {
		return fail("output", err)
	}
	maxLanes := 1
	if frames == nil {
		maxLanes = g.spriteWorkers(generationbudget.GPU, count)
	}
	// Probes finish before this leaf callback. Choose and reserve available
	// decoder capacity atomically, then construct the command for that count.
	// A contended sheet can overlap existing work without holding partial slots
	// or waiting for the entire configured ceiling to become free.
	render := func(ctx context.Context) error {
		budget := g.generationBudget()
		lanes, release, err := budget.AcquireUpTo(ctx, generationbudget.GPU, maxLanes)
		if err != nil {
			return fmt.Errorf("waiting for GPU sprite budget: %w", err)
		}
		defer release()
		var args ffmpeg.Args
		if frames == nil {
			seekLists, counts, cleanup, seekErr := intelSpriteSeekInputs(input, source, times, lanes, filepath.Dir(output))
			if seekErr != nil {
				return seekErr
			}
			defer cleanup()
			args, err = transcoder.IntelSpriteSheetInputs(seekLists, plan, counts, columns, rows, tmp.Name())
		} else {
			args, err = transcoder.IntelSpriteSheetFrames(input, plan, frames, columns, rows, tmp.Name())
		}
		if err != nil {
			return err
		}
		logger.Infof("[generator] GPU sprite decoder lanes=%d ceiling=%d tiles=%d", lanes, maxLanes, count)
		return g.generateAdmittedWithContext(ctx, lockCtx, budget.FFMpegArgs(args))
	}
	runWork := g.intelSpriteWork
	if runWork == nil {
		runWork = ffmpeg.RunIntelGenerationWork
	}
	d, err = runWork(workCtx, plan, render, nil, func(ctx context.Context, args ffmpeg.Args) error { return g.generateWithContext(ctx, lockCtx, args) })
	if err != nil {
		if frames == nil {
			return d, fmt.Errorf("GPU sprite failed without software fallback (a requested frame must occur within its one-second seek interval): %w", err)
		}
		return d, fmt.Errorf("GPU sprite frame render failed without software fallback: %w", err)
	}
	// DecodeConfig parses only the JPEG header; the host never decodes sheet
	// pixels. A complete canonical grid is mandatory before atomic publication.
	release, err := g.generationBudget().Acquire(workCtx, generationbudget.CPU)
	if err != nil {
		return fail("output", err)
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		release()
		return fail("output", err)
	}
	config, format, err := image.DecodeConfig(f)
	_ = f.Close()
	release()
	displayWidth, displayHeight := ffmpeg.IntelDisplayDimensions(plan.Source)
	height := int(math.Round(float64(displayHeight)*float64(width)/float64(displayWidth)/2)) * 2
	if height < 2 {
		height = 2
	}
	if err != nil {
		return fail("output", err)
	}
	if format != "jpeg" || config.Width != width*columns || config.Height != height*rows {
		return fail("output", fmt.Errorf("GPU sprite geometry/format %dx%d %s, expected %dx%d JPEG", config.Width, config.Height, format, width*columns, height*rows))
	}
	if err = workCtx.Err(); err != nil {
		return fail("cancellation", err)
	}
	if err = os.Rename(tmp.Name(), output); err != nil {
		return fail("output", err)
	}
	return d, nil
}

// Contiguous, balanced partitions retain canonical tile order while each input
// owns an independent decoder and accurate seeks. A sheet reserves all lanes
// atomically at its render leaf; capability probes still consume a single slot.
func intelSpriteSeekInputs(input string, source ffmpeg.IntelSource, times []float64, lanes int, dir string) ([]string, []int, func(), error) {
	var paths []string
	cleanup := func() {
		for _, path := range paths {
			_ = os.Remove(path)
		}
	}
	if lanes < 1 || lanes > len(times) {
		return nil, nil, cleanup, fmt.Errorf("invalid GPU sprite decoder lane count")
	}
	counts := make([]int, lanes)
	offset := 0
	for lane := range counts {
		count := len(times) / lanes
		if lane < len(times)%lanes {
			count++
		}
		counts[lane] = count
		data, err := ffmpeg.IntelSpriteSeekList(input, source, times[offset:offset+count])
		if err != nil {
			cleanup()
			return nil, nil, cleanup, err
		}
		f, err := os.CreateTemp(dir, ".sprite-seeks-*.ffconcat")
		if err != nil {
			cleanup()
			return nil, nil, cleanup, err
		}
		paths = append(paths, f.Name())
		_, writeErr := f.WriteString(data)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			cleanup()
			if writeErr != nil {
				return nil, nil, cleanup, writeErr
			}
			return nil, nil, cleanup, closeErr
		}
		offset += count
	}
	return paths, counts, cleanup, nil
}

func intelSpriteEligibility(source ffmpeg.IntelSource, backend string) error {
	if err := source.ValidateSprite(backend); err != nil {
		return err
	}
	if !source.HasSquareOrUnspecifiedSampleAspectRatio() {
		return fmt.Errorf("sprite sample aspect ratio %q (display aspect ratio %q) is not supported by the GPU grid", source.SampleAspectRatio, source.DisplayAspectRatio)
	}
	return nil
}

// IntelSpriteFrameInfo replaces ffprobe -count_frames for short GPU sprites.
// Null output carries wrapped hardware frames, never CPU pixel data. The same
// leaf budget and source lock own metadata and the full GPU decode separately.
func (g Generator) IntelSpriteFrameInfo(ctx context.Context, input string) (*ffmpeg.FrameInfo, error) {
	if g.IntelSprites == nil || !g.IntelSprites.Enabled() {
		return nil, fmt.Errorf("GPU sprite backend was not selected")
	}
	g = g.WithIntelGenerationBudget()
	done := make(chan struct{})
	lockCtx := g.LockManager.ReadLockWithCompletion(ctx, input, done)
	defer lockCtx.Cancel()
	defer close(done)
	source, err := g.intelSourceMetadata(lockCtx, lockCtx, input, *g.IntelSprites, false)
	if err != nil {
		if g.IntelDiagnostic != nil {
			g.IntelDiagnostic(ffmpeg.IntelGenerationDiagnostic{Selected: g.IntelSprites.Backend, Actual: "none", Stage: "metadata", Reason: err.Error()})
		}
		return nil, err
	}
	plan, err := ffmpeg.NewIntelSpritePlan(*g.IntelSprites, source, input, 0, spriteScreenshotWidth)
	if err != nil {
		return nil, err
	}
	if err := ffmpeg.ValidateIntelDevice(plan.Config.Device); err != nil {
		return nil, err
	}
	args := ffmpeg.Args{"-v", "error", "-nostdin", "-abort_on", "empty_output"}
	args = append(args, plan.InputArgs...)
	args = args.Input(input)
	args = append(args, "-map", fmt.Sprintf("0:%d", source.StreamIndex), "-an", "-fps_mode", "passthrough", "-progress", "pipe:1", "-nostats", "-f", "null", "-")
	progress, err := g.generateOutput(lockCtx, args)
	if err != nil {
		return nil, err
	}
	result := &ffmpeg.FrameInfo{}
	var endUS int64
	for _, line := range strings.Split(string(progress), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "frame":
			result.NumberOfFrames, _ = strconv.Atoi(strings.TrimSpace(value))
		case "out_time_us":
			endUS, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		}
	}
	if result.NumberOfFrames <= 0 {
		return nil, fmt.Errorf("GPU frame count returned no decoded frames")
	}
	rate, ok := new(big.Rat).SetString(source.FrameRate)
	if ok && rate.Sign() > 0 {
		result.FrameRate, _ = rate.Float64()
	}
	if result.FrameRate <= 0 && endUS > 0 {
		result.FrameRate = float64(result.NumberOfFrames) * 1e6 / float64(endUS)
	}
	return result, nil
}
