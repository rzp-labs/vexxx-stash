package generationbudget

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRollingMeasurementOwnValidationIsIsolatedButExternalCPUIsNot(t *testing.T) {
	for _, external := range []bool{false, true} {
		read := make(chan struct{})
		var once sync.Once
		b, err := NewWithResources(Settings{MaxProcesses: 4}, func() Resources {
			return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1}
		}, func(int) ProcessResources {
			once.Do(func() { close(read) })
			return ProcessResources{Memory: 32 << 20, GPU: 32 << 20}
		})
		if err != nil {
			t.Fatal(err)
		}
		w := Workload{Key: "preview/window", MemoryPerSlot: 64 << 20, GPUPerSlot: 64 << 20}
		b.PrepareWorkload(w)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var releaseExternal func()
		sample, err := b.Measure(ctx, w, func(ctx context.Context) error {
			_, releaseGPU, err := b.AcquireWorkload(ctx, w, 1)
			if err != nil {
				return err
			}
			defer releaseGPU()
			releaseCPU, err := b.Acquire(ctx, CPU)
			if err != nil {
				return err
			}
			defer releaseCPU()
			if external {
				releaseExternal, err = b.Acquire(context.Background(), CPU)
				if err != nil {
					return err
				}
			}
			RecordProcess(ctx, 123)
			select {
			case <-read:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		cancel()
		if releaseExternal != nil {
			releaseExternal()
		}
		if err != nil || sample.Isolated == external {
			t.Fatal("rolling sample confused owned checks with external contention", external, sample, err)
		}
		b.mu.Lock()
		active := b.active
		b.mu.Unlock()
		if active != 0 {
			t.Fatal("measurement admission accounting leaked", active)
		}
	}
}

func TestRollingMeasurementDoesNotAccumulateCompletedChildren(t *testing.T) {
	var sampled [16]chan struct{}
	var observed [16]sync.Once
	for pid := 1; pid <= 15; pid++ {
		sampled[pid] = make(chan struct{})
	}
	b, err := NewWithResources(Settings{}, func() Resources {
		return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1}
	}, func(pid int) ProcessResources {
		observed[pid].Do(func() { close(sampled[pid]) })
		return ProcessResources{Memory: 64 << 20, GPU: 64 << 20}
	})
	if err != nil {
		t.Fatal(err)
	}
	w := Workload{Key: "preview/fifteen-chunks", MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
	b.PrepareWorkload(w)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := b.Measure(ctx, w, func(ctx context.Context) error {
		observer := ctx.Value(processObservationKey{}).(*processObservation)
		for first := 1; first <= 15; first += 2 {
			last := min(first+1, 15)
			for pid := first; pid <= last; pid++ {
				RecordProcess(ctx, pid)
			}
			for pid := first; pid <= last; pid++ {
				select {
				case <-sampled[pid]:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			observer.mu.Lock()
			for pid := first; pid <= last; pid++ {
				// Same completion marker installed by RecordProcessResult after Wait.
				observer.pids[pid] = 64 << 20
			}
			observer.mu.Unlock()
		}
		return nil
	})
	if err != nil || !s.MemoryKnown || !s.GPUKnown || s.Processes != 15 || s.MemoryPeak > 128<<20 || s.GPUPeak > 128<<20 || s.MemoryProcessPeak != 64<<20 || s.GPUProcessPeak != 64<<20 {
		t.Fatal("completed allocations accumulated across the rolling pool", s, err)
	}
	b.StartTuning(w)
	b.RecordCanonicalSample(w, s, 2, 15, 15)
	if c := b.learning[w.Key]; c.memoryCost != 80<<20 || c.gpuCost != 80<<20 {
		t.Fatal("per-process reservation depends on completed chunk count", c)
	}
}

func TestMeasuredAutoEscapesOversizedEstimateAndRejectsSlowerTrial(t *testing.T) {
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
	w := Workload{Key: "runtime/hevc/file", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30}
	if got := b.PrepareWorkload(w); got != 1 {
		t.Fatal("oversized estimate baseline", got)
	}
	b.StartTuning(w)
	s := Sample{Elapsed: 2 * time.Second, MemoryPeak: 64 << 20, MemoryKnown: true, Isolated: true, Live: true}
	if !b.RecordSample(w, s, 1, 4, 81, nil) {
		t.Fatal("validated measured baseline remained capped by3GiB estimate")
	}
	lanes, release, err := b.AcquireWorkload(context.Background(), w, 81)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if lanes != 2 {
		t.Fatal("same-generation trial did not grow", lanes)
	}
	s.Elapsed, s.MemoryPeak = time.Second, 128<<20
	if !b.RecordSample(w, s, 2, 4, 81, nil) {
		t.Fatal("faster trial did not grow")
	}
	lanes, release, err = b.AcquireWorkload(context.Background(), w, 81)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if lanes != 4 {
		t.Fatal("second measured trial", lanes)
	}
	s.Elapsed, s.MemoryPeak = 3*time.Second, 256<<20
	if b.RecordSample(w, s, 4, 4, 81, nil) {
		t.Fatal("slower trial was accepted as improvement")
	}
	if got := b.FinishTuning(w); got != 2 {
		t.Fatal("fastest actually tested capacity was not selected", got)
	}
}

func TestColdPreviewCountNeedsSteadyWholeWindowBeforeLargerWarmTrial(t *testing.T) {
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
	w := Workload{Key: "runtime/preview/file/window", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30}
	b.StartTuning(w)
	s := Sample{Elapsed: time.Second, MemoryPeak: 64 << 20, GPUPeak: 64 << 20, MemoryKnown: true, GPUKnown: true, Isolated: true, Processes: 1}
	if !b.RecordSample(w, s, 1, 1, 15, nil) {
		t.Fatal("cold first group did not open a measured two-slot trial")
	}
	s.Processes, s.MemoryPeak, s.GPUPeak = 15, 128<<20, 128<<20
	b.RetainTestedCapacity(w, s, 2, 15)
	if !b.HasMeasuredCapacity(w) || b.HasCanonicalSample(w, 15) {
		t.Fatal("changing-concurrency cold window established a steady reference")
	}
	if retry, _ := b.PrepareCanonicalTrial(w, 15, 15); retry != 0 || b.PrepareWorkload(w) != 2 {
		t.Fatal("first warm window speculated before establishing a steady baseline")
	}
	// Only the complete steady first warm pass can arm a subsequent four-slot
	// trial. Partial chunk groups cannot replace this equal-window reference.
	if !b.RecordCanonicalSample(w, s, 2, 15, 15) || b.FinishTuning(w) != 2 || !b.HasCanonicalSample(w, 15) {
		t.Fatal("complete steady window did not retain its tested count and pending trial")
	}
	if retry, _ := b.PrepareCanonicalTrial(w, 15, 15); retry != 2 || b.PrepareWorkload(w) != 4 {
		t.Fatal("next warm window failed to exercise pending capacity")
	}
	s.Elapsed = 2 * time.Second
	if b.RecordCanonicalSample(w, s, 4, 15, 15) || b.FinishTuning(w) != 2 {
		t.Fatal("slower complete window replaced the faster tested count")
	}
}

func TestMeasuredSpriteSharedProcessOverheadDoesNotBecomePerDecoderCeiling(t *testing.T) {
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
	w := Workload{Key: "sprite/file", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30}
	b.StartTuning(w)
	s := Sample{Elapsed: time.Second, MemoryPeak: 1 << 30, MemoryKnown: true, Isolated: true, Live: true, Processes: 1}
	if !b.RecordSample(w, s, 1, 4, 81, nil) || b.Settings().MaxGPUProcesses != 2 {
		t.Fatal("baseline", b.Settings())
	}
	s.Elapsed, s.MemoryPeak = time.Second/2, 1100<<20
	if !b.RecordSample(w, s, 2, 4, 81, nil) || b.Settings().MaxGPUProcesses <= 2 {
		t.Fatal("fixed process allocation was multiplied per decoder", b.Settings())
	}
	// A slower larger trial must restore the cost model of the selected two lanes.
	bestCost := b.learning[w.Key].memoryCost
	s.Elapsed, s.MemoryPeak = 2*time.Second, 1200<<20
	b.RecordSample(w, s, 4, 4, 81, nil)
	if b.FinishTuning(w) != 2 || b.learning[w.Key].memoryCost != bestCost {
		t.Fatal("selected capacity retained rejected trial's cost")
	}
}

func TestMeasuredAutoPreservesManualAndExhaustion(t *testing.T) {
	manual := newAdaptive(Settings{MaxProcesses: 18, MaxGPUProcesses: 18, Threads: 3}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: 0} })
	w := Workload{Key: "file", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30}
	s := Sample{Elapsed: time.Second, MemoryPeak: 64 << 20, MemoryKnown: true, Isolated: true, Live: true}
	manual.StartTuning(w)
	if manual.RecordSample(w, s, 1, 4, 81, nil) || manual.FinishTuning(w) != 18 || manual.Settings().Threads != 3 {
		t.Fatal("manual settings were tuned")
	}
	var gpu atomic.Int64
	gpu.Store(-1)
	b := newAdaptive(Settings{MaxProcesses: 4}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: gpu.Load()} })
	b.StartTuning(w)
	b.RecordSample(w, s, 1, 4, 81, nil)
	gpu.Store(0)
	_, release, err := b.AcquireWorkload(context.Background(), w, 81)
	if release != nil {
		release()
	}
	if !IsPressure(err) {
		t.Fatal("newly exposed exhausted GPU counter bypassed by prior unknown-counter measurement", err)
	}
}

