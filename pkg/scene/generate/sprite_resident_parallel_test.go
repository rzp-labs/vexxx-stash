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
