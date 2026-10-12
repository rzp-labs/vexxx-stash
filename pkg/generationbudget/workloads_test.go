package generationbudget

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIndependentAutoPreservesManualOtherWorkloadAdmission(t *testing.T) {
	for _, resources := range []Resources{
		{CPUs: 4, MemoryAvailable: 0, GPUAvailable: 1 << 30},
		{CPUs: 4, MemoryAvailable: 1 << 30, GPUAvailable: 0},
		{CPUs: 4, MemoryAvailable: 0, GPUAvailable: 0},
	} {
		for _, independent := range []bool{false, true} {
			probe := func() Resources { return resources }
			settings := Settings{MaxProcesses: 2, MaxGPUProcesses: 2}
			var b *Budget
			var err error
			if independent {
				b, err = NewIndependentAutoWithResources(settings, probe)
			} else {
				b, err = NewWithResources(settings, probe)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			w := Workload{Kind: OtherWorkload, Key: "manual-marker", MemoryPerSlot: 8 << 20, GPUPerSlot: 8 << 20}
			n, release, err := b.AcquireWorkload(ctx, w, 2)
			cancel()
			if err != nil || n != 2 {
				t.Fatalf("manual marker admission changed (independent=%v, resources=%+v): %d %v", independent, resources, n, err)
			}
			if b.reservedMemory != 0 || b.reservedGPU != 0 {
				t.Fatal("manual marker unexpectedly reserved Auto headroom")
			}
			release()
			if b.active != 0 || b.gpuActive != 0 || b.reservedMemory != 0 || b.reservedGPU != 0 {
				t.Fatal("manual marker release leaked accounting")
			}
			if independent {
				for _, kind := range []WorkloadKind{SpriteWorkload, PreviewWorkload} {
					w.Kind = kind
					_, _, err := b.AcquireWorkload(context.Background(), w, 2)
					if !IsPressure(err) {
						t.Fatalf("Auto family bypassed exhausted headroom: kind=%v err=%v", kind, err)
					}
				}
			}
		}
	}
}

func TestManualOtherReleasePreservesAutoReservations(t *testing.T) {
	b, err := NewIndependentAutoWithResources(Settings{MaxProcesses: 2, MaxGPUProcesses: 2}, func() Resources {
		return Resources{CPUs: 4, MemoryAvailable: 1 << 30, GPUAvailable: 1 << 30}
	})
	if err != nil {
		t.Fatal(err)
	}
	sprite := Workload{Key: "sprite", Kind: SpriteWorkload, MemoryPerSlot: 8 << 20, GPUPerSlot: 8 << 20}
	_, rs, err := b.AcquireWorkload(context.Background(), sprite, 1)
	if err != nil {
		t.Fatal(err)
	}
	host, gpu := b.reservedMemory, b.reservedGPU
	marker := sprite
	marker.Key, marker.Kind = "marker", OtherWorkload
	_, rm, err := b.AcquireWorkload(context.Background(), marker, 1)
	if err != nil {
		t.Fatal(err)
	}
	if b.reservedMemory != host || b.reservedGPU != gpu || host == 0 || gpu == 0 {
		t.Fatal("manual marker changed protected workload reservations")
	}
	rm()
	if b.reservedMemory != host || b.reservedGPU != gpu {
		t.Fatal("manual marker release removed Auto reservations")
	}
	rs()
	if b.reservedMemory != 0 || b.reservedGPU != 0 || b.active != 0 || b.gpuActive != 0 {
		t.Fatal("mixed release leaked accounting")
	}
}

func TestWorkloadAggregateAcrossPlansAndCancellation(t *testing.T) {
	b, _ := NewIndependentAutoWithResources(Settings{MaxProcesses: 8, MaxGPUProcesses: 8}, func() Resources { return Resources{CPUs: 8, MemoryAvailable: -1, GPUAvailable: -1} })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sw := Workload{Key: "sprite/fileA", Kind: SpriteWorkload}
	pw := Workload{Key: "preview/fileA", Kind: PreviewWorkload}
	n, rs, err := b.AcquireWorkload(ctx, sw, 8)
	if err != nil || n != 2 {
		t.Fatalf("sprite %d %v", n, err)
	}
	n, rp, err := b.AcquireWorkload(ctx, pw, 8)
	if err != nil || n != 2 {
		t.Fatalf("preview %d %v", n, err)
	}
	sw.Key = "sprite/fileB"
	blocked, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	_, _, err = b.AcquireWorkload(blocked, sw, 8)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("new sprite plan bypassed family ceiling", err)
	}
	rs()
	rp()
	n, r, err := b.AcquireWorkload(ctx, sw, 8)
	if err != nil || n != 2 {
		t.Fatalf("capacity not reusable %d %v", n, err)
	}
	r()
	if len(b.queue) != 0 || b.active != 0 || b.gpuByKind[SpriteWorkload] != 0 || b.gpuByKind[PreviewWorkload] != 0 {
		t.Fatal("cancelled waiter leaked")
	}
}

