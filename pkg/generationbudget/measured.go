package generationbudget

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sync"
	"time"
)

// Sample measures a real render, not a capability probe. Owned process/client
// allocation high-water marks exclude unrelated host work; budget contention invalidates throughput
// calibration. Unknown counters remain unknown instead of becoming free memory.
type Sample struct {
	Elapsed                               time.Duration
	MemoryPeak, GPUPeak                   int64
	MemoryProcessPeak, GPUProcessPeak     int64
	Processes                             int
	MemoryKnown, GPUKnown, Isolated, Live bool
}

// ScopeWorkload lets an unidentified runtime adjust within this generation only.
// Its observations disappear after all leaf children have drained. Identified
// workloads retain process-local evidence under the caller's file/plan key.
func (b *Budget) ScopeWorkload(w Workload) (Workload, func(bool)) {
	if !b.AutoGPU() {
		return w, func(bool) {}
	}
	base, identified := w.Key, !w.RuntimeUnidentified
	w.Key += fmt.Sprintf("/generation-%d", b.sampleSequence.Add(1))
	w.RuntimeUnidentified = false
	b.mu.Lock()
	revision := b.learningRevisions[base]
	if identified {
		if previous := b.learning[base]; previous != nil {
			copy := *previous
			b.learning[w.Key] = &copy
		}
	}
	b.mu.Unlock()
	return w, func(validated bool) {
		b.mu.Lock()
		defer b.mu.Unlock()
		// A completed overlapping generation may have published or invalidated
		// the base since this scope copied it. Stale cleanup only drops its own
		// controller; revisions also distinguish absent -> published -> absent.
		if identified && b.learningRevisions[base] == revision {
			if learned := b.learning[w.Key]; validated && learned != nil && learned.measured {
				copy := *learned
				b.learning[base] = &copy
				b.learningRevisions[base]++
			} else if !validated {
				delete(b.learning, base)
				b.learningRevisions[base]++
			}
		}
		delete(b.learning, w.Key)
	}
}

// Measure samples while the render owns its ordinary leaf permits. It acquires
// no permits itself, and waits for the sampler before returning or cancellation.
func (b *Budget) Measure(ctx context.Context, w Workload, run func(context.Context) error) (Sample, error) {
	if !b.AutoGPU() {
		start := time.Now()
		err := run(ctx)
		return Sample{Elapsed: time.Since(start)}, err
	}
	observer := &processObservation{budget: b, pids: map[int]int64{}, wake: make(chan struct{}, 1)}
	ctx = context.WithValue(ctx, processObservationKey{}, observer)
	s := Sample{Isolated: true}
	peaks := map[int]ProcessResources{}
	read := func() {
		observer.mu.Lock()
		pids := map[int]int64{}
		for pid, rss := range observer.pids {
			pids[pid] = rss
		}
		observer.mu.Unlock()
		memory, gpu := int64(0), int64(0)
		s.MemoryKnown, s.GPUKnown = len(pids) > 0, len(pids) > 0
		for pid, rss := range pids {
			r := b.processResources(pid)
			previous, exists := peaks[pid]
			if !exists {
				previous = ProcessResources{Memory: -1, GPU: -1}
			}
			previous.Memory, previous.GPU = max(previous.Memory, r.Memory, rss), max(previous.GPU, r.GPU)
			peaks[pid] = previous
			if previous.Memory < 0 {
				s.MemoryKnown = false
			} else {
				s.MemoryProcessPeak = max(s.MemoryProcessPeak, previous.Memory)
				if rss < 0 {
					memory = saturatingAdd(memory, previous.Memory)
				}
			}
			if previous.GPU < 0 {
				s.GPUKnown = false
			} else {
				s.GPUProcessPeak = max(s.GPUProcessPeak, previous.GPU)
				if rss < 0 {
					gpu = saturatingAdd(gpu, previous.GPU)
				}
			}
		}
		// Completed children retain their individual peaks, including wait4 RSS,
		// but their allocations never accumulate across the rolling queue.
		s.MemoryPeak, s.GPUPeak = max(s.MemoryPeak, memory, s.MemoryProcessPeak), max(s.GPUPeak, gpu, s.GPUProcessPeak)
		if s.GPUPeak == 0 {
			s.GPUKnown = false
		} // A zero snapshot cannot calibrate an unobserved allocation peak.
		s.Live = len(pids) > 0
		s.Processes = len(pids)
		b.mu.Lock()
		observer.mu.Lock()
		running := len(observer.pids) == 0
		for _, rss := range observer.pids {
			if rss < 0 {
				running = true
			}
		}
		observer.mu.Unlock()
		if running && b.active != observer.active {
			s.Isolated = false
		}
		b.mu.Unlock()
	}
	read()
	done := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				read()
			case <-observer.wake:
				read()
			case <-done:
				read()
				return
			}
		}
	}()
	start := time.Now()
	err := run(ctx)
	close(done)
	sampler.Wait()
	s.Elapsed = time.Since(start)
	return s, err
}

