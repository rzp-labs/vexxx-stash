package generate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// Rendering uses the already-probed plan; metadata/capability checks are never
// learned as throughput samples and never bypassed by a cached capacity value.
func generationFileWorkload(plan ffmpeg.IntelGenerationPlan, operation, input string) generationbudget.Workload {
	w := plan.GenerationWorkload(operation)
	identity := input
	if info, err := os.Stat(input); err == nil {
		identity += fmt.Sprintf("/%d/%d", info.Size(), info.ModTime().UnixNano())
	}
	// Equal geometry does not imply equal reference pools, seek cost or throughput.
	// Never reuse a different file's measurements; do not expose its private path.
	w.Key += fmt.Sprintf("/file-%x", sha256.Sum256([]byte(identity)))
	return w
}

func (g Generator) withGenerationWorkload(plan ffmpeg.IntelGenerationPlan, operation, input string) Generator {
	w := generationFileWorkload(plan, operation, input)
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
	canonicalUnits       int
	resourceSample       generationbudget.Sample
	started              time.Time
	coldPreview          bool
}

func (g Generator) finishCapacityWork(ctx context.Context, err error) {
	if g.capacityObservation == nil || g.generationBudget() == nil {
		return
	}
	sample := g.capacityObservation
	sample.mu.Lock()
	if err != nil && sample.err == nil {
		sample.err = err
	}
	if ctx.Err() != nil {
		sample.err = ctx.Err()
	}
	lanes, elapsed, units, observedErr := sample.lanes, sample.elapsed, sample.units, sample.err
	canonicalUnits, measured, started, cold := sample.canonicalUnits, sample.resourceSample, sample.started, sample.coldPreview
	sample.mu.Unlock()
	b, w := g.generationBudget(), *g.capacityWorkload
	if canonicalUnits > 0 && observedErr == nil {
		measured.Elapsed = time.Since(started) // Include concat/final validation.
		if cold {
			b.RetainTestedCapacity(w, measured, lanes, canonicalUnits)
		} else {
			grew := b.RecordCanonicalSample(w, measured, lanes, canonicalUnits, canonicalUnits)
			if measured.Isolated && !grew {
				b.RefineCapacity(w, lanes)
			}
		}
		b.FinishTuning(w)
	} else {
		b.Observe(w, lanes, elapsed, units, observedErr)
	}
}
