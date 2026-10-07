package generate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

type residentSpriteCommand struct {
	Args  []string
	Seeks []string
}

// This subprocess fixture observes the production sheet command and holds its
// admission until released. It does not claim to simulate a hardware driver.
func TestResidentSpriteFFmpegHelper(t *testing.T) {
	dir := os.Getenv("VEXXX_TEST_RESIDENT_SPRITE")
	if dir == "" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(20)
	}
	output := args[len(args)-1]
	job := filepath.Base(filepath.Dir(output))
	record := residentSpriteCommand{Args: args}
	for i, arg := range args {
		if arg == "-i" && i+1 < len(args) {
			data, err := os.ReadFile(args[i+1])
			if err != nil {
				os.Exit(21)
			}
			record.Seeks = append(record.Seeks, string(data))
		}
	}
	data, _ := json.Marshal(record)
	if history, err := os.OpenFile(filepath.Join(dir, "history-"+job+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600); err == nil {
		_, _ = history.Write(append(data, '\n'))
		_ = history.Close()
	}
	first, firstErr := os.OpenFile(filepath.Join(dir, "first-"+job+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if firstErr == nil {
		_, _ = first.Write(data)
		_ = first.Close()
	}
	start := filepath.Join(dir, "start-"+job+".json")
	if err := os.WriteFile(start+".tmp", data, 0600); err != nil {
		os.Exit(22)
	}
	if err := os.Rename(start+".tmp", start); err != nil {
		os.Exit(23)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "release-"+job)); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if delay, err := os.ReadFile(filepath.Join(dir, "timing-"+job)); err == nil {
		var milliseconds int
		_, _ = fmt.Sscanf(string(delay), "%d", &milliseconds)
		tiles := 0
		for _, seeks := range record.Seeks {
			tiles += strings.Count(seeks, "\ninpoint ")
		}
		time.Sleep(time.Duration((tiles+len(record.Seeks)-1)/len(record.Seeks)*milliseconds) * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "invalid-trial-"+job)); err == nil && strings.HasPrefix(filepath.Base(output), ".auto-sprite-") {
		_ = os.WriteFile(output, []byte("invalid trial JPEG"), 0600)
		os.Exit(0)
	}
	if _, err := os.Stat(filepath.Join(dir, "pressure-"+job)); err == nil && len(record.Seeks) > 1 {
		fmt.Fprintln(os.Stderr, "Cannot allocate memory")
		os.Exit(40)
	}
	if _, err := os.Stat(filepath.Join(dir, "invalid-output-"+job)); err == nil {
		_ = os.WriteFile(output, []byte("not a JPEG"), 0600)
		os.Exit(0)
	}
	if _, err := os.Stat(filepath.Join(dir, "fail-"+job)); err == nil {
		fmt.Fprintln(os.Stderr, "injected GPU sheet failure")
		os.Exit(24)
	}
	f, err := os.Create(output)
	if err != nil {
		os.Exit(25)
	}
	if err := jpeg.Encode(f, image.NewGray(image.Rect(0, 0, 1440, 720)), nil); err != nil {
		os.Exit(26)
	}
	if err := f.Close(); err != nil {
		os.Exit(27)
	}
	os.Exit(0)
}

func residentSpriteTestGenerator(t *testing.T, settings generationbudget.Settings) (Generator, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	dir := t.TempDir()
	t.Setenv("VEXXX_TEST_RESIDENT_SPRITE", dir)
	frame := intelMetadataTestRecord()
	frame["color_primaries"], frame["color_transfer"], frame["color_space"] = "bt709", "bt709", "bt709"
	metadata := string(intelMetadataTestOutput(frame))
	script := "case \" $* \" in *'-hwaccel_metadata 1'*) printf '%s\\n' '" + metadata + "';exit 0;; esac\nexec '" + os.Args[0] + "' -test.run='^TestResidentSpriteFFmpegHelper$' -- \"$@\"\n"
	g := metadataTestGenerator(t, script, settings)
	g.IntelSprites = &ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	// Only replace unavailable physical-device/capability validation. The public
	// sprite entry, metadata, planner, command, admission, cleanup and publication
	// all remain the production implementation exercised by this test.
	g.intelSpriteWork = func(ctx context.Context, plan ffmpeg.IntelGenerationPlan, hardware, software func(context.Context) error, _ ffmpeg.IntelGenerationRunner) (ffmpeg.IntelGenerationDiagnostic, error) {
		if software != nil {
			t.Error("GPU sprite supplied a software fallback")
		}
		err := hardware(ctx)
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		actual := "vaapi"
		if err != nil {
			actual = "none"
		}
		return ffmpeg.IntelGenerationDiagnostic{Selected: plan.Config.Backend, Actual: actual}, err
	}
	return g, dir
}

func residentSpriteStart(t *testing.T, ctx context.Context, dir, job string) residentSpriteCommand {
	t.Helper()
	for {
		data, err := os.ReadFile(filepath.Join(dir, "start-"+job+".json"))
		if err == nil {
			var record residentSpriteCommand
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			return record
		}
		select {
		case <-ctx.Done():
			t.Fatal("production sprite command did not start", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func residentSpriteRelease(t *testing.T, dir, job string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "release-"+job), nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func residentSpriteOutput(t *testing.T, dir, job string) string {
	t.Helper()
	path := filepath.Join(dir, job)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(path, "sprite.jpg")
}

func TestResidentSpriteEntryUsesConfiguredDecoderConcurrency(t *testing.T) {
	for _, limits := range [][2]int{{1, 1}, {12, 12}, {64, 64}, {12, 4}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) {
			g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{MaxProcesses: limits[0], MaxGPUProcesses: limits[1]})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			times := make([]float64, 81)
			for i := range times {
				times[i] = float64(i/2) + 0.125 // duplicates cross partition boundaries
			}
			output := residentSpriteOutput(t, dir, "sheet")
			done := make(chan error, 1)
			go func() { _, err := g.IntelSpriteSheet(ctx, "synthetic.mp4", times, 9, 9, output); done <- err }()
			record := residentSpriteStart(t, ctx, dir, "sheet")
			want := min(limits[0], limits[1], len(times))
			if len(record.Seeks) != want {
				t.Fatalf("public resident sprite entry started %d decoder inputs; configured %d", len(record.Seeks), want)
			}
			input, _ := filepath.Abs("synthetic.mp4")
			offset := 0
			for _, seek := range record.Seeks {
				count := strings.Count(seek, "\nfile ")
				expected, err := ffmpeg.IntelSpriteSeekList(input, ffmpeg.IntelSource{StartTime: "0"}, times[offset:offset+count])
				if err != nil || seek != expected {
					t.Fatalf("decoder partition at %d changed canonical timestamps/order", offset)
				}
				offset += count
			}
			if offset != len(times) {
				t.Fatal("sprite command omitted tiles")
			}
			command := strings.Join(record.Args, " ")
			if strings.Count(command, "-hwaccel_strict 1") != want || !strings.Contains(command, "mjpeg_vaapi -global_quality 95") {
				t.Fatal("resident sprite changed strict decoder/quality", command)
			}
			for _, bad := range []string{"hwdownload", "hwupload", "-c:v bmp", "-c:v mjpeg ", "scale="} {
				if strings.Contains(command, bad) {
					t.Fatal("CPU pixel operation in resident command", bad)
				}
			}
			residentSpriteRelease(t, dir, "sheet")
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".*"))
			if len(leftovers) != 0 {
				t.Fatal("resident sheet leaked temporary files", leftovers)
			}
		})
	}
}