type processObservationKey struct{}
type processObservation struct {
	budget *Budget
	// Admission updates active under budget.mu. Packet checks belonging to this
	// same rolling preview are owned work; other stages still contaminate it.
	active int
	mu     sync.Mutex
	pids   map[int]int64
	wake   chan struct{}
}

// Register only commands owned by this render. The final wait4 high-water RSS
// covers short-lived allocation peaks that a periodic /proc sampler can miss.
func RecordProcess(ctx context.Context, pid int) {
	if observer, ok := ctx.Value(processObservationKey{}).(*processObservation); ok {
		observer.mu.Lock()
		observer.pids[pid] = -1
		observer.mu.Unlock()
		select {
		case observer.wake <- struct{}{}:
		default:
		}
	}
}
func RecordProcessResult(ctx context.Context, pid int, state *os.ProcessState) {
	observer, ok := ctx.Value(processObservationKey{}).(*processObservation)
	if !ok || state == nil {
		return
	}
	usage := reflect.ValueOf(state.SysUsage())
	if usage.Kind() == reflect.Pointer && !usage.IsNil() {
		usage = usage.Elem()
	}
	if usage.Kind() != reflect.Struct {
		return
	}
	field := usage.FieldByName("Maxrss")
	if !field.IsValid() || !field.CanInt() {
		return
	}
	rss := field.Int()
	if runtime.GOOS == "linux" {
		if rss < 0 || rss > int64(^uint64(0)>>1)/1024 {
			return
		}
		rss *= 1024
	} else if runtime.GOOS != "darwin" {
		return
	}
	observer.mu.Lock()
	observer.pids[pid] = max(observer.pids[pid], rss)
	observer.mu.Unlock()
}

// StartTuning compares batches within this generation. A prior file-local best
// is a starting point, never a throughput reference for different source seeks.
func (b *Budget) StartTuning(w Workload) {
	if !b.AutoGPU() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selectCapacity(w)
	if c := b.learning[w.Key]; c != nil {
		c.bestRate = 0
		c.comparisonUnits, c.nextLimit = 0, 0
		c.unfinished = true
	}
}

// HasCanonicalSample distinguishes a complete-output comparison from optional
// partial calibration. Later generations can tune using the full work they must
// render anyway, without repeatedly paying for expanded partial baselines.
func (b *Budget) HasCanonicalSample(w Workload, units int) bool {
	if !b.AutoGPU() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.learning[w.Key]
	return c != nil && c.measured && c.bestRate > 0 && c.comparisonUnits == units
}

