package generate

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func measuredSpriteFixture(t *testing.T) (Generator, string) {
	g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{})
	b, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
		return generationbudget.Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1}
	}, func(int) generationbudget.ProcessResources {
		return generationbudget.ProcessResources{Memory: 32 << 20, GPU: 64 << 20}
	})
	if err != nil {
		t.Fatal(err)
	}
	g.Budget = b
	return g, dir
}

func spriteHistory(t *testing.T, dir string) []residentSpriteCommand {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "history-sheet.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var history []residentSpriteCommand
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var command residentSpriteCommand
		if err := json.Unmarshal([]byte(line), &command); err != nil {
			t.Fatal(err)
		}
		history = append(history, command)
	}
	return history
}

func fullSpriteTimes() []float64 {
	times := make([]float64, 81)
	for i := range times {
		times[i] = float64(i)
	}
	return times
}

func TestResidentSpriteAutoAdjustsDuringFirstGenerationBeyondEstimate(t *testing.T) {
	g, dir := measuredSpriteFixture(t)
	output := residentSpriteOutput(t, dir, "sheet")
	if err := os.WriteFile(filepath.Join(dir, "timing-sheet"), []byte("30"), 0600); err != nil {
		t.Fatal(err)
	}
	residentSpriteRelease(t, dir, "sheet")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := g.IntelSpriteSheet(ctx, "synthetic.mp4", fullSpriteTimes(), 9, 9, output); err != nil {
		t.Fatal(err)
	}
	history := spriteHistory(t, dir)
	if len(history) < 3 || len(history[0].Seeks) != 1 || len(history[len(history)-1].Seeks) <= 1 {
		t.Fatal("first render remained at estimated ceiling1", len(history), len(history[len(history)-1].Seeks))
	}
	for i, command := range history {
		args := strings.Join(command.Args, " ")
		if !strings.Contains(args, "-hwaccel_strict 1") || !strings.Contains(args, "-c:v mjpeg_vaapi") || strings.Contains(args, "hwdownload") || strings.Contains(args, "hwupload") || strings.Contains(args, "libx264") {
			t.Fatal("Auto changed strict GPU rendering", i, args)
		}
	}
	if count := strings.Count(strings.Join(history[len(history)-1].Seeks, ""), "\ninpoint "); count != 81 {
		t.Fatal("final render omitted canonical seeks", count)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".*")); len(leftovers) != 0 {
		t.Fatal("calibration files leaked", leftovers)
	}
}

func TestResidentSpriteInvalidCalibrationNeverPublishesItsOutput(t *testing.T) {
	g, dir := measuredSpriteFixture(t)
	output := residentSpriteOutput(t, dir, "sheet")
	if err := os.WriteFile(filepath.Join(dir, "invalid-trial-sheet"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	residentSpriteRelease(t, dir, "sheet")
	if _, err := g.IntelSpriteSheet(context.Background(), "synthetic.mp4", fullSpriteTimes(), 9, 9, output); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, format, err := image.DecodeConfig(f); err != nil || format != "jpeg" {
		t.Fatal("invalid temporary trial published", format, err)
	}
	history := spriteHistory(t, dir)
	if len(history) != 2 || len(history[1].Seeks) != 1 {
		t.Fatal("invalid trial taught higher capacity", len(history))
	}
}

func TestResidentSpriteCalibrationCancellationDrainsAndPreservesExisting(t *testing.T) {
	g, dir := measuredSpriteFixture(t)
	output := residentSpriteOutput(t, dir, "sheet")
	if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := g.IntelSpriteSheet(ctx, "synthetic.mp4", fullSpriteTimes(), 9, 9, output)
		done <- err
	}()
	command := residentSpriteStart(t, ctx, dir, "sheet")
	if !strings.HasPrefix(filepath.Base(command.Args[len(command.Args)-1]), ".auto-sprite-") {
		t.Fatal("fixture did not cancel calibration")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("calibration did not drain")
	}
	if data, _ := os.ReadFile(output); string(data) != "existing" {
		t.Fatal("cancelled calibration replaced existing output")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".*")); len(leftovers) != 0 {
		t.Fatal("cancel leaked files", leftovers)
	}
	check, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	release, err := g.Budget.Acquire(check, generationbudget.GPU)
	if err != nil {
		t.Fatal("calibration leaked permits", err)
	}
	release()
}

