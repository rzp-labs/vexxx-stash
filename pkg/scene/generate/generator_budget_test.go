package generate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

type testBudgetConfig struct{ budget *generationbudget.Budget }

func (c testBudgetConfig) GetTranscodeInputArgs() []string               { return nil }
func (c testBudgetConfig) GetTranscodeOutputArgs() []string              { return nil }
func (c testBudgetConfig) GetGenerationBudget() *generationbudget.Budget { return c.budget }

func TestGeneratorSharedBudgetAndOverride(t *testing.T) {
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := Generator{FFMpegConfig: testBudgetConfig{budget: budget}}
	other := Generator{FFMpegConfig: testBudgetConfig{budget: budget}}
	if g.generationBudget() != other.generationBudget() {
		t.Fatal("generators did not share budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	args, release, err := g.acquireGeneration(ctx, []string{"-i", "in.mp4", "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 5 {
		t.Fatal("threads not bounded")
	}
	cancel()
	if _, _, err := other.acquireGeneration(ctx, []string{"out.mp4"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter admitted: %v", err)
	}
	release()
	release()
	override, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 2})
	g.Budget = override
	if g.generationBudget() != override {
		t.Fatal("explicit override ignored")
	}
}

func TestGeneratorHardwareAdmissionUsesGPULimit(t *testing.T) {
	for _, args := range [][]string{
		{"-c:v", "h264_rkmpp", "out.mp4"},
		{"-c:v:0", "h264_v4l2m2m", "out.mp4"},
		{"-hwaccel", "d3d11va", "-i", "in.mp4", "-c:v", "libwebp", "out.webp"},
		{"-hwaccel", "rkmpp", "-i", "in.mp4", "-c:v", "bmp", "out.bmp"},
	} {
		t.Run(args[1], func(t *testing.T) {
			budget, err := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 1})
			if err != nil {
				t.Fatal(err)
			}
			g := Generator{Budget: budget}
			hardware, err := budget.Acquire(context.Background(), generationbudget.GPU)
			if err != nil {
				t.Fatal(err)
			}
			defer hardware()
			// Total slots remain free, but hardware admission must wait for the GPU slot.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_, release, err := g.acquireGeneration(ctx, args)
			if release != nil {
				release()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("hardware bypassed occupied GPU slot: %v", err)
			}
			// A cancelled hardware waiter must not prevent CPU work or later reuse.
			_, cpu, err := g.acquireGeneration(context.Background(), []string{"-c:v", "libx264", "out.mp4"})
			if err != nil {
				t.Fatal(err)
			}
			cpu()
			hardware()
			_, release, err = g.acquireGeneration(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestGeneratorWeightedHardwareAdmission(t *testing.T) {
	budget, err := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 3, Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	g := Generator{Budget: budget}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	args := []string{"-hwaccel", "vaapi", "-i", "in0.mp4", "-hwaccel", "vaapi", "-i", "in1.mp4", "-hwaccel", "vaapi", "-i", "in2.mp4", "out.jpg"}
	bounded, weighted, err := g.acquireGenerationN(ctx, args, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer weighted()
	inputs := 0
	for i, arg := range bounded {
		if arg == "-i" {
			inputs++
			if i < 2 || bounded[i-2] != "-threads" || bounded[i-1] != "2" {
				t.Fatalf("input threads not bounded: %v", bounded)
			}
		}
	}
	if inputs != 3 || bounded[len(bounded)-3] != "-threads" || bounded[len(bounded)-2] != "2" {
		t.Fatalf("weighted render threading changed: %v", bounded)
	}
	// The weighted render exhausts GPU slots, while one CPU slot remains free.
	wait, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if _, release, err := g.acquireGeneration(wait, []string{"-hwaccel", "vaapi", "-i", "probe.mp4", "-f", "null", "-"}); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("single GPU probe bypassed weighted reservation: %v", err)
	}
	_, cpu, err := g.acquireGeneration(ctx, []string{"-c:v", "libx264", "out.mp4"})
	if err != nil {
		t.Fatal("remaining CPU slot unavailable", err)
	}
	cpu()
	weighted()
	_, release, err := g.acquireGenerationN(ctx, args, 3)
	if err != nil {
		t.Fatal("weighted reservation leaked", err)
	}
	release()
}

func TestGeneratorWeightedFailureReleasesPermits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixtures")
	}
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 3, MaxGPUProcesses: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, path := range []string{filepath.Join(t.TempDir(), "missing-ffmpeg"), "/usr/bin/false"} {
		g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(path)}
		lock := fsutil.NewReadLockManager().ReadLock(ctx, "synthetic.mp4")
		if err := g.generateWithContextN(ctx, lock, []string{"-hwaccel", "vaapi", "out.jpg"}, 3); err == nil {
			t.Fatal("expected execution failure")
		}
		lock.Cancel()
		release, err := budget.AcquireN(ctx, generationbudget.GPU, 3)
		if err != nil {
			t.Fatal("failed process leaked weighted reservation", err)
		}
		release()
	}
}

func TestGeneratorFailureReleasesPermit(t *testing.T) {
	budget, _ := generationbudget.New(generationbudget.Settings{})
	for _, path := range []string{filepath.Join(t.TempDir(), "missing-ffmpeg"), "/usr/bin/false"} {
		if runtime.GOOS == "windows" {
			t.Skip("Unix command fixtures")
		}
		g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(path)}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lock := fsutil.NewReadLockManager().ReadLock(ctx, "synthetic.mp4")
		if err := g.generate(lock, []string{"out.mp4"}); err == nil {
			t.Fatal("expected execution failure")
		}
		if _, err := g.generateOutput(lock, []string{"out.mp4"}); err == nil {
			t.Fatal("expected output execution failure")
		}
		lock.Cancel()
		release, err := budget.Acquire(ctx, generationbudget.CPU)
		if err != nil {
			t.Fatal("failed process leaked permit", err)
		}
		release()
		cancel()
	}
}

