package generationbudget

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveAutoAndMixedRequests(t *testing.T) {
	r := Resources{CPUs: 12, MemoryAvailable: 2 << 30, GPUAvailable: -1}
	for _, tt := range []struct{ request, want Settings }{
		{Settings{}, Settings{8, 1, 1}},
		{Settings{MaxGPUProcesses: 10}, Settings{10, 10, 1}},
		{Settings{MaxProcesses: 3}, Settings{3, 1, 4}},
		{Settings{3, 2, 7}, Settings{3, 2, 7}},
	} {
		if got := tt.request.Resolve(r); got != tt.want {
			t.Fatalf("%+v => %+v want %+v", tt.request, got, tt.want)
		}
	}
}

func TestAutoUsesWorkloadHeadroomAndValidatedRendering(t *testing.T) {
	r := Resources{CPUs: 16, MemoryAvailable: 8 << 30, GPUAvailable: 2 << 30}
	b := newAdaptive(Settings{}, func() Resources { return r })
	small := Workload{Key: "driver/codec/small", MemoryPerSlot: 64 << 20, GPUPerSlot: 64 << 20}
	large := Workload{Key: "driver/codec/8K", MemoryPerSlot: 512 << 20, GPUPerSlot: 512 << 20}
	acquire := func(w Workload) int {
		t.Helper()
		lanes, release, err := b.AcquireWorkload(context.Background(), w, 16)
		if err != nil {
			t.Fatal(err)
		}
		release()
		return lanes
	}
	if got := acquire(small); got != 4 {
		t.Fatalf("detected small workload seed=%d want4", got)
	}
	if got := acquire(large); got != 1 {
		t.Fatalf("large workload ignored memory costs: %d", got)
	}
	for range 2 {
		b.Observe(small, 4, time.Second, 81, nil)
	}
	if got := acquire(small); got != 5 {
		t.Fatalf("validated work did not grow capacity: %d", got)
	}
	b.Observe(small, 5, time.Second, 81, &PressureError{Err: errors.New("explicit OOM")})
	if got := acquire(small); got != 2 {
		t.Fatalf("pressure did not reduce capacity: %d", got)
	}
	for _, err := range []error{errors.New("VAAPI23"), context.Canceled, context.DeadlineExceeded} {
		b.Observe(small, 2, time.Second, 81, err)
		if got := acquire(small); got != 2 {
			t.Fatalf("non-resource failure changed capacity: %d", got)
		}
	}
	// Device/driver/filter changes get independent evidence and restart discards learning.
	changed := small
	changed.Key = "new-driver/codec/small"
	if got := acquire(changed); got != 4 {
		t.Fatalf("new runtime reused old capacity: %d", got)
	}
	fresh := newAdaptive(Settings{}, func() Resources { return r })
	lanes, release, err := fresh.AcquireWorkload(context.Background(), small, 16)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if lanes != 4 {
		t.Fatal("restart retained observations")
	}
}

func TestManualLimitsDoNotLearnOrRejectEstimatedCosts(t *testing.T) {
	b := newAdaptive(Settings{8, 6, 3}, func() Resources { return Resources{CPUs: 2, MemoryAvailable: 0, GPUAvailable: 0} })
	w := Workload{Key: "manual", MemoryPerSlot: 1 << 30, GPUPerSlot: 1 << 30}
	lanes, release, err := b.AcquireWorkload(context.Background(), w, 6)
	if err != nil {
		t.Fatal(err)
	}
	release()
	b.Observe(w, lanes, time.Second, 81, &PressureError{Err: errors.New("OOM")})
	if lanes != 6 || b.Settings() != (Settings{8, 6, 3}) {
		t.Fatal("manual limits were auto tuned")
	}
}