func TestMixedManualResourcePressureAndFIFO(t *testing.T) {
	r := Resources{CPUs: 8, MemoryAvailable: 2 << 30, GPUAvailable: -1}
	b, _ := NewIndependentAutoWithResources(Settings{MaxProcesses: 8, MaxGPUProcesses: 8}, func() Resources { return r })
	sw := Workload{Key: "sprite", Kind: SpriteWorkload, MemoryPerSlot: 1 << 30}
	pw := Workload{Key: "preview", Kind: PreviewWorkload, MemoryPerSlot: 1 << 30}
	n, rs, err := b.AcquireWorkload(context.Background(), sw, 8)
	if err != nil || n != 1 {
		t.Fatalf("resource admission %d %v", n, err)
	}
	granted := make(chan func(), 1)
	go func() {
		_, release, e := b.AcquireWorkload(context.Background(), pw, 8)
		if e != nil {
			t.Error(e)
			granted <- nil
		} else {
			granted <- release
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		queued := len(b.queue) == 1
		b.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	later, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if _, err := b.Acquire(later, CPU); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("later CPU bypassed FIFO", err)
	}
	stop()
	rs()
	select {
	case release := <-granted:
		if release == nil {
			t.Fatal("preview failed")
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("preview starved")
	}
	r.MemoryAvailable = 0
	if _, _, err := b.AcquireWorkload(context.Background(), sw, 1); !IsPressure(err) {
		t.Fatal("Auto workload bypassed exhausted resources", err)
	}
	if b.active != 0 || b.reservedMemory != 0 || len(b.queue) != 0 {
		t.Fatal("resource cleanup leaked")
	}
}

func TestWorkloadAutoWithManualSharedGPUCeiling(t *testing.T) {
	b, _ := NewIndependentAutoWithResources(Settings{MaxProcesses: 64, MaxGPUProcesses: 5}, func() Resources { return Resources{CPUs: 64, MemoryAvailable: 32 << 30, GPUAvailable: -1} })
	w := Workload{Key: "sprite", Kind: SpriteWorkload, MemoryPerSlot: 16 << 20}
	initial := b.PrepareWorkload(w)
	for range 20 {
		b.Observe(w, b.PrepareWorkload(w), time.Second, 81, nil)
	}
	if b.PrepareWorkload(w) != 5 || initial >= 5 || b.Settings().MaxGPUProcesses != 5 {
		t.Fatal("manual shared limit disabled Auto or was exceeded")
	}
}

func TestIndependentAutoRecoversObservableHeadroom(t *testing.T) {
	resources := Resources{CPUs: 8, MemoryAvailable: 0, GPUAvailable: -1}
	b, _ := NewIndependentAutoWithResources(Settings{MaxProcesses: 8, MaxGPUProcesses: 8}, func() Resources { return resources })
	w := Workload{Key: "sprite", Kind: SpriteWorkload, MemoryPerSlot: 1 << 30}
	if _, _, err := b.AcquireWorkload(context.Background(), w, 8); !IsPressure(err) {
		t.Fatal("zero headroom admitted", err)
	}
	resources.MemoryAvailable = 4 << 30
	n, release, err := b.AcquireWorkload(context.Background(), w, 8)
	if err != nil || n != 1 {
		t.Fatalf("Auto request failed to recover headroom: %d %v", n, err)
	}
	release()
	if b.Settings().MaxGPUProcesses != 8 {
		t.Fatal("pressure replaced manual ceiling")
	}
}

func TestIndependentAutoAggregateAndScopeCleanup(t *testing.T) {
	b, _ := NewIndependentAutoWithResources(Settings{MaxProcesses: 6, MaxGPUProcesses: 6}, func() Resources { return Resources{CPUs: 8, MemoryAvailable: 8 << 30, GPUAvailable: -1} })
	base := Workload{Key: "sprite-A", Kind: SpriteWorkload, MemoryPerSlot: 64 << 20}
	scoped, cleanup := b.ScopeWorkload(base)
	n, release, err := b.AcquireWorkload(context.Background(), scoped, 6)
	if err != nil || n != 2 {
		t.Fatalf("Auto scoped lanes: %d %v", n, err)
	}
	blocked, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, _, err = b.AcquireWorkload(blocked, Workload{Key: "sprite-B", Kind: SpriteWorkload, MemoryPerSlot: 64 << 20}, 6)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("independent plan bypassed Auto aggregate ceiling", err)
	}
	release()
	cleanup(false)
	if _, retained := b.learning[scoped.Key]; retained {
		t.Fatal("scope controller retained")
	}
	if b.active != 0 || b.reservedMemory != 0 || len(b.queue) != 0 {
		t.Fatal("scope cleanup leaked permits")
	}
	// Discarded scope cannot leave an effective ceiling above live family evidence.
	if b.PrepareWorkload(Workload{Kind: SpriteWorkload}) != 2 {
		t.Fatal("remaining plan evidence was discarded")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	n, release, err = b.AcquireWorkload(ctx, base, 6)
	if err != nil || n != 2 {
		t.Fatalf("scope cancellation prevented reuse: %d %v", n, err)
	}
	release()
}