func TestSpriteTuningComparesEqualWorkWhenStartupAmortizes(t *testing.T) {
	b, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
		return generationbudget.Resources{CPUs: 4, MemoryAvailable: 4 << 30, GPUAvailable: -1}
	}, func(int) generationbudget.ProcessResources {
		return generationbudget.ProcessResources{Memory: 64 << 20, GPU: 64 << 20}
	})
	if err != nil {
		t.Fatal(err)
	}
	w := generationbudget.Workload{Key: "same-file", MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
	type trial struct{ lanes, tiles int }
	var trials []trial
	render := func(ctx context.Context, lanes int, times []float64, path string) error {
		trials = append(trials, trial{lanes, len(times)})
		generationbudget.RecordProcess(ctx, 7)
		// Startup is fixed. Higher lane counts are slower per tile, although
		// the old unequal-work comparison favored them by amortizing startup.
		perTile := 10 * time.Millisecond
		if lanes > 4 {
			perTile = 12 * time.Millisecond
		}
		timer := time.NewTimer(100*time.Millisecond + time.Duration(len(times))*perTile)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		err = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 72, 72)), nil)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	g := Generator{}
	if err := g.tuneSprite(context.Background(), nil, b, w, ffmpeg.IntelGenerationPlan{Filter: "scale_vaapi=w=8:h=8"}, fullSpriteTimes(), 9, 9, 81, t.TempDir(), render); err != nil {
		t.Fatal(err)
	}
	if len(trials) < 3 || trials[0] != (trial{4, 4}) || trials[1] != (trial{4, 8}) || trials[2] != (trial{8, 8}) {
		t.Fatal("expanded candidate was not compared with equal baseline work", trials)
	}
	if got := b.Settings().MaxGPUProcesses; got != 4 {
		t.Fatal("startup amortization selected the slower full-sheet count", got, trials)
	}
	if len(trials) > 6 {
		t.Fatal("replacement baseline escaped the trial count bound", trials)
	}
}

func TestSpriteTuningSkipsFirstSamplesThatExceedProjectedAllowance(t *testing.T) {
	for _, tc := range []struct{ tiles, initialLanes int }{{6, 1}, {18, 4}, {81, 21}} {
		// The initial sqrt seed is controlled independently of tile count.
		b, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
			return generationbudget.Resources{CPUs: 4, MemoryAvailable: int64(tc.initialLanes*tc.initialLanes) * (256 << 20), GPUAvailable: -1}
		})
		if err != nil {
			t.Fatal(err)
		}
		w := generationbudget.Workload{Key: "skip", MemoryPerSlot: 128 << 20}
		times := make([]float64, tc.tiles)
		calls := 0
		if err := (Generator{}).tuneSprite(context.Background(), nil, b, w, ffmpeg.IntelGenerationPlan{}, times, 3, (tc.tiles+2)/3, tc.tiles, t.TempDir(), func(context.Context, int, []float64, string) error {
			calls++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if calls != 0 || b.HasMeasuredCapacity(w) {
			t.Fatalf("oversized first sample attempted: tiles=%d seed=%d calls=%d", tc.tiles, tc.initialLanes, calls)
		}
	}
}

func TestSpriteTuningContinuesSeedOneSearchToDemandBound(t *testing.T) {
	b, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
		return generationbudget.Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1}
	}, func(int) generationbudget.ProcessResources {
		return generationbudget.ProcessResources{Memory: 64 << 20, GPU: 64 << 20}
	})
	if err != nil {
		t.Fatal(err)
	}
	base := generationbudget.Workload{Key: "identified-file/same-window", MemoryPerSlot: 3 << 30, GPUPerSlot: 3 << 30}
	type trial struct{ lanes, tiles int }
	var trials []trial
	render := func(ctx context.Context, lanes int, times []float64, path string) error {
		trials = append(trials, trial{lanes, len(times)})
		generationbudget.RecordProcess(ctx, 7)
		// Every larger candidate improves for exactly the same tiles. The
		// initial one-lane cost leaves time for all six bounded first trials.
		timer := time.NewTimer(time.Duration((len(times)+lanes-1)/lanes) * 80 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		err = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 72, 72)), nil)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	g := Generator{}
	plan := ffmpeg.IntelGenerationPlan{Filter: "scale_vaapi=w=8:h=8"}
	var canonical []int
	for generation := 0; generation < 8; generation++ {
		w, finish := b.ScopeWorkload(base)
		before := len(trials)
		dir := t.TempDir()
		if err := g.tuneSprite(context.Background(), nil, b, w, plan, fullSpriteTimes(), 9, 9, 81, dir, render); err != nil {
			t.Fatal(err)
		}
		if generation == 0 && (len(trials) != 6 || b.Settings().MaxGPUProcesses != 8) {
			t.Fatal("fixture did not reach the six-trial unfinished boundary", trials, b.Settings())
		}
		if generation > 0 && len(trials) != before {
			t.Fatal("warm generation repeated partial calibration", trials[before:])
		}
		b.PrepareCanonicalTrial(w, 81, 81)
		lanes := 0
		output := filepath.Join(dir, "canonical.jpg")
		sample, err := b.Measure(context.Background(), w, func(ctx context.Context) error {
			granted, release, err := b.AcquireWorkload(ctx, w, 81)
			if err != nil {
				return err
			}
			defer release()
			lanes = granted
			return render(ctx, lanes, fullSpriteTimes(), output)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSpriteTrial(output, plan, 9, 9); err != nil {
			t.Fatal(err)
		}
		canonical = append(canonical, lanes)
		b.RecordCanonicalSample(w, sample, lanes, 81, 81)
		b.FinishTuning(w)
		finish(true)
		if b.HasMeasuredCapacity(base) {
			break
		}
	}
	if len(canonical) != 5 || canonical[0] != 8 || canonical[1] != 16 || canonical[2] != 32 || canonical[3] != 64 || canonical[4] != 81 {
		t.Fatal("finite calibration budget became a permanent concurrency cap", canonical)
	}
	if !b.HasMeasuredCapacity(base) || b.Settings().MaxGPUProcesses != 81 {
		t.Fatal("search did not settle at the real work-demand bound", b.Settings())
	}
}