func TestResidentSpriteEntriesShareAllDecoderPermits(t *testing.T) {
	g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{MaxProcesses: 6, MaxGPUProcesses: 4})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{}, 3)
	launch := make(chan struct{})
	work := g.intelSpriteWork
	g.intelSpriteWork = func(ctx context.Context, plan ffmpeg.IntelGenerationPlan, hardware, software func(context.Context) error, run ffmpeg.IntelGenerationRunner) (ffmpeg.IntelGenerationDiagnostic, error) {
		ready <- struct{}{}
		select {
		case <-launch:
		case <-ctx.Done():
			return ffmpeg.IntelGenerationDiagnostic{}, ctx.Err()
		}
		return work(ctx, plan, hardware, software, run)
	}
	done := make(chan error, 3)
	for i := range 3 {
		output := residentSpriteOutput(t, dir, fmt.Sprint(i))
		go func() {
			_, err := g.IntelSpriteSheet(ctx, "synthetic.mp4", []float64{0.125, 1.5}, 9, 9, output)
			done <- err
		}()
	}
	for range 3 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(launch)
	var active []string
	for len(active) < 2 {
		paths, _ := filepath.Glob(filepath.Join(dir, "start-*.json"))
		active = paths
		select {
		case <-ctx.Done():
			t.Fatal("configured GPU lanes did not admit two sheets", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	// Two live two-input sheets consume all four GPU slots; a third sheet
	// must wait despite each sheet being only one host subprocess.
	blocked, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	permit, err := g.Budget.Acquire(blocked, generationbudget.GPU)
	stop()
	if err == nil {
		permit()
		t.Fatal("two resident two-input sheets did not reserve all four GPU slots")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("GPU admission failed for a reason other than the shared limit", err)
	}
	active, _ = filepath.Glob(filepath.Join(dir, "start-*.json"))
	if len(active) != 2 {
		t.Fatalf("expected at most two two-input sheets within four GPU slots; observed %d", len(active))
	}
	for _, path := range active {
		job := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "start-"), ".json")
		record := residentSpriteStart(t, ctx, dir, job)
		if len(record.Seeks) != 2 {
			t.Fatal("live sheet did not use two independent decoder inputs")
		}
	}
	for i := range 3 {
		residentSpriteRelease(t, dir, fmt.Sprint(i))
	}
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestResidentSpriteEntryUsesAvailableDecoderCapacity(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		total, gpu, cpu, lanes int
	}{
		{"GPU contention", 12, 12, 0, 11},
		{"mixed total and GPU contention", 6, 4, 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{MaxProcesses: tc.total, MaxGPUProcesses: tc.gpu})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// These unrelated leaf tasks remain live until after the sheet has
			// finished. Available slots must be usable without waiting for them.
			holdGPU, err := g.Budget.Acquire(ctx, generationbudget.GPU)
			if err != nil {
				t.Fatal(err)
			}
			defer holdGPU()
			holdCPU := func() {}
			if tc.cpu > 0 {
				holdCPU, err = g.Budget.AcquireN(ctx, generationbudget.CPU, tc.cpu)
				if err != nil {
					t.Fatal(err)
				}
				defer holdCPU()
			}
			times := make([]float64, 81)
			for i := range times {
				times[i] = float64(i/2) + 0.125
			}
			output := residentSpriteOutput(t, dir, "sheet")
			done := make(chan error, 1)
			go func() { _, err := g.IntelSpriteSheet(ctx, "synthetic.mp4", times, 9, 9, output); done <- err }()
			record := residentSpriteStart(t, ctx, dir, "sheet")
			if len(record.Seeks) != tc.lanes {
				t.Fatalf("sheet started %d decoder inputs with %d available; unrelated tasks remain live", len(record.Seeks), tc.lanes)
			}
			// The sheet must account for every actual decoder. A subsequent GPU
			// task waits while the sheet and unrelated leaf tasks fill the budget.
			queued := make(chan func(), 1)
			queuedErr := make(chan error, 1)
			go func() {
				release, err := g.Budget.Acquire(ctx, generationbudget.GPU)
				if err != nil {
					queuedErr <- err
					return
				}
				queued <- release
			}()
			select {
			case release := <-queued:
				release()
				t.Fatal("resident sheet failed to reserve its actual decoder slots")
			case err := <-queuedErr:
				t.Fatal(err)
			case <-time.After(25 * time.Millisecond):
			}
			residentSpriteRelease(t, dir, "sheet")
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("sheet did not finish while unrelated tasks remained live", ctx.Err())
			}
			select {
			case release := <-queued:
				release()
			case err := <-queuedErr:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal("sheet completion did not release its actual decoder slots", ctx.Err())
			}
			holdCPU()
			holdGPU()
			fresh, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			release, err := g.Budget.AcquireN(fresh, generationbudget.GPU, tc.gpu)
			if err != nil {
				t.Fatal("partial-capacity sheet leaked decoder permits", err)
			}
			release()
		})
	}
}

