package generationbudget

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

func newTestBudget(t *testing.T, settings Settings) *Budget {
	t.Helper()
	b, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func waitForQueue(t *testing.T, b *Budget, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second * 5)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		actual := len(b.queue)
		b.mu.Unlock()
		if actual == count {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("queue did not reach %d", count)
}

func TestSettings(t *testing.T) {
	s, err := (Settings{}).Normalize()
	if err != nil || s != (Settings{1, 1, 1}) {
		t.Fatalf("auto: %+v, %v", s, err)
	}
	for _, s := range []Settings{{MaxProcesses: -1}, {MaxGPUProcesses: -1}, {Threads: -1}, {MaxProcesses: 65}, {MaxGPUProcesses: 65}, {Threads: 65}, {MaxProcesses: 1, MaxGPUProcesses: 2}} {
		if _, err := New(s); err == nil {
			t.Fatalf("accepted invalid settings %+v", s)
		}
	}
}

func TestSharedCPUAndGPU(t *testing.T) {
	b := newTestBudget(t, Settings{MaxProcesses: 2, MaxGPUProcesses: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := b.Acquire(ctx, GPU)
	if err != nil {
		t.Fatal(err)
	}
	cpu, err := b.Acquire(ctx, CPU)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan func(), 1)
	go func() {
		release, err := b.Acquire(ctx, GPU)
		if err == nil {
			ready <- release
		}
	}()
	waitForQueue(t, b, 1)
	cpu()
	// A free total slot cannot admit a second GPU while the GPU slot is held.
	b.mu.Lock()
	active, gpu, queued := b.active, b.gpuActive, len(b.queue)
	b.mu.Unlock()
	if active != 1 || gpu != 1 || queued != 1 {
		t.Fatalf("unexpected accounting %d/%d queued %d", active, gpu, queued)
	}
	first()
	select {
	case release := <-ready:
		release()
		release()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != 0 || b.gpuActive != 0 {
		t.Fatalf("permits leaked: %d/%d", b.active, b.gpuActive)
	}
}

func TestFIFOAndCancelledHead(t *testing.T) {
	b := newTestBudget(t, Settings{MaxProcesses: 2, MaxGPUProcesses: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, _ := b.Acquire(ctx, GPU)
	blocked, stop := context.WithCancel(ctx)
	gpuErr := make(chan error, 1)
	go func() {
		release, err := b.Acquire(blocked, GPU)
		if release != nil {
			release()
		}
		gpuErr <- err
	}()
	waitForQueue(t, b, 1)
	cpuReady := make(chan func(), 1)
	go func() {
		release, err := b.Acquire(ctx, CPU)
		if err == nil {
			cpuReady <- release
		}
	}()
	waitForQueue(t, b, 2) // FIFO: CPU may not jump the GPU waiter.
	stop()
	if err := <-gpuErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	select {
	case release := <-cpuReady:
		release()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	first()
}

func TestWeightedAdmissionIsAtomicAndFIFO(t *testing.T) {
	b := newTestBudget(t, Settings{MaxProcesses: 4, MaxGPUProcesses: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := b.Acquire(ctx, GPU)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	weightedReady := make(chan func(), 1)
	go func() {
		release, err := b.AcquireN(ctx, GPU, 3)
		if err == nil {
			weightedReady <- release
		}
	}()
	waitForQueue(t, b, 1)
	cpuReady := make(chan func(), 1)
	go func() {
		release, err := b.Acquire(ctx, CPU)
		if err == nil {
			cpuReady <- release
		}
	}()
	waitForQueue(t, b, 2)
	b.mu.Lock()
	active, gpu := b.active, b.gpuActive
	b.mu.Unlock()
	if active != 1 || gpu != 1 {
		t.Fatalf("partially reserved or bypassed FIFO: %d/%d", active, gpu)
	}
	first()
	select {
	case release := <-weightedReady:
		defer release()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case release := <-cpuReady:
		defer release()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != 4 || b.gpuActive != 3 || len(b.queue) != 0 {
		t.Fatalf("weighted reservation not fully accounted: %d/%d queued %d", b.active, b.gpuActive, len(b.queue))
	}
}

func TestWeightedCancelledHeadAndReleaseReuse(t *testing.T) {
	b := newTestBudget(t, Settings{MaxProcesses: 4, MaxGPUProcesses: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := b.AcquireN(ctx, GPU, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	blocked, stop := context.WithCancel(ctx)
	defer stop()
	weightedErr := make(chan error, 1)
	go func() {
		release, err := b.AcquireN(blocked, GPU, 3)
		if release != nil {
			release()
		}
		weightedErr <- err
	}()
	waitForQueue(t, b, 1)
	cpuReady := make(chan func(), 1)
	go func() {
		release, err := b.AcquireN(ctx, CPU, 2)
		if err == nil {
			cpuReady <- release
		}
	}()
	waitForQueue(t, b, 2)
	stop()
	if err := <-weightedErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	select {
	case release := <-cpuReady:
		release()
		release()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	first()
	first()
	release, err := b.AcquireN(ctx, GPU, 3)
	if err != nil {
		t.Fatal("cancelled reservation leaked permits", err)
	}
	release()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != 0 || b.gpuActive != 0 || len(b.queue) != 0 {
		t.Fatalf("leak after reuse: %+v", b)
	}
}

func TestWeightedGPUReservationRespectsTotalLimit(t *testing.T) {
	b := newTestBudget(t, Settings{MaxProcesses: 4, MaxGPUProcesses: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cpu, err := b.AcquireN(ctx, CPU, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer cpu()
	blocked, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		release, err := b.AcquireN(blocked, GPU, 3)
		if release != nil {
			release()
		}
		done <- err
	}()
	waitForQueue(t, b, 1) // All GPU slots are free, but only two total slots are free.
	b.mu.Lock()
	active, gpu := b.active, b.gpuActive
	b.mu.Unlock()
	if active != 2 || gpu != 0 {
		t.Fatalf("GPU request exceeded total capacity: %d/%d", active, gpu)
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cpu()
	release, err := b.AcquireN(ctx, GPU, 3)
	if err != nil {
		t.Fatal("total capacity not reusable", err)
	}
	release()
}

func TestWeightedInvalidAndNestedReservations(t *testing.T) {
	b := newTestBudget(t, Settings{MaxProcesses: 4, MaxGPUProcesses: 3})
	for _, tc := range []struct {
		class Class
		slots int
	}{{CPU, 0}, {GPU, -1}, {CPU, 5}, {GPU, 4}, {Class(99), 1}} {
		if release, err := b.AcquireN(context.Background(), tc.class, tc.slots); err == nil || release != nil {
			t.Fatalf("accepted invalid reservation %+v", tc)
		}
	}
	if err := b.Run(context.Background(), GPU, func(ctx context.Context) error {
		if release, err := b.AcquireN(ctx, GPU, 2); err == nil || release != nil {
			t.Fatal("accepted nested weighted reservation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var disabled *Budget
	release, err := disabled.AcquireN(context.Background(), GPU, 12)
	if err != nil {
		t.Fatal("nil budget rejected valid reservation", err)
	}
	release()
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if release, err := disabled.AcquireN(canceled, GPU, 12); !errors.Is(err, context.Canceled) || release != nil {
		t.Fatal("nil budget admitted cancelled reservation", err)
	}
}

func TestFailureFallbackAndNested(t *testing.T) {
	b := newTestBudget(t, Settings{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failed := errors.New("unsupported GPU filter")
	err := b.Run(ctx, GPU, func(scoped context.Context) error {
		if err := b.Run(scoped, CPU, func(context.Context) error { t.Fatal("nested stage ran"); return nil }); err == nil {
			t.Fatal("nested acquisition accepted")
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if err := b.Run(ctx, CPU, func(context.Context) error { return nil }); err != nil {
		t.Fatal("fallback permit was not released", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if release, err := b.Acquire(canceled, CPU); !errors.Is(err, context.Canceled) || release != nil {
		t.Fatalf("cancelled job admitted: %v", err)
	}
}

func TestContentionAndCancellationRaces(t *testing.T) {
	b := newTestBudget(t, Settings{MaxProcesses: 3, MaxGPUProcesses: 2})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			if i%2 == 0 {
				go cancel()
			} else {
				defer cancel()
			}
			// Mix single and weighted requests while cancellation races admission.
			release, err := b.AcquireN(ctx, Class(i%2), 1+i%2)
			if err != nil {
				return
			}
			b.mu.Lock()
			if b.active > 3 || b.gpuActive > 2 {
				t.Error("budget exceeded")
			}
			b.mu.Unlock()
			runtime.Gosched()
			release()
		}(i)
	}
	wg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != 0 || b.gpuActive != 0 || len(b.queue) != 0 {
		t.Fatalf("leak: %+v", b)
	}
}

func TestFFMpegThreadBoundsAndClassification(t *testing.T) {
	b := newTestBudget(t, Settings{})
	args := []string{"-i", "input.mp4", "-threads", "4", "-threads:v", "0", "-filter_threads", "8", "-filter_complex_threads", "8", "-lossless", "1", "-compression_level", "6", "out.webp"}
	original := append([]string(nil), args...)
	bounded := b.FFMpegArgs(args)
	if !reflect.DeepEqual(args, original) {
		t.Fatal("mutated caller arguments")
	}
	for i, arg := range bounded {
		if arg == "-threads" || arg == "-threads:v" || arg == "-filter_threads" || arg == "-filter_complex_threads" {
			if bounded[i+1] != "1" {
				t.Fatalf("thread cap missing: %v", bounded)
			}
		}
		if arg == "-lossless" && bounded[i+1] != "1" {
			t.Fatal("lossless changed")
		}
		if arg == "-compression_level" && bounded[i+1] != "6" {
			t.Fatal("quality changed")
		}
	}
	if bounded[len(bounded)-1] != "out.webp" {
		t.Fatal("output moved")
	}
	for _, args := range [][]string{{"-c:v", "h264_qsv"}, {"-hwaccel", "vaapi", "-c:v", "libwebp"}, {"-vf", "scale_vaapi=w=640:h=-2"}} {
		if ClassifyFFMpeg(args) != GPU {
			t.Fatalf("hardware job classified CPU: %v", args)
		}
	}
	if ClassifyFFMpeg(original) != CPU {
		t.Fatal("software job classified GPU")
	}
	var disabled *Budget
	if !reflect.DeepEqual(disabled.FFMpegArgs(args), args) {
		t.Fatal("legacy arguments changed")
	}
}