func TestGeneratorActiveCancellationReleasesPermit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixture")
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	fixture := filepath.Join(dir, "ffmpeg")
	// This fixture ignores FFmpeg options. -version must finish during NewEncoder.
	script := "#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffmpeg version 7.0'; exit 0; fi\n: > '" + started + "'\nexec sleep 30\n"
	if err := os.WriteFile(fixture, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 3, MaxGPUProcesses: 3})
	g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(fixture)}
	ctx, cancel := context.WithCancel(context.Background())
	lock := fsutil.NewReadLockManager().ReadLock(ctx, "synthetic.mp4")
	defer lock.Cancel()
	done := make(chan error, 1)
	go func() { done <- g.generateWithContextN(ctx, lock, []string{"-hwaccel", "vaapi", "out.jpg"}, 3) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("fixture did not start")
		}
		runtime.Gosched()
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled process succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process cancellation hung")
	}
	fallbackCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	release, err := budget.AcquireN(fallbackCtx, generationbudget.GPU, 3)
	if err != nil {
		t.Fatal("cancelled process leaked permit", err)
	}
	release()
}

func TestGeneratorProbeTimeoutStartsAfterBudgetAdmission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	fixture := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffmpeg version 7.0'; fi\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(fixture)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Occupy the only total slot longer than the probe execution timeout.
	release, err := budget.Acquire(ctx, generationbudget.CPU)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	probeCtx := ffmpeg.WithIntelProbeTimeout(ctx, 20*time.Millisecond)
	lock := fsutil.NewReadLockManager().ReadLock(ctx, "synthetic.mp4")
	defer lock.Cancel()
	done := make(chan error, 1)
	go func() { done <- g.generateWithContext(probeCtx, lock, []string{"-hwaccel", "vaapi", "out.mp4"}) }()
	timer := time.NewTimer(60 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		t.Fatalf("probe finished before budget admission: %v", err)
	case <-timer.C:
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("queue wait consumed probe execution timeout: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestGeneratorSoftwareFilterDoesNotWaitForGPUSlot(t *testing.T) {
	budget, err := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 1})
	if err != nil {
		t.Fatal(err)
	}
	hardware, err := budget.Acquire(context.Background(), generationbudget.GPU)
	if err != nil {
		t.Fatal(err)
	}
	defer hardware()
	g := Generator{Budget: budget}
	for _, graph := range []string{"drawtext=text=hwupload", "drawtext=text='example,hwmap;scale_rkrga'", "subtitles=filename='/tmp/scale_qsv.srt'"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, release, err := g.acquireGeneration(ctx, []string{"-vf", graph, "-c:v", "libx264", "out.mp4"})
		if release != nil {
			release()
		}
		cancel()
		if err != nil {
			t.Fatalf("software graph %q waited for GPU slot: %v", graph, err)
		}
	}
}
