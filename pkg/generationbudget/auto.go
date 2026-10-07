package generationbudget

import (
	"context"
	"errors"
	"math"
	"time"
)

// Workload describes a capability-checked rendering plan. Key must include the
// device/runtime, source format/dimensions and output filter/encoder operation.
// Costs are estimates used for admission; successful execution proves capacity.
type Workload struct {
	Key                       string
	MemoryPerSlot, GPUPerSlot int64
	// Missing runtime identity permits a resource-based trial but no reuse of
	// rendering evidence across plans whose device/driver identity is unknown.
	RuntimeUnidentified bool
}
type capacity struct {
	limit, ceiling              int
	successes                   int
	measured                    bool
	memoryCost, gpuCost         int64
	bestLimit                   int
	bestRate                    float64
	bestElapsed                 time.Duration
	demand                      int
	bestMemoryCost, bestGPUCost int64
	unfinished                  bool
	nextLimit, comparisonUnits  int
}

// NewForDevice keeps one scheduler for its entire configuration lifetime. Auto
// observations are deliberately in-memory: restart, driver/device or plan changes
// require fresh evidence instead of trusting a stale on-disk concurrency number.
func NewForDevice(s Settings, device string) (*Budget, error) {
	return NewWithResources(s, func() Resources { return DetectResources(device) })
}

// NewWithResources uses a read-only resource probe. It permits deterministic
// admission tests without a physical GPU or changing host/container limits.
func NewWithResources(s Settings, resources func() Resources, processProbe ...func(int) ProcessResources) (*Budget, error) {
	requested, err := s.Normalize()
	if err != nil {
		return nil, err
	}
	if resources == nil {
		return nil, errors.New("generation budget requires a resource probe")
	}
	b := newAdaptive(requested, resources)
	if len(processProbe) > 1 {
		return nil, errors.New("generation budget accepts one process resource probe")
	}
	if len(processProbe) == 1 && processProbe[0] != nil {
		b.processResources = processProbe[0]
	}
	return b, nil
}
func newAdaptive(requested Settings, resources func() Resources) *Budget {
	r := resources()
	effective := requested.Resolve(r)
	cpuLimit := requested.MaxProcesses
	if cpuLimit == 0 {
		cpuLimit = (Settings{}).Resolve(r).MaxProcesses
	}
	return &Budget{settings: effective, requested: requested, cpuLimit: cpuLimit, sharedGPULimit: effective.MaxGPUProcesses, resources: resources, processResources: DetectProcessResources, learning: make(map[string]*capacity), gpuByWorkload: make(map[string]int), memoryLimit: r.MemoryAvailable / 2, gpuMemoryLimit: r.GPUAvailable / 2}
}

func (b *Budget) AutoGPU() bool    { return b != nil && b.requested.MaxGPUProcesses == 0 }
func (b *Budget) CanTuneGPU() bool { return b.AutoGPU() && b.requested.MaxProcesses != 1 }

// AcquireWorkload chooses lanes atomically alongside other admitted work. Only
// this entry point can explore beyond the seed; probes retain ordinary admission.
func (b *Budget) AcquireWorkload(ctx context.Context, w Workload, maxSlots int) (int, func(), error) {
	return b.acquireWorkload(ctx, GPU, maxSlots, true, w)
}