func TestResidentSpriteEntryCancellationUnderContention(t *testing.T) {
	for _, mode := range []string{"waiting", "rendering"} {
		t.Run(mode, func(t *testing.T) {
			g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{MaxProcesses: 12, MaxGPUProcesses: 12})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Pause after the public entry has completed metadata and planning,
			// at the same callback boundary as the hardware capability probes.
			ready := make(chan struct{})
			launch := make(chan struct{})
			entered := make(chan struct{})
			work := g.intelSpriteWork
			g.intelSpriteWork = func(ctx context.Context, plan ffmpeg.IntelGenerationPlan, hardware, software func(context.Context) error, run ffmpeg.IntelGenerationRunner) (ffmpeg.IntelGenerationDiagnostic, error) {
				close(ready)
				select {
				case <-launch:
				case <-ctx.Done():
					return ffmpeg.IntelGenerationDiagnostic{}, ctx.Err()
				}
				close(entered)
				return work(ctx, plan, hardware, software, run)
			}
			output := residentSpriteOutput(t, dir, "sheet")
			if err := os.WriteFile(output, []byte("existing asset"), 0600); err != nil {
				t.Fatal(err)
			}
			times := make([]float64, 81)
			for i := range times {
				times[i] = float64(i)
			}
			done := make(chan error, 1)
			go func() { _, err := g.IntelSpriteSheet(ctx, "synthetic.mp4", times, 9, 9, output); done <- err }()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("sheet did not reach hardware render boundary", ctx.Err())
			}
			held := 1
			if mode == "waiting" {
				held = 12
			}
			holdGPU, err := g.Budget.AcquireN(ctx, generationbudget.GPU, held)
			if err != nil {
				t.Fatal(err)
			}
			defer holdGPU()
			close(launch)
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if mode == "rendering" {
				record := residentSpriteStart(t, ctx, dir, "sheet")
				if len(record.Seeks) != 11 {
					t.Fatalf("contended sheet used %d decoder lanes instead of 11", len(record.Seeks))
				}
			} else {
				select {
				case err := <-done:
					t.Fatal("sheet did not wait for an available decoder permit", err)
				case <-time.After(25 * time.Millisecond):
				}
				if _, err := os.Stat(filepath.Join(dir, "start-sheet.json")); !os.IsNotExist(err) {
					t.Fatal("sheet started without an available decoder permit", err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("contended sheet did not cancel explicitly", err)
				}
			case <-time.After(time.Second):
				t.Fatal("contended sheet did not stop after cancellation")
			}
			data, _ := os.ReadFile(output)
			if string(data) != "existing asset" {
				t.Fatal("cancelled resident render replaced existing asset")
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".*"))
			if len(leftovers) != 0 {
				t.Fatal("cancelled contended render leaked temporary files", leftovers)
			}
			holdGPU()
			fresh, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			release, err := g.Budget.AcquireN(fresh, generationbudget.GPU, 12)
			if err != nil {
				t.Fatal("cancelled contended render leaked decoder permits", err)
			}
			release()
		})
	}
}