func TestAutoMemoryPressureWaitCancellationAndRelease(t *testing.T) {
	r := Resources{CPUs: 8, MemoryAvailable: 2 << 30, GPUAvailable: -1}
	b := newAdaptive(Settings{}, func() Resources { return r })
	w := Workload{Key: "large", MemoryPerSlot: 1 << 30}
	lanes, release, err := b.AcquireWorkload(context.Background(), w, 8)
	if err != nil || lanes != 1 {
		t.Fatalf("first admission %d %v", lanes, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := b.AcquireWorkload(ctx, w, 8); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait was not cancellable: %v", err)
	}
	release()
	release()
	if b.reservedMemory != 0 || b.active != 0 {
		t.Fatal("cancellation/idempotent release leaked reservations")
	}
	r.MemoryAvailable = 0
	if _, _, err := b.AcquireWorkload(context.Background(), w, 8); !IsPressure(err) {
		t.Fatalf("zero available memory confused with unavailable counter: %v", err)
	}
}

func TestAutoAllowsExclusiveProbedTrialBelowEstimatedHeadroom(t *testing.T) {
	r := Resources{CPUs: 4, MemoryAvailable: 11 << 29, GPUAvailable: -1}
	b := newAdaptive(Settings{}, func() Resources { return r })
	// The physical 8192x4096 ten-bit estimate is 3 GiB; available memory is
	// 5.5 GiB. Passing capability probes does not prove this conservative cost.
	w := Workload{Key: "runtime/8K/Main10", MemoryPerSlot: 8192 * 4096 * 96, GPUPerSlot: 8192 * 4096 * 96}
	lanes, release, err := b.AcquireWorkload(context.Background(), w, 81)
	if err != nil || lanes != 1 {
		t.Fatalf("exclusive probed trial rejected: lanes=%d err=%v", lanes, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := b.AcquireWorkload(ctx, Workload{Key: "runtime/another", MemoryPerSlot: w.MemoryPerSlot}, 81); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("overestimated trial was not exclusive: %v", err)
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelProbe()
	if releaseProbe, err := b.Acquire(probeCtx, GPU); !errors.Is(err, context.DeadlineExceeded) {
		if releaseProbe != nil {
			releaseProbe()
		}
		t.Fatalf("probe overlapped exclusive trial: %v", err)
	}
	release()
	release()
	if b.active != 0 || b.exclusiveActive || b.reservedMemory != 0 || b.reservedGPU != 0 {
		t.Fatal("exclusive trial leaked reservations")
	}
	for _, exhausted := range []Resources{
		{CPUs: 4, MemoryAvailable: 0, GPUAvailable: -1},
		{CPUs: 4, MemoryAvailable: 11 << 29, GPUAvailable: 0},
	} {
		r = exhausted
		if _, _, err := b.AcquireWorkload(context.Background(), w, 81); !IsPressure(err) {
			t.Fatalf("actual exhausted counter bypassed: %+v err=%v", r, err)
		}
	}
}

func TestAutoWorkloadTrialExcludesOtherKeysActiveLanes(t *testing.T) {
	b := newAdaptive(Settings{MaxProcesses: 64}, func() Resources { return Resources{CPUs: 64, MemoryAvailable: -1, GPUAvailable: -1} })
	b.PrepareWorkload(Workload{Key: "larger-envelope"}) // Shared ceiling remains 8.
	sheet, preview := Workload{Key: "sheet"}, Workload{Key: "preview"}
	b.PrepareWorkload(sheet)
	b.Observe(sheet, 8, time.Second, 81, &PressureError{Err: errors.New("allocation failure")})
	b.PrepareWorkload(preview)
	b.Observe(preview, 2, time.Second, 1, &PressureError{Err: errors.New("allocation failure")})
	lanes, releaseSheet, err := b.AcquireWorkload(context.Background(), sheet, 4)
	if err != nil || lanes != 4 {
		t.Fatalf("sheet admission: %d %v", lanes, err)
	}
	defer releaseSheet()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lanes, releasePreview, err := b.AcquireWorkload(ctx, preview, 1)
	if err != nil || lanes != 1 {
		t.Fatalf("unrelated sheet consumed preview trial despite shared headroom: %d %v", lanes, err)
	}
	defer releasePreview()
	blocked, cancelBlocked := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelBlocked()
	if _, _, err := b.AcquireWorkload(blocked, preview, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-key trial limit bypassed: %v", err)
	}
	releasePreview()
	releaseSheet()
	if b.active != 0 || b.gpuActive != 0 || len(b.gpuByWorkload) != 0 {
		t.Fatal("per-workload release leaked active lanes")
	}
}

func TestAutoCancellationDuringGrantReleasesWorkloadAndExclusiveState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnProbe := false
	b := newAdaptive(Settings{}, func() Resources {
		if cancelOnProbe {
			cancel()
		}
		return Resources{CPUs: 4, MemoryAvailable: 11 << 29, GPUAvailable: -1}
	})
	cancelOnProbe = true // Cancellation races the grant after the initial ctx check.
	w := Workload{Key: "runtime/8K", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30}
	if _, release, err := b.AcquireWorkload(ctx, w, 81); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("cancelled grant accepted: %v", err)
	}
	if b.active != 0 || b.gpuActive != 0 || b.exclusiveActive || len(b.gpuByWorkload) != 0 || b.reservedMemory != 0 || b.reservedGPU != 0 {
		t.Fatal("cancellation leaked grant/reservation state")
	}
}

func TestQueuedUnfitAutoHeadFailsAfterDrainAndLaterCPUProgresses(t *testing.T) {
	var available atomic.Int64
	available.Store(2 << 30)
	b := newAdaptive(Settings{MaxProcesses: 4}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: available.Load(), GPUAvailable: -1} })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	active, err := b.Acquire(ctx, CPU)
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	go func() {
		_, release, err := b.AcquireWorkload(ctx, Workload{Key: "unfit", MemoryPerSlot: 2 << 30}, 4)
		if release != nil {
			release()
		}
		failed <- err
	}()
	waitForQueue(t, b, 1)
	later := make(chan func(), 1)
	go func() {
		release, err := b.Acquire(ctx, CPU)
		if err == nil {
			later <- release
		}
	}()
	waitForQueue(t, b, 2)
	available.Store(0) // An exhausted counter, rather than an oversized estimate.
	active()
	select {
	case err := <-failed:
		if !IsPressure(err) {
			t.Fatalf("unfit queued head did not fail: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("unfit queued head stranded after drain")
	}
	select {
	case release := <-later:
		release()
	case <-ctx.Done():
		t.Fatal("unfit head starved later CPU work")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) != 0 || b.active != 0 || b.reservedMemory != 0 {
		t.Fatal("failed waiter leaked reservation/queue state")
	}
}

func TestAutoGPUCapacityCanExceedCPUCountWithoutIncreasingCPULeafLimit(t *testing.T) {
	b := newAdaptive(Settings{}, func() Resources { return Resources{CPUs: 4, MemoryAvailable: 16 << 30, GPUAvailable: 8 << 30} })
	w := Workload{Key: "runtime/codec", MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
	lanes, release, err := b.AcquireWorkload(context.Background(), w, 81)
	if err != nil {
		t.Fatal(err)
	}
	if lanes <= 4 || b.Settings().MaxProcesses != lanes {
		t.Fatalf("CPU count became GPU cap: lanes=%d settings=%+v", lanes, b.Settings())
	}
	release()
	var cpu []func()
	for range 4 {
		release, err := b.Acquire(context.Background(), CPU)
		if err != nil {
			t.Fatal(err)
		}
		cpu = append(cpu, release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := b.Acquire(ctx, CPU); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("GPU sizing enlarged CPU concurrency: %v", err)
	}
	for _, release := range cpu {
		release()
	}
	// An explicit total limit still caps the same hardware/workload envelope.
	manualTotal := newAdaptive(Settings{MaxProcesses: 3}, b.resources)
	lanes, release, err = manualTotal.AcquireWorkload(context.Background(), w, 3)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if lanes > 3 || manualTotal.Settings().MaxProcesses != 3 {
		t.Fatal("manual total limit was overridden")
	}
}

func TestQueuedWorkloadUsesLiveBackoffDespiteAnotherPreparedKey(t *testing.T) {
	b := newAdaptive(Settings{MaxProcesses: 16}, func() Resources { return Resources{CPUs: 16, MemoryAvailable: -1, GPUAvailable: -1} })
	a := Workload{Key: "A"}
	other := Workload{Key: "B"}
	if b.PrepareWorkload(a) != 4 {
		t.Fatal("unexpected initial trial")
	}
	held, err := b.AcquireN(context.Background(), CPU, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	granted := make(chan int, 1)
	go func() {
		lanes, release, err := b.AcquireWorkload(ctx, a, 16)
		if err != nil {
			granted <- 0
			return
		}
		granted <- lanes
		release()
	}()
	waitForQueue(t, b, 1)
	b.Observe(a, 4, time.Second, 81, &PressureError{Err: errors.New("explicit allocation failure")})
	if b.PrepareWorkload(other) != 4 {
		t.Fatal("other workload did not have independent capacity")
	}
	held()
	select {
	case lanes := <-granted:
		if lanes != 2 {
			t.Fatalf("other key bypassed queuedA backoff: %d", lanes)
		}
	case <-ctx.Done():
		t.Fatal("queuedA did not progress")
	}
	if b.Settings().MaxGPUProcesses != 4 {
		t.Fatal("reporting currentB changed due to dispatchA")
	}
}

func TestAutoThreadsFollowAdaptiveTotalAndPreserveExplicitThreads(t *testing.T) {
	for _, threads := range []int{0, 7} {
		b := newAdaptive(Settings{Threads: threads}, func() Resources { return Resources{CPUs: 16, MemoryAvailable: 512 << 20, GPUAvailable: -1} })
		w := Workload{Key: "runtime/small", MemoryPerSlot: 640 * 360 * 48}
		b.PrepareWorkload(w)
		want := 4
		if threads != 0 {
			want = threads
		}
		if got := b.Settings(); got.MaxProcesses != 4 || got.Threads != want {
			t.Fatalf("prepared settings %+v, want total4 threads%d", got, want)
		}
		for range 2 {
			b.Observe(w, 4, time.Second, 81, nil)
		}
		want = 3
		if threads != 0 {
			want = threads
		}
		if got := b.Settings(); got.MaxProcesses != 5 || got.Threads != want {
			t.Fatalf("grown settings %+v, want total5 threads%d", got, want)
		}
	}
}

func TestAutoLearnsOnlyExercisedConcurrency(t *testing.T) {
	b := newAdaptive(Settings{MaxProcesses: 16}, func() Resources { return Resources{CPUs: 16, MemoryAvailable: -1, GPUAvailable: -1} })
	w := Workload{Key: "runtime/partial"}
	if got := b.PrepareWorkload(w); got != 4 {
		t.Fatal(got)
	}
	for range 4 {
		b.Observe(w, 1, time.Second, 81, nil)
	}
	if got := b.PrepareWorkload(w); got != 4 {
		t.Fatal("serial success falsely proved four-lane trial", got)
	}
	b.Observe(w, 2, time.Second, 81, &PressureError{Err: errors.New("OOM")})
	if got := b.PrepareWorkload(w); got != 1 {
		t.Fatal("pressure at two granted lanes did not reduce below two", got)
	}
}

func TestIncompleteRuntimeIdentityDoesNotReuseRenderingEvidence(t *testing.T) {
	b := newAdaptive(Settings{MaxProcesses: 16}, func() Resources { return Resources{CPUs: 16, MemoryAvailable: -1, GPUAvailable: -1} })
	w := Workload{Key: "unidentified/codec", RuntimeUnidentified: true}
	if got := b.PrepareWorkload(w); got != 4 {
		t.Fatal(got)
	}
	b.Observe(w, 4, time.Second, 81, &PressureError{Err: errors.New("OOM")})
	if b.Settings().MaxGPUProcesses != 2 {
		t.Fatal("completed trial did not report pressure reduction")
	}
	if got := b.PrepareWorkload(w); got != 4 {
		t.Fatal("incomplete identity reused previous runtime evidence", got)
	}
	if len(b.learning) != 0 {
		t.Fatal("incomplete identity accumulated learned states")
	}
}
