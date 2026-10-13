package generationbudget

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func independentTestBudget(t *testing.T, settings Settings) *Budget {
	t.Helper()
	b, err := NewIndependentAutoWithResources(settings, func() Resources { return Resources{CPUs: 8, MemoryAvailable: 8 << 30, GPUAvailable: -1} })
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func completeSample() Sample {
	return Sample{Elapsed: time.Second, MemoryPeak: 64 << 20, MemoryKnown: true, Isolated: true, Processes: 1}
}
func assertTuning(t *testing.T, b *Budget, kind WorkloadKind, state MeasurementState, value int) {
	t.Helper()
	got := b.WorkloadTuning(kind)
	if got.MeasurementState != state || (value == 0) != (got.AutomaticConcurrency == nil) || value > 0 && *got.AutomaticConcurrency != value {
		t.Fatalf("tuning=%+v value=%d want=%s/%d", got, value, state, value)
	}
}
func TestIndependentAutoSharedModesAndEvidence(t *testing.T) {
	for _, total := range []int{0, 6} {
		for _, gpu := range []int{0, 6} {
			t.Run(fmt.Sprintf("total%d-gpu%d", total, gpu), func(t *testing.T) {
				b := independentTestBudget(t, Settings{MaxProcesses: total, MaxGPUProcesses: gpu})
				sw := Workload{Kind: SpriteWorkload, Key: "sprite", MemoryPerSlot: 64 << 20}
				pw := Workload{Kind: PreviewWorkload, Key: "preview", MemoryPerSlot: 64 << 20}
				assertTuning(t, b, SpriteWorkload, NotMeasured, 0)
				sn, pn := b.PrepareWorkload(sw), b.PrepareWorkload(pw)
				if !b.AutoWorkload(sw) || !b.AutoWorkload(pw) || sn < 1 || pn < 1 || gpu > 0 && (sn >= gpu || pn >= gpu) {
					t.Fatal("shared manual disabled independent Auto")
				}
				b.RetainTestedCapacity(sw, completeSample(), sn, 81)
				b.RetainTestedCapacity(pw, completeSample(), pn, 15)
				assertTuning(t, b, SpriteWorkload, Measured, sn)
				assertTuning(t, b, PreviewWorkload, Measured, pn)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, rs, err := b.AcquireWorkload(ctx, sw, 1)
				if err != nil {
					t.Fatal(err)
				}
				_, rp, err := b.AcquireWorkload(ctx, pw, 1)
				if err != nil {
					rs()
					t.Fatal(err)
				}
				b.mu.Lock()
				exceeded := b.active > b.settings.MaxProcesses || b.gpuActive > b.sharedGPULimit
				b.mu.Unlock()
				if exceeded {
					t.Fatal("simultaneous workloads exceeded shared limits")
				}
				before := b.PrepareWorkload(pw)
				b.Observe(sw, sn, time.Second, 81, &PressureError{Err: errors.New("allocation exhausted")})
				if b.PrepareWorkload(pw) != before {
					t.Fatal("sprite pressure retuned preview")
				}
				assertTuning(t, b, SpriteWorkload, Adapting, 0)
				assertTuning(t, b, PreviewWorkload, Measured, pn)
				rs()
				rs()
				rp()
				rp()
				if b.active != 0 || b.gpuActive != 0 || b.reservedMemory != 0 || b.reservedGPU != 0 || len(b.queue) != 0 {
					t.Fatal("permit cleanup failed")
				}
				if gpu > 0 && b.Settings().MaxGPUProcesses != gpu {
					t.Fatal("manual shared GPU ceiling was rewritten")
				}
			})
		}
	}
}
func TestSnapshotRequiresCompleteValidatedCurrentCount(t *testing.T) {
	b := independentTestBudget(t, Settings{MaxProcesses: 8, MaxGPUProcesses: 8})
	w := Workload{Kind: SpriteWorkload, Key: "sprite", MemoryPerSlot: 64 << 20}
	seed := b.PrepareWorkload(w)
	b.RecordSample(w, completeSample(), seed, 9, 81, nil)
	b.FinishTuning(w)
	assertTuning(t, b, SpriteWorkload, NotMeasured, 0)
	b.StartTuning(w)
	b.RecordCanonicalSample(w, completeSample(), seed, 81, seed)
	b.FinishTuning(w)
	assertTuning(t, b, SpriteWorkload, Measured, seed)
	value := b.WorkloadTuning(SpriteWorkload).AutomaticConcurrency
	*value = 999
	assertTuning(t, b, SpriteWorkload, Measured, seed)
	faster := completeSample()
	faster.Elapsed = time.Second / 2
	b.RecordCanonicalSample(w, faster, seed, 81, 81)
	assertTuning(t, b, SpriteWorkload, Adapting, 0)
	b.FinishTuning(w)
	assertTuning(t, b, SpriteWorkload, Measured, seed)
	contended := completeSample()
	contended.Isolated = false
	b.RecordCanonicalSample(w, contended, seed+1, 81, 81)
	assertTuning(t, b, SpriteWorkload, Measured, seed)
	assertTuning(t, b, PreviewWorkload, NotMeasured, 0)
	b.Observe(w, seed, time.Second, 81, errors.New("unknown VAAPI error"))
	assertTuning(t, b, SpriteWorkload, Measured, seed)
	b.Observe(w, seed, time.Second, 81, context.Canceled)
	assertTuning(t, b, SpriteWorkload, Measured, seed)
}
func TestSnapshotScopeFailureAndStaleCleanup(t *testing.T) {
	b := independentTestBudget(t, Settings{MaxProcesses: 4, MaxGPUProcesses: 4})
	base := Workload{Kind: PreviewWorkload, Key: "preview", MemoryPerSlot: 64 << 20}
	older, oldCleanup := b.ScopeWorkload(base)
	b.PrepareWorkload(older)
	newer, newCleanup := b.ScopeWorkload(base)
	n := b.PrepareWorkload(newer)
	b.RetainTestedCapacity(newer, completeSample(), n, 15)
	newCleanup(true)
	oldCleanup(false)
	assertTuning(t, b, PreviewWorkload, Measured, n)
	failed, cleanup := b.ScopeWorkload(base)
	b.PrepareWorkload(failed)
	cleanup(false)
	assertTuning(t, b, PreviewWorkload, NotMeasured, 0)
	if len(b.learning) != 0 || len(b.queue) != 0 || b.active != 0 {
		t.Fatal("scope cleanup retained state")
	}
}
func TestSnapshotDoesNotProbeAdmitOrLearn(t *testing.T) {
	var calls atomic.Int64
	b, err := NewIndependentAutoWithResources(Settings{}, func() Resources { calls.Add(1); return Resources{CPUs: 8, MemoryAvailable: 8 << 30, GPUAvailable: -1} })
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	for range 20 {
		assertTuning(t, b, SpriteWorkload, NotMeasured, 0)
		assertTuning(t, b, PreviewWorkload, NotMeasured, 0)
	}
	if before != calls.Load() || len(b.learning) != 0 || b.active != 0 || len(b.queue) != 0 {
		t.Fatal("query started probe, learning or admission")
	}
}
func TestIndependentAutoOneSharedSlotStillMeasures(t *testing.T) {
	b := independentTestBudget(t, Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
	for _, kind := range []WorkloadKind{SpriteWorkload, PreviewWorkload} {
		w := Workload{Kind: kind, Key: fmt.Sprint(kind), MemoryPerSlot: 64 << 20}
		if !b.AutoWorkload(w) || b.CanTuneWorkload(w) || b.PrepareWorkload(w) != 1 {
			t.Fatal("single-slot semantics")
		}
		b.RecordCanonicalSample(w, completeSample(), 1, 1, 1)
		b.FinishTuning(w)
		assertTuning(t, b, kind, Measured, 1)
	}
}

func TestTypeSnapshotAggregatesDifferentFilesWithoutReplacingNewerEvidence(t *testing.T) {
	b := independentTestBudget(t, Settings{MaxProcesses: 8, MaxGPUProcesses: 8})
	first := Workload{Kind: SpriteWorkload, Key: "file-A", MemoryPerSlot: 64 << 20}
	b.PrepareWorkload(first)
	b.RetainTestedCapacity(first, completeSample(), 3, 81)
	assertTuning(t, b, SpriteWorkload, Measured, 3)
	// A smaller file/plan's seed or complete render cannot turn the type-wide
	// selection into a last-file value. Its own admission remains plan-specific.
	second := Workload{Kind: SpriteWorkload, Key: "file-B", MemoryPerSlot: 2 << 30}
	n := b.PrepareWorkload(second)
	if n != 1 {
		t.Fatal("fixture expected one-lane plan", n)
	}
	b.RetainTestedCapacity(second, completeSample(), n, 81)
	assertTuning(t, b, SpriteWorkload, Measured, 3)
	if b.PrepareWorkload(second) != 1 {
		t.Fatal("aggregate replaced individual plan")
	}
	failed, cleanup := b.ScopeWorkload(second)
	b.PrepareWorkload(failed)
	cleanup(false)
	assertTuning(t, b, SpriteWorkload, Measured, 3)
	// A new, larger untested plan makes a numeric aggregate unavailable until
	// that current selection has complete-output evidence; it never borrows proof.
	third := Workload{Kind: SpriteWorkload, Key: "file-C", MemoryPerSlot: 16 << 20}
	b.PrepareWorkload(third)
	b.Observe(third, 2, time.Second, 81, nil)
	b.Observe(third, 2, time.Second, 81, nil)
	b.Observe(third, 3, time.Second, 81, nil)
	b.Observe(third, 3, time.Second, 81, nil)
	assertTuning(t, b, SpriteWorkload, Adapting, 0)
	assertTuning(t, b, PreviewWorkload, NotMeasured, 0)
}

func TestValidatedCountSurvivesScopeWithoutKnownAllocationCounters(t *testing.T) {
	b := independentTestBudget(t, Settings{MaxProcesses: 4, MaxGPUProcesses: 4})
	w, cleanup := b.ScopeWorkload(Workload{Kind: SpriteWorkload, Key: "unknown-counters", MemoryPerSlot: 64 << 20})
	n := b.PrepareWorkload(w)
	sample := Sample{Elapsed: time.Second, Isolated: true}
	b.RecordCanonicalSample(w, sample, n, 81, 81)
	b.FinishTuning(w)
	cleanup(true)
	assertTuning(t, b, SpriteWorkload, Measured, n)
	c := b.learning["unknown-counters"]
	if c == nil || c.measured || c.limit != n {
		t.Fatal("complete count invented allocation evidence or growth", c)
	}
}

func TestConcurrentQueriesAndIndependentLearning(t *testing.T) {
	b := independentTestBudget(t, Settings{MaxProcesses: 8, MaxGPUProcesses: 8})
	done := make(chan struct{}, 2)
	for _, kind := range []WorkloadKind{SpriteWorkload, PreviewWorkload} {
		go func(kind WorkloadKind) {
			for i := 0; i < 100; i++ {
				w, cleanup := b.ScopeWorkload(Workload{Kind: kind, Key: fmt.Sprintf("%d/file%d", kind, i%2), MemoryPerSlot: 64 << 20})
				n := b.PrepareWorkload(w)
				b.RetainTestedCapacity(w, completeSample(), n, 81)
				cleanup(true)
			}
			done <- struct{}{}
		}(kind)
	}
	for range 1000 {
		for _, kind := range []WorkloadKind{SpriteWorkload, PreviewWorkload} {
			got := b.WorkloadTuning(kind)
			if got.AutomaticConcurrency != nil {
				if got.MeasurementState != Measured || *got.AutomaticConcurrency < 1 || *got.AutomaticConcurrency > 8 {
					t.Fatal("invalid concurrent snapshot", got)
				}
				*got.AutomaticConcurrency = 1000 // Detached response must not mutate admission.
			} else if got.MeasurementState == Measured {
				t.Fatal("measurement without value")
			}
		}
	}
	<-done
	<-done
	assertTuning(t, b, SpriteWorkload, Measured, 2)
	assertTuning(t, b, PreviewWorkload, Measured, 2)
}

func TestSnapshotIgnoresNonIndependentAndUnknownFamilies(t *testing.T) {
	var b *Budget
	assertTuning(t, b, SpriteWorkload, NotMeasured, 0)
	assertTuning(t, b, OtherWorkload, NotMeasured, 0)
	shared, err := NewWithResources(Settings{MaxProcesses: 2, MaxGPUProcesses: 2}, func() Resources {
		return Resources{CPUs: 4, MemoryAvailable: 4 << 30, GPUAvailable: 4 << 30}
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTuning(t, shared, SpriteWorkload, NotMeasured, 0)
	independent := independentTestBudget(t, Settings{MaxProcesses: 4, MaxGPUProcesses: 4})
	assertTuning(t, independent, OtherWorkload, NotMeasured, 0)
}