func TestResidentSpriteEntryFailureCancellationAndPermitReuse(t *testing.T) {
	for _, mode := range []string{"failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{MaxProcesses: 12, MaxGPUProcesses: 12})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output := residentSpriteOutput(t, dir, "sheet")
			if err := os.WriteFile(output, []byte("existing asset"), 0600); err != nil {
				t.Fatal(err)
			}
			times := make([]float64, 81)
			for i := range times {
				times[i] = float64(i)
			}
			done := make(chan error, 1)
			go func() { _, err := g.IntelSpriteSheet(ctx, "synthetic.mp4", times, 9, 9, output); done <- err }()
			residentSpriteStart(t, ctx, dir, "sheet")
			if mode == "cancel" {
				cancel()
			} else {
				if err := os.WriteFile(filepath.Join(dir, "fail-sheet"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				residentSpriteRelease(t, dir, "sheet")
			}
			if err := <-done; err == nil || (mode == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("GPU %s did not fail explicitly: %v", mode, err)
			}
			data, _ := os.ReadFile(output)
			if string(data) != "existing asset" {
				t.Fatal("failed resident render replaced existing asset")
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".*"))
			if len(leftovers) != 0 {
				t.Fatal("failed render leaked temporary files", leftovers)
			}
			fresh, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			release, err := g.Budget.AcquireN(fresh, generationbudget.GPU, 12)
			if err != nil {
				t.Fatal("failed resident sheet leaked decoder permits", err)
			}
			release()
		})
	}
}