func TestCanonicalRecoveryCannotRestorePressureInvalidatedBest(t *testing.T) {
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
	w := Workload{Key: "identified/full-sheet", MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
	b.StartTuning(w)
	s := Sample{Elapsed: time.Second, MemoryPeak: 1 << 30, MemoryKnown: true, Isolated: true, Live: true, Processes: 1}
	b.RecordCanonicalSample(w, s, 8, 81, 81)
	b.FinishTuning(w)
	// Repeated equal-rate complete output settles at8; its next ordinary
	// render therefore uses the original typed-pressure-only retry policy.
	b.RecordCanonicalSample(w, s, 8, 81, 81)
	b.FinishTuning(w)
	pressure := &PressureError{Err: errors.New("Cannot allocate memory")}
	b.Observe(w, 8, time.Second, 81, pressure)
	if b.Settings().MaxGPUProcesses != 4 {
		t.Fatal("pressure did not reduce the live count", b.Settings())
	}
	s.Elapsed = 2 * time.Second
	b.RecordCanonicalSample(w, s, 4, 81, 4)
	b.FinishTuning(w)
	if b.Settings().MaxGPUProcesses != 4 || b.learning[w.Key].bestLimit != 4 {
		t.Fatal("slower recovered output restored the pressure-invalidated best", b.Settings())
	}
	// Fixed process memory must use the recovered four-lane cost, too.
	lanes, release, err := b.AcquireWorkload(context.Background(), w, 81)
	if err != nil {
		t.Fatal(err)
	}
	if lanes != 4 || b.reservedMemory != 1280<<20 {
		t.Fatalf("recovery lost the observed cost: lanes=%d reserved=%d", lanes, b.reservedMemory)
	}
	release()
}

func TestContendedPressureRecoveryRetainsBackoffAcrossScopeAndAdmission(t *testing.T) {
	for _, measuredError := range []bool{false, true} {
		b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
		base := Workload{Key: "identified/full-sheet", MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
		w, finish := b.ScopeWorkload(base)
		b.StartTuning(w)
		s := Sample{Elapsed: time.Second, MemoryPeak: 1 << 30, MemoryKnown: true, Isolated: true, Live: true, Processes: 1}
		b.RecordCanonicalSample(w, s, 8, 81, 81)
		b.FinishTuning(w)
		b.RecordCanonicalSample(w, s, 8, 81, 81)
		b.FinishTuning(w)
		pressure := &PressureError{Err: errors.New("Cannot allocate memory")}
		if measuredError {
			b.RecordSample(w, s, 8, 81, 81, pressure)
		} else {
			b.Observe(w, 8, time.Second, 81, pressure)
		}
		// Both pressure entry points must discard the excluded reference now,
		// independently of the next sample's eligibility.
		if c := b.learning[w.Key]; c.limit != 4 || c.bestLimit != 0 || c.bestRate != 0 || c.comparisonUnits != 0 || c.nextLimit != 0 {
			t.Fatalf("pressure kept an excluded comparison: measuredError=%v state=%+v", measuredError, c)
		}
		s.Isolated, s.Elapsed = false, 2*time.Second
		if b.RecordCanonicalSample(w, s, 4, 81, 4) {
			t.Fatal("contended recovery taught throughput")
		}
		if b.FinishTuning(w) != 4 {
			t.Fatal("finish restored excluded best")
		}
		finish(true) // Complete recovered output validated despite contention.
		c := b.learning[base.Key]
		if c == nil || c.limit != 4 || c.bestLimit != 0 || c.nextLimit != 0 {
			t.Fatal("scope retention restored excluded best", c)
		}
		next, finishNext := b.ScopeWorkload(base)
		lanes, release, err := b.AcquireWorkload(context.Background(), next, 81)
		if err != nil {
			t.Fatal(err)
		}
		if lanes != 4 || b.reservedMemory != 1280<<20 || b.reservedGPU != 1024<<20 {
			t.Fatalf("next admission lost backoff/envelope: lanes=%d host=%d GPU=%d", lanes, b.reservedMemory, b.reservedGPU)
		}
		release()
		if b.reservedMemory != 0 || b.reservedGPU != 0 {
			t.Fatal("reservation release leaked")
		}
		finishNext(true)
	}
}

func TestPartialMeasuredCountersPreserveAdmissionAndReservations(t *testing.T) {
	for _, hostKnown := range []bool{true, false} {
		name := "GPU-only"
		if hostKnown {
			name = "RSS-only"
		}
		t.Run(name, func(t *testing.T) {
			r := Resources{CPUs: 16, MemoryAvailable: 8 << 30, GPUAvailable: 8 << 30}
			if hostKnown {
				r.GPUAvailable = 512 << 20
			} else {
				r.MemoryAvailable = 512 << 20
			}
			b := newAdaptive(Settings{}, func() Resources { return r })
			w := Workload{Key: name, MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
			b.StartTuning(w)
			s := Sample{Elapsed: time.Second, MemoryPeak: 64 << 20, GPUPeak: 64 << 20, MemoryKnown: hostKnown, GPUKnown: !hostKnown, Isolated: true, Live: true, Processes: 1}
			if !b.RecordSample(w, s, 1, 4, 81, nil) {
				t.Fatal("measured dimension did not permit the second lane")
			}
			// A faster two-lane sprite may amortize the observed dimension, but
			// cannot grow past the missing dimension's conservative envelope.
			s.Elapsed = time.Second / 2
			if b.RecordSample(w, s, 2, 4, 81, nil) {
				t.Fatal("partial sample bypassed positive headroom in missing dimension")
			}
			lanes, release, err := b.AcquireWorkload(context.Background(), w, 81)
			if err != nil {
				t.Fatal(err)
			}
			if lanes != 2 || b.reservedMemory <= 0 || b.reservedGPU <= 0 {
				t.Fatalf("partial sample admission: lanes=%d host=%d GPU=%d", lanes, b.reservedMemory, b.reservedGPU)
			}
			missingCost, missingReserved := b.learning[w.Key].memoryCost, b.reservedMemory
			if hostKnown {
				missingCost, missingReserved = b.learning[w.Key].gpuCost, b.reservedGPU
			}
			if missingCost != 128<<20 || missingReserved != 256<<20 {
				t.Fatalf("missing dimension lost provisional cost: cost=%d reserved=%d", missingCost, missingReserved)
			}
			release()
			if b.reservedMemory != 0 || b.reservedGPU != 0 {
				t.Fatal("partial counter reservation leaked on release")
			}
		})
	}
}

func TestSpriteCounterAvailabilityRegressionRetainsPreviouslyMeasuredCost(t *testing.T) {
	for _, loseHost := range []bool{true, false} {
		b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 16, MemoryAvailable: 8 << 30, GPUAvailable: 8 << 30} })
		w := Workload{Key: "sprite", MemoryPerSlot: 256 << 20, GPUPerSlot: 256 << 20}
		b.StartTuning(w)
		initialLanes := b.Settings().MaxGPUProcesses
		s := Sample{Elapsed: time.Second, MemoryPeak: int64(initialLanes) * 64 << 20, GPUPeak: int64(initialLanes) * 96 << 20, MemoryKnown: true, GPUKnown: true, Isolated: true, Live: true, Processes: 1}
		b.RecordSample(w, s, initialLanes, 16, 81, nil)
		oldHost, oldGPU := b.learning[w.Key].memoryCost, b.learning[w.Key].gpuCost
		lanes := b.Settings().MaxGPUProcesses
		s.Elapsed = time.Second / 2
		s.MemoryKnown, s.GPUKnown = !loseHost, loseHost
		s.MemoryPeak, s.GPUPeak = 32<<20, 32<<20
		b.RecordSample(w, s, lanes, 16, 81, nil)
		c := b.learning[w.Key]
		if loseHost && c.memoryCost != oldHost || !loseHost && c.gpuCost != oldGPU {
			t.Fatalf("counter disappearance erased measured cost: loseHost=%v before=%d/%d after=%d/%d", loseHost, oldHost, oldGPU, c.memoryCost, c.gpuCost)
		}
		if loseHost && c.gpuCost >= oldGPU || !loseHost && c.memoryCost >= oldHost {
			t.Fatal("still-observed shared-process cost no longer amortizes")
		}
		// Finish uses the fastest tested trial and must retain the missing
		// dimension there too, rather than restore a zero best-cost snapshot.
		b.FinishTuning(w)
		if loseHost && c.memoryCost != oldHost || !loseHost && c.gpuCost != oldGPU {
			t.Fatal("finish discarded the retained dimension")
		}
	}
}

