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
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(fixture)}
	ctx, cancel := context.WithCancel(context.Background())
	lock := fsutil.NewReadLockManager().ReadLock(ctx, "synthetic.mp4")
	defer lock.Cancel()
	done := make(chan error, 1)
	go func() { done <- g.generate(lock, []string{"out.mp4"}) }()
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
	release, err := budget.Acquire(fallbackCtx, generationbudget.CPU)
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
	lock := fsutil.NewReadLockManager().ReadLock(probeCtx, "synthetic.mp4")
	defer lock.Cancel()
	done := make(chan error, 1)
	go func() { done <- g.generate(lock, []string{"-hwaccel", "vaapi", "out.mp4"}) }()
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
