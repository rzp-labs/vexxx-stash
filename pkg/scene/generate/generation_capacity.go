package generate

import (
	"context"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// Rendering uses the already-probed plan; metadata/capability checks are never
// learned as throughput samples and never bypassed by a cached capacity value.
func (g Generator) withGenerationWorkload(plan ffmpeg.IntelGenerationPlan, operation string) Generator {
	w := plan.GenerationWorkload(operation)
	g.capacityWorkload = &w
	g.capacityObservation = &capacityObservation{}
	if b := g.generationBudget(); b != nil {
		b.PrepareWorkload(w)
	}
	return g
}
func (g Generator) generateCapacityWork(ctx context.Context, lockCtx *fsutil.LockContext, args []string, slots int) error {
	b := g.generationBudget()
	w := *g.capacityWorkload
	lanes, release, err := b.AcquireWorkload(ctx, w, slots)
	if err != nil {
		return err
	}
	sample := g.capacityObservation
	sample.mu.Lock()
	sample.active += lanes
	sample.lanes = max(sample.lanes, sample.active)
	sample.mu.Unlock()
	start := time.Now()
	err = g.generateAdmittedWithContext(ctx, lockCtx, b.FFMpegArgs(args), lanes)
	err = ffmpeg.GenerationPressure(err)
	sample.mu.Lock()
	sample.active -= lanes
	sample.elapsed += time.Since(start)
	sample.units++
	if err != nil && (sample.err == nil || generationbudget.IsPressure(err)) {
		sample.err = err
	}
	sample.mu.Unlock()
	// Drain and remove this child from the observed overlap before releasing
	// admission; a newly dispatched chunk must not count the finished child.
	release()
	return err
}

// Chunk commands contribute one observation only after the complete output
// passes the existing canonical validation. Concurrent chunks share this state.
type capacityObservation struct {
	mu                   sync.Mutex
	elapsed              time.Duration
	units, lanes, active int
	err                  error
}

func (g Generator) finishCapacityWork(ctx context.Context, err error) {
	if g.capacityObservation == nil || g.generationBudget() == nil {
		return
	}
	sample := g.capacityObservation
	sample.mu.Lock()
	defer sample.mu.Unlock()
	if err != nil && sample.err == nil {
		sample.err = err
	}
	if ctx.Err() != nil {
		sample.err = ctx.Err()
	}
	g.generationBudget().Observe(*g.capacityWorkload, sample.lanes, sample.elapsed, sample.units, sample.err)
}