func TestResidentSpriteAutoRetriesOnlyExplicitResourcePressure(t *testing.T) {
	for _, tt := range []struct {
		name, flag  string
		gpu         int
		wantSuccess bool
	}{
		{"auto-pressure", "pressure", 0, true},
		{"manual-pressure", "pressure", 4, false},
		{"auto-driver-failure", "fail", 0, false},
		{"auto-invalid-output", "invalid-output", 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: tt.gpu, Threads: 1})
			budget, err := generationbudget.NewWithResources(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: tt.gpu, Threads: 1}, func() generationbudget.Resources {
				return generationbudget.Resources{CPUs: 4, MemoryAvailable: 128 << 30, GPUAvailable: 64 << 30}
			})
			if err != nil {
				t.Fatal(err)
			}
			g.Budget = budget
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output := residentSpriteOutput(t, dir, "sheet")
			if err := os.WriteFile(filepath.Join(dir, tt.flag+"-sheet"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			residentSpriteRelease(t, dir, "sheet")
			_, err = g.IntelSpriteSheet(ctx, "synthetic.mp4", []float64{0.125, 1.5, 2.5, 3.5}, 9, 9, output)
			if (err == nil) != tt.wantSuccess {
				t.Fatalf("success=%t error=%v", tt.wantSuccess, err)
			}
			record := residentSpriteStart(t, ctx, dir, "sheet")
			firstData, readErr := os.ReadFile(filepath.Join(dir, "first-sheet.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var first residentSpriteCommand
			if err := json.Unmarshal(firstData, &first); err != nil {
				t.Fatal(err)
			}
			if tt.wantSuccess {
				if len(first.Seeks) != 2 || len(record.Seeks) != 1 {
					t.Fatalf("retry did not reduce2 to1: %d -> %d", len(first.Seeks), len(record.Seeks))
				}
				if _, err := os.Stat(output); err != nil {
					t.Fatal("validated retry not published", err)
				}
			} else {
				if len(record.Seeks) != len(first.Seeks) {
					t.Fatal("non-resource/manual failure was retried")
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("failed/invalid output was published", err)
				}
				if tt.gpu == 0 && g.Budget.Settings().MaxGPUProcesses != 2 {
					t.Fatal("correctness failure taught capacity")
				}
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".*"))
			if len(leftovers) != 0 {
				t.Fatal("failed attempt leaked files", leftovers)
			}
			permit, err := g.Budget.Acquire(ctx, generationbudget.GPU)
			if err != nil {
				t.Fatal("render leaked permits", err)
			}
			permit()
		})
	}
}

func TestResidentSpriteEntrySelectsMixedAutoManualCapacity(t *testing.T) {
	for _, request := range []generationbudget.Settings{
		{}, {MaxProcesses: 4}, {MaxGPUProcesses: 4}, {MaxProcesses: 4, MaxGPUProcesses: 4},
		{Threads: 3}, {MaxProcesses: 4, Threads: 3}, {MaxGPUProcesses: 4, Threads: 3}, {MaxProcesses: 4, MaxGPUProcesses: 4, Threads: 3},
	} {
		t.Run(fmt.Sprintf("total%d-gpu%d-threads%d", request.MaxProcesses, request.MaxGPUProcesses, request.Threads), func(t *testing.T) {
			g, dir := residentSpriteTestGenerator(t, generationbudget.Settings{})
			var err error
			g.Budget, err = generationbudget.NewWithResources(request, func() generationbudget.Resources {
				return generationbudget.Resources{CPUs: 1, MemoryAvailable: 128 << 30, GPUAvailable: 64 << 30}
			})
			if err != nil {
				t.Fatal(err)
			}
			if request.MaxProcesses == 0 && request.MaxGPUProcesses == 0 && g.Budget.Settings().MaxProcesses != 1 {
				t.Fatal("fixture did not start at one CPU process")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output := residentSpriteOutput(t, dir, "sheet")
			residentSpriteRelease(t, dir, "sheet")
			times := make([]float64, 81)
			for i := range times {
				times[i] = float64(i)
			}
			_, err = g.IntelSpriteSheet(ctx, "synthetic.mp4", times, 9, 9, output)
			if err != nil {
				t.Fatal(err)
			}
			record := residentSpriteStart(t, ctx, dir, "sheet")
			if len(record.Seeks) <= 1 {
				t.Fatal("initial CPU total capped GPU trial")
			}
			if request.MaxProcesses > 0 && len(record.Seeks) > request.MaxProcesses {
				t.Fatal("GPU exceeded manual total")
			}
			if len(record.Seeks) != g.Budget.Settings().MaxGPUProcesses {
				t.Fatalf("render lanes%d differ from effective capacity%d", len(record.Seeks), g.Budget.Settings().MaxGPUProcesses)
			}
			if request.MaxGPUProcesses > 0 && len(record.Seeks) != request.MaxGPUProcesses {
				t.Fatal("manual GPU request changed")
			}
			wantThreads := request.Threads
			if wantThreads == 0 {
				wantThreads = 1
			}
			if g.Budget.Settings().Threads != wantThreads {
				t.Fatal("mixed Auto/manual threads changed")
			}
		})
	}
}
