package generate

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/logger"
)

// A resident sheet's input count cannot change after FFmpeg starts. Auto instead
// exercises a small representative subset with the identical GPU filter/grid
// and encoder, then renders all canonical seeks once at the best tested count.
// This is bounded to six candidates/ten seconds, independent of driver capacity.
func (g Generator) tuneSprite(ctx context.Context, lockCtx *fsutil.LockContext, budget *generationbudget.Budget, w generationbudget.Workload, plan ffmpeg.IntelGenerationPlan, times []float64, columns, rows, demand int, dir string, render func(context.Context, int, []float64, string) error) error {
	trialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer func() { cancel() }()
	if budget.HasCanonicalSample(w, len(times)) {
		return nil
	}
	// The first sample must leave room inside its projected quarter-sheet
	// allowance. Small sheets and a seed requiring more than a fifth of the
	// sheet's tiles use ordinary strict rendering instead of paying for a trial
	// that already consumes the forecast budget before it can be installed.
	if len(times)/5 < 4 || min(demand, budget.PrepareWorkload(w)) > len(times)/5 {
		return nil
	}
	started := time.Now()
	budget.StartTuning(w)
	defer budget.FinishTuning(w)
	validated := false
	pressureRetry := false
	sampleTiles := 0
	for trial := 0; trial < 6; trial++ {
		requested := min(demand, budget.PrepareWorkload(w))
		n := min(len(times), max(4, requested, sampleTiles))
		if sampleTiles > 0 && n > sampleTiles {
			// A larger subset amortizes fixed startup/grid/encoder costs. Rerun
			// the best tested lane count on that same subset before comparing a
			// larger candidate. This baseline counts against all trial bounds.
			budget.FinishTuning(w)
			budget.StartTuning(w)
		}
		sampleTiles = n
		subset := make([]float64, n)
		for i := range subset {
			subset[i] = times[i*(len(times)-1)/(n-1)]
		}
		file, err := os.CreateTemp(dir, ".auto-sprite-*.jpg")
		if err != nil {
			return err
		}
		path := file.Name()
		if err := file.Close(); err != nil {
			os.Remove(path)
			return err
		}
		lanes := 0
		sample, renderErr := budget.Measure(trialCtx, w, func(sampleCtx context.Context) error {
			granted, release, err := budget.AcquireWorkload(sampleCtx, w, min(demand, n))
			if err != nil {
				return err
			}
			defer release()
			lanes = granted
			return render(sampleCtx, lanes, subset, path)
		})
		if renderErr == nil {
			renderErr = validateSpriteTrial(path, plan, columns, rows)
		}
		os.Remove(path)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(renderErr, context.DeadlineExceeded) || trialCtx.Err() != nil {
			logger.Infof("[generator] Auto sprite tuning deadline; using completed trials")
			break // Timed-out child is already drained; cancellation is not evidence.
		}
		renderErr = ffmpeg.GenerationPressure(renderErr)
		grow := budget.RecordSample(w, sample, lanes, n, demand, renderErr)
		logger.Infof("[generator] Auto sprite trial=%d lanes=%d tiles=%d elapsed=%s rate=%.3f memory_peak=%d gpu_peak=%d memory_known=%t gpu_known=%t isolated=%t next=%d error=%v", trial+1, lanes, n, sample.Elapsed, float64(n)/sample.Elapsed.Seconds(), sample.MemoryPeak, sample.GPUPeak, sample.MemoryKnown, sample.GPUKnown, sample.Isolated, budget.Settings().MaxGPUProcesses, renderErr)
		if renderErr != nil {
			if generationbudget.IsPressure(renderErr) && !pressureRetry && lanes > 1 {
				pressureRetry = true
				// The failed child has drained. One explicitly classified allocation
				// failure can retry at the reduced count, just as the final sheet does.
				continue
			}
			if !validated {
				// Partial-sheet calibration is optional, not a new capability gate.
				// A driver may reject this shape yet support the complete canonical
				// grid. Keep the provisional count and let that strict render decide.
				break
			}
			// Speculative work is discarded; unknown errors are not pressure. Only
			// the earlier validated same-file GPU count may feed the final render,
			// whose complete output/correctness/error checks are unchanged.
			if budget.RefineCapacity(w, lanes) {
				continue
			}
			break
		}
		if !validated {
			// Calibration may consume at most a quarter of this baseline's
			// projected full-sheet time, and never more than ten seconds.
			// Include setup, validation and cleanup of the initial sample, too.
			allowance := min(10*time.Second, time.Since(started)*time.Duration(len(times))/(4*time.Duration(n)))
			cancel()
			trialCtx, cancel = context.WithDeadline(ctx, started.Add(allowance))
		}
		validated = true
		if !grow {
			if budget.RefineCapacity(w, lanes) {
				continue
			}
			break
		}
	}
	return nil
}

func validateSpriteTrial(path string, plan ffmpeg.IntelGenerationPlan, columns, rows int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	config, format, err := image.DecodeConfig(f)
	if err != nil {
		return err
	}
	tileWidth, tileHeight := 0, 0
	// The production plan's final concrete scale is authoritative, including VR.
	for _, filter := range strings.Split(plan.Filter, ",") {
		if strings.HasPrefix(filter, "scale_vaapi=w=") {
			w, h := 0, 0
			_, _ = fmt.Sscanf(filter, "scale_vaapi=w=%d:h=%d", &w, &h)
			if w > 0 && h > 0 {
				tileWidth, tileHeight = w, h
			}
		}
	}
	if format != "jpeg" || tileWidth < 1 || tileHeight < 1 || config.Width != tileWidth*columns || config.Height != tileHeight*rows {
		return fmt.Errorf("invalid Auto GPU sprite trial output %dx%d %s", config.Width, config.Height, format)
	}
	return nil
}
