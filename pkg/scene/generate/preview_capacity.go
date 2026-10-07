package generate

import (
	"context"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/logger"
)

// Cold plans measure one small group, then feed the remaining canonical chunks
// through the ordinary rolling pool. Warm plans use one steady complete pass;
// only equal complete windows select their subsequent trial count.
func (g Generator) runAdaptivePreviewChunks(ctx context.Context, chunks []previewChunkOptions, run func(context.Context, int) error) error {
	b, w, state := g.generationBudget(), *g.capacityWorkload, g.capacityObservation
	cold := !b.HasMeasuredCapacity(w) && !b.HasCanonicalSample(w, len(chunks))
	state.mu.Lock()
	state.active, state.lanes, state.units, state.elapsed, state.err = 0, 0, 0, 0, nil
	state.canonicalUnits, state.resourceSample, state.started, state.coldPreview = 0, generationbudget.Sample{}, time.Now(), cold
	state.mu.Unlock()
	validatedRun := func(ctx context.Context, i int) error {
		if err := run(ctx, i); err != nil {
			return err
		}
		return g.validateIntelMarkerOutput(ctx, chunks[i].OutputPath)
	}
	measure := func(first, count, workers int) (generationbudget.Sample, error) {
		return b.Measure(ctx, w, func(ctx context.Context) error {
			return runPreviewChunks(ctx, count, workers, func(ctx context.Context, i int) error { return validatedRun(ctx, first+i) })
		})
	}
	first := 0
	var sample generationbudget.Sample
	if cold {
		b.StartTuning(w)
		workers := min(len(chunks), b.PrepareWorkload(w))
		measured, err := measure(0, workers, workers)
		if err != nil {
			return ffmpeg.GenerationPressure(err)
		}
		state.mu.Lock()
		lanes := state.lanes
		state.mu.Unlock()
		b.RecordSample(w, measured, lanes, workers, len(chunks), nil)
		first, sample = workers, measured
	} else {
		// The ordinary preview error policy remains strict: no speculative retry
		// or software fallback. Live leaf admission still bounds every child.
		b.PrepareCanonicalTrial(w, len(chunks), len(chunks))
	}
	if first < len(chunks) {
		workers := min(len(chunks)-first, b.PrepareWorkload(w))
		measured, err := measure(first, len(chunks)-first, workers)
		if err != nil {
			return ffmpeg.GenerationPressure(err)
		}
		if first == 0 {
			sample = measured
		} else {
			// Groups do not overlap. Retain their allocation high-water bounds and
			// require both groups to expose a dimension before replacing its cost.
			sample.MemoryPeak, sample.GPUPeak = max(sample.MemoryPeak, measured.MemoryPeak), max(sample.GPUPeak, measured.GPUPeak)
			sample.MemoryProcessPeak, sample.GPUProcessPeak = max(sample.MemoryProcessPeak, measured.MemoryProcessPeak), max(sample.GPUProcessPeak, measured.GPUProcessPeak)
			sample.MemoryKnown, sample.GPUKnown = sample.MemoryKnown && measured.MemoryKnown, sample.GPUKnown && measured.GPUKnown
			sample.Isolated, sample.Live = sample.Isolated && measured.Isolated, sample.Live || measured.Live
			sample.Processes += measured.Processes
		}
	}
	state.mu.Lock()
	state.canonicalUnits, state.resourceSample = len(chunks), sample
	lanes := state.lanes
	state.mu.Unlock()
	logger.Infof("[generator] Auto preview rolling chunks=%d lanes=%d cold=%t elapsed=%s memory_peak=%d gpu_peak=%d memory_known=%t gpu_known=%t isolated=%t", len(chunks), lanes, cold, time.Since(state.started), sample.MemoryPeak, sample.GPUPeak, sample.MemoryKnown, sample.GPUKnown, sample.Isolated)
	return nil
}