// PrepareCanonicalTrial selects a pending count for the actual requested output.
// The returned previous count is a strict-GPU retry target; the deadline bounds
// a speculative attempt to 125% of its validated equal-work baseline's duration.
func (b *Budget) PrepareCanonicalTrial(w Workload, units, demand int) (int, time.Duration) {
	if !b.CanTuneGPU() {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selectCapacity(w)
	if c := b.learning[w.Key]; c != nil {
		if c.unfinished && c.measured && c.bestRate > 0 && c.comparisonUnits == units && min(c.nextLimit, c.ceiling, demand) > c.bestLimit {
			c.limit = min(c.nextLimit, c.ceiling, demand)
			b.updateCapacity(c.limit)
			return c.bestLimit, time.Duration(saturatingAdd(int64(c.bestElapsed), int64(c.bestElapsed/4)))
		}
	}
	return 0, 0
}

// RecordCanonicalSample starts an equal-work baseline when moving from partial
// trials to the complete output. Only callers that validated and published the
// canonical output may record it. Subsequent complete outputs use that baseline.
func (b *Budget) RecordCanonicalSample(w Workload, sample Sample, lanes, units, demand int) bool {
	if !b.AutoGPU() || !sample.Isolated {
		return false
	}
	b.mu.Lock()
	if c := b.learning[w.Key]; c != nil && c.comparisonUnits != units {
		c.bestRate, c.bestElapsed = 0, 0
		c.comparisonUnits, c.nextLimit = 0, 0
		c.unfinished = true
	}
	b.mu.Unlock()
	return b.RecordSample(w, sample, lanes, units, demand, nil)
}

// RetainTestedCapacity keeps a validated cold plan's observed count and costs,
// without treating a changing-concurrency pass as a steady throughput reference.
// Its next complete pass at that count establishes the equal-work baseline.
func (b *Budget) RetainTestedCapacity(w Workload, sample Sample, lanes, units int) {
	if !b.AutoGPU() || !sample.Isolated {
		return
	}
	b.StartTuning(w)
	b.RecordCanonicalSample(w, sample, lanes, units, lanes)
	b.FinishTuning(w)
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.learning[w.Key]; c != nil {
		c.bestRate, c.bestElapsed = 0, 0
		c.comparisonUnits, c.nextLimit = 0, 0
		c.unfinished = false
	}
}

// HasMeasuredCapacity skips calibration only after exploration actually
// settles. Exhausting the per-generation trial/time allowance is not a ceiling.
func (b *Budget) HasMeasuredCapacity(w Workload) bool {
	if !b.AutoGPU() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.learning[w.Key]
	return c != nil && c.measured && c.bestLimit > 0 && !c.unfinished
}

// RefineCapacity tries between the faster validated count and a rejected
// speculative count. Rejection is not automatically an allocation diagnosis.
func (b *Budget) RefineCapacity(w Workload, rejected int) bool {
	if !b.AutoGPU() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.learning[w.Key]
	if c == nil || c.bestLimit < 1 || rejected-c.bestLimit < 2 {
		return false
	}
	c.limit = min(c.ceiling, c.bestLimit+(rejected-c.bestLimit)/2)
	c.unfinished = c.limit > c.bestLimit
	b.updateCapacity(c.limit)
	b.dispatch()
	return c.limit > c.bestLimit
}

// RecordSample replaces the provisional surface-pool estimate with observed
// high-water costs (25% allocation reserve, minimum16MiB per observed lane).
// Trials grow geometrically only after validated, saturated, faster rendering.
// The available work and positive manual total bound exploration; resource
// headroom and explicit allocation pressure still bound every leaf admission.
func (b *Budget) RecordSample(w Workload, sample Sample, lanes, units, demand int, err error) bool {
	if !b.AutoGPU() || w.Key == "" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.learning[w.Key]
	if c == nil {
		return false
	}
	if err != nil {
		c.nextLimit = 0
		if IsPressure(err) {
			c.limit = max(1, lanes/2)
			c.invalidatePressureBest()
		} else if c.bestLimit > 0 {
			// A rejected speculative trial is not classified as resource pressure.
			// The final complete render must still validate and retain its own errors.
			c.limit = c.bestLimit
		}
		b.updateCapacity(c.limit)
		b.dispatch()
		return false
	}
	if lanes < 1 || units < 1 || demand < 1 || sample.Elapsed <= 0 {
		return false
	}
	if sample.Isolated && (sample.Live || sample.MemoryPeak > 0 || sample.GPUPeak > 0) && (sample.MemoryKnown || sample.GPUKnown) {
		cost := func(peak, processPeak int64) int64 {
			perLane := peak / int64(lanes)
			if sample.Processes > 1 {
				// Each preview child owns one lane. A short child may expose only its
				// final RSS peak; dividing that by the pool size would undercharge it.
				perLane = max(perLane, processPeak)
			}
			return max(int64(16<<20), saturatingAdd(perLane, perLane/4))
		}
		// Counter availability is independent for host and device allocations.
		// A missing counter cannot erase either the provisional reservation or
		// an earlier measured cost, including single-process sprite trials whose
		// observed per-lane costs may otherwise amortize at higher lane counts.
		memory, gpu := w.MemoryPerSlot, w.GPUPerSlot
		if c.measured {
			memory, gpu = c.memoryCost, c.gpuCost
		}
		if sample.MemoryKnown {
			memory = cost(sample.MemoryPeak, sample.MemoryProcessPeak)
		}
		if sample.GPUKnown {
			gpu = cost(sample.GPUPeak, sample.GPUProcessPeak)
		}
		if c.measured && !(sample.Processes == 1 && units > 1) {
			memory = max(memory, c.memoryCost)
			gpu = max(gpu, c.gpuCost)
		}
		c.measured, c.memoryCost, c.gpuCost = true, memory, gpu
		c.demand = max(c.demand, demand)
	}
	if !sample.Isolated {
		// Contention cannot replace a same-subset comparison that a later
		// generation may resume, or establish that exploration has settled.
		c.unfinished = true
		b.updateCapacity(c.limit)
		return false
	}
	if demand < c.bestLimit {
		// A recovered render's reduced demand excludes the previous best.
		// Its successful lower count establishes a fresh reference; comparing
		// its throughput to the invalidated higher count would undo backoff.
		c.bestRate = 0
	}
	rate := float64(units) / sample.Elapsed.Seconds()
	if c.bestRate > 0 && rate <= c.bestRate*1.05 {
		c.limit = c.bestLimit
		c.unfinished, c.nextLimit = false, 0
		if sample.Processes == 1 && units > 1 {
			c.memoryCost, c.gpuCost = c.bestMemoryCost, c.bestGPUCost
		}
		b.updateCapacity(c.limit)
		b.dispatch()
		return false
	}
	c.bestRate, c.bestLimit = rate, lanes
	c.bestElapsed = sample.Elapsed
	c.comparisonUnits, c.nextLimit = units, 0
	c.bestMemoryCost, c.bestGPUCost = c.memoryCost, c.gpuCost
	// Contention or unknown/contaminated measurements cannot justify growth.
	if !c.measured || lanes < c.limit {
		b.updateCapacity(c.limit)
		return false
	}
	b.selectCapacity(w)
	next := lanes
	if lanes < demand {
		next += min(lanes, demand-lanes)
	}
	c.limit = min(c.ceiling, demand, next)
	c.unfinished = c.limit > lanes
	b.updateCapacity(c.limit)
	b.dispatch()
	return c.limit > lanes
}

// FinishTuning uses the fastest validated trial, not the unexercised next trial.
func (b *Budget) FinishTuning(w Workload) int {
	if !b.AutoGPU() {
		return b.Settings().MaxGPUProcesses
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.learning[w.Key]; c != nil && c.bestLimit > 0 {
		if c.unfinished && c.limit > c.bestLimit {
			c.nextLimit = c.limit
		}
		c.limit = min(c.ceiling, c.bestLimit)
		c.memoryCost, c.gpuCost = c.bestMemoryCost, c.bestGPUCost
		b.updateCapacity(c.limit)
		b.dispatch()
	}
	return b.settings.MaxGPUProcesses
}