func TestMeasuredScopesDoNotShareConcurrentTuningOrUnknownIdentity(t *testing.T) {
	for _, identified := range []bool{false, true} {
		b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
		base := Workload{Key: "file", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30, RuntimeUnidentified: !identified}
		a, finishA := b.ScopeWorkload(base)
		other, finishOther := b.ScopeWorkload(base)
		if a.Key == other.Key {
			t.Fatal("concurrent generations share controller")
		}
		b.StartTuning(a)
		b.StartTuning(other)
		b.RecordSample(a, Sample{Elapsed: time.Second, MemoryKnown: true, Isolated: true, Live: true}, 1, 4, 81, nil)
		finishA(true)
		if b.learning[other.Key] == nil || b.learning[other.Key].measured {
			t.Fatal("another generation's controller changed")
		}
		finishOther(true)
		if identified && b.learning[base.Key] == nil {
			t.Fatal("validated identified file evidence lost")
		}
		if !identified && len(b.learning) != 0 {
			t.Fatal("unknown-runtime evidence escaped its generation")
		}
		failed, finishFailed := b.ScopeWorkload(base)
		b.StartTuning(failed)
		finishFailed(false)
		if len(b.learning) != 0 {
			t.Fatal("failed complete output retained file evidence")
		}
	}
}

func TestMeasuredSampleRequiresLiveOrObservedAllocationAndIgnoresCancellation(t *testing.T) {
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1} })
	w := Workload{Key: "file", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30}
	b.StartTuning(w)
	s := Sample{Elapsed: time.Second, MemoryKnown: true, Isolated: true}
	if b.RecordSample(w, s, 1, 4, 81, nil) || b.learning[w.Key].measured {
		t.Fatal("unsampled fast child taught zero allocation cost")
	}
	s.Live = true
	if b.RecordSample(w, s, 1, 4, 81, context.Canceled) || b.learning[w.Key].measured {
		t.Fatal("cancelled job taught capacity")
	}
	_, err := b.Measure(context.Background(), w, func(context.Context) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