// PrepareWorkload resolves a capability-checked plan before its coordinator
// chooses worker count. It never reserves resources or bypasses leaf admission.
func (b *Budget) PrepareWorkload(w Workload) int {
	if b == nil {
		return 1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.selectCapacity(w)
}

func (b *Budget) selectCapacity(w Workload) int {
	if !b.AutoGPU() || w.Key == "" {
		return b.settings.MaxGPUProcesses
	}
	r := b.resources()
	c := b.learning[w.Key]
	if c != nil && c.measured {
		w.MemoryPerSlot, w.GPUPerSlot = c.memoryCost, c.gpuCost
	}
	ceiling := b.settings.MaxProcesses
	// Automatic total capacity can accommodate GPU lanes beyond CPU process
	// capacity. CPU leaf work retains its own detected ceiling; GPU surface costs
	// and actual rendering evidence determine the GPU envelope.
	if b.requested.MaxProcesses == 0 {
		ceiling = b.cpuLimit
		if c != nil && c.measured {
			ceiling = max(1, c.demand)
		}
	}
	knownCeiling := false
	if r.MemoryAvailable > 0 {
		b.memoryLimit = max(b.memoryLimit, r.MemoryAvailable/2)
	}
	if r.GPUAvailable > 0 {
		b.gpuMemoryLimit = max(b.gpuMemoryLimit, r.GPUAvailable/2)
	}
	if w.MemoryPerSlot > 0 && r.MemoryAvailable >= 0 {
		memoryCeiling := max(1, int(r.MemoryAvailable/2/w.MemoryPerSlot))
		if b.requested.MaxProcesses == 0 {
			ceiling = memoryCeiling
		} else {
			ceiling = min(ceiling, memoryCeiling)
		}
		knownCeiling = true
	}
	if w.GPUPerSlot > 0 && r.GPUAvailable >= 0 {
		gpuCeiling := max(1, int(r.GPUAvailable/2/w.GPUPerSlot))
		if b.requested.MaxProcesses == 0 && !knownCeiling {
			ceiling = gpuCeiling
		} else {
			ceiling = min(ceiling, gpuCeiling)
		}
	}
	if w.RuntimeUnidentified {
		c = nil
	}
	if c == nil {
		// Capability probes have exercised one actual decoder/filter/encoder. The
		// initial trial also consumes detected headroom and estimated surface costs;
		// it is not a claim that VRAM capacity implies safe decoder concurrency.
		c = &capacity{limit: max(1, int(math.Sqrt(float64(ceiling)))), ceiling: ceiling}
		if !w.RuntimeUnidentified {
			b.learning[w.Key] = c
		}
	}
	c.ceiling = ceiling
	c.limit = min(c.limit, ceiling)
	b.updateCapacity(c.limit)
	return b.settings.MaxGPUProcesses
}

// Observe records actual rendering (never a probe or cancelled job). Resource
// pressure reduces the workload's trial capacity. Successful validated renders
// cautiously grow within resource limits.
func (b *Budget) Observe(w Workload, lanes int, elapsed time.Duration, units int, err error) {
	if !b.AutoGPU() || w.Key == "" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if w.RuntimeUnidentified {
		// A later plan must start from detected resources again. This completed
		// trial can still report its actual grant or explicit pressure backoff.
		if lanes > 0 && (err == nil || IsPressure(err)) {
			if IsPressure(err) {
				lanes = max(1, lanes/2)
			}
			b.updateCapacity(lanes)
			b.dispatch()
		}
		return
	}
	c := b.learning[w.Key]
	if c == nil {
		return
	}
	if IsPressure(err) {
		c.limit = max(1, c.limit/2)
		if lanes > 0 {
			c.limit = min(c.limit, max(1, lanes/2))
		}
		c.successes = 0
		c.invalidatePressureBest()
	} else if err == nil && lanes >= c.limit && elapsed > 0 && units > 0 {
		if c.measured {
			return
		} // Measured controllers select by throughput, not job count.
		c.successes++
		if c.successes >= 2 {
			c.limit = min(c.ceiling, c.limit+1)
			c.successes = 0
		}
	} else if err != nil {
		c.successes = 0
	}
	b.updateCapacity(c.limit)
	b.dispatch()
}

// Pressure invalidates any faster reference above the reduced count before a
// recovery sample can be eligible for learning. Keep the old allocation envelope
// conservatively reserved at fewer lanes until owned counters can replace it.
func (c *capacity) invalidatePressureBest() {
	if c.bestLimit > c.limit {
		if c.measured {
			rescale := func(cost int64) int64 {
				total := int64(math.MaxInt64)
				if cost <= math.MaxInt64/int64(c.bestLimit) {
					total = cost * int64(c.bestLimit)
				}
				perLane := total / int64(c.limit)
				if total%int64(c.limit) != 0 {
					perLane++
				}
				return max(cost, perLane)
			}
			c.memoryCost = rescale(max(c.memoryCost, c.bestMemoryCost))
			c.gpuCost = rescale(max(c.gpuCost, c.bestGPUCost))
		}
		c.bestLimit, c.bestRate, c.bestElapsed, c.comparisonUnits = 0, 0, 0, 0
	}
	c.nextLimit, c.unfinished = 0, false
}

// PressureError is supplied by the runtime layer only for explicit resource
// exhaustion. Unsupported capabilities, corrupt media and unknown driver errors
// must retain their original failure instead of teaching a false capacity limit.
type PressureError struct{ Err error }

func (e *PressureError) Error() string { return e.Err.Error() }
func (e *PressureError) Unwrap() error { return e.Err }
func IsPressure(err error) bool        { var p *PressureError; return errors.As(err, &p) }

func (b *Budget) memorySlots(w *waiter, slots int) (int, bool) {
	if !b.AutoGPU() || w.workload.MemoryPerSlot <= 0 && w.workload.GPUPerSlot <= 0 {
		return slots, false
	}
	r := b.resources()
	// Costs are estimates, not measured minimum allocations. Once capability
	// probes have passed, let an otherwise idle budget exercise one lane even
	// when the estimate exceeds half-headroom. Its full estimated reservation
	// still prevents overlapping work; an exhausted counter cannot be bypassed.
	if w.class == GPU && (r.MemoryAvailable == 0 || r.GPUAvailable == 0) {
		return 0, false
	}
	exclusiveTrial := slots > 0 && w.class == GPU && w.workload.Key != "" && b.active == 0 && r.MemoryAvailable != 0 && r.GPUAvailable != 0
	// Budget half of observable headroom for generation surface estimates,
	// leaving room for the driver, composition/output and unrelated host work.
	if w.workload.MemoryPerSlot > 0 && r.MemoryAvailable >= 0 {
		slots = min(slots, int(max(int64(0), min(b.memoryLimit-b.reservedMemory, r.MemoryAvailable/2))/w.workload.MemoryPerSlot))
	}
	if w.workload.GPUPerSlot > 0 && r.GPUAvailable >= 0 {
		slots = min(slots, int(max(int64(0), min(b.gpuMemoryLimit-b.reservedGPU, r.GPUAvailable/2))/w.workload.GPUPerSlot))
	}
	// No exposed counter means unknown, not zero capacity. Driver pressure and
	// successful workload observations remain authoritative in that case.
	if slots < 1 && exclusiveTrial {
		return 1, true
	}
	return slots, false
}
func (b *Budget) reserve(w *waiter) {
	if !b.AutoGPU() {
		return
	}
	b.reservedMemory += int64(w.slots) * w.workload.MemoryPerSlot
	b.reservedGPU += int64(w.slots) * w.workload.GPUPerSlot
}
func (b *Budget) unreserve(w *waiter) {
	if !b.AutoGPU() {
		return
	}
	b.reservedMemory -= int64(w.slots) * w.workload.MemoryPerSlot
	b.reservedGPU -= int64(w.slots) * w.workload.GPUPerSlot
}

// Reporting the current workload must not overwrite the shared GPU ceiling.
// Dispatch reads each queued workload's live learned limit under the same lock.
func (b *Budget) updateCapacity(current int) {
	b.settings.MaxGPUProcesses = max(1, current)
	if b.AutoGPU() {
		b.sharedGPULimit = max(1, current)
		for _, c := range b.learning {
			b.sharedGPULimit = max(b.sharedGPULimit, c.limit)
		}
	} else {
		b.sharedGPULimit = b.requested.MaxGPUProcesses
	}
	if b.requested.MaxProcesses == 0 {
		b.settings.MaxProcesses = max(b.cpuLimit, b.sharedGPULimit)
		if b.requested.Threads == 0 {
			b.settings.Threads = max(1, b.resources().CPUs/b.settings.MaxProcesses)
		}
	}
}
