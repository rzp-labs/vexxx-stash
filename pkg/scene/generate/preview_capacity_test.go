package generate

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// Observe the production chunk/concat/admission paths with subprocess fixtures.
// These are control-flow tests, not hardware or encoded-media quality evidence.
func TestAutoPreviewAdjustsDuringFirstJobAndKeepsEveryChunkInOrder(t *testing.T) {
	for _, validPackets := range []bool{true, false} {
		t.Run(strconv.FormatBool(validPackets), func(t *testing.T) {
			paths := previewTestPaths{markerTestPaths{t.TempDir()}}
			binary := filepath.Join(t.TempDir(), "ffmpeg")
			script := `#!/bin/sh
if [ "$1" = '-version' ];then echo 'ffmpeg version 8.1';exit 0;fi
seek=0;mode=;input=;prev=
for arg do
 if [ "$prev" = '-ss' ];then seek="$arg";fi
 if [ "$prev" = '-f' ];then mode="$arg";fi
 if [ "$prev" = '-i' ];then input="$arg";fi
 prev="$arg"
done
output="$prev"
if [ "$mode" = 'concat' ];then
 : > "$output"
 while read label chunk;do
  chunk=$(printf '%s' "$chunk" | tr -d "'")
  cat "$(dirname "$input")/$chunk" >> "$output"
 done < "$input"
else
 case " $* " in *'-hwaccel vaapi'*'-hwaccel_strict 1'*'-c:v h264_vaapi'*) ;; *) exit 44;; esac
 sleep 0.12
 printf '%s\n' "$seek" > "$output"
fi
`
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			probe := filepath.Join(t.TempDir(), "ffprobe")
			packets := "1"
			if !validPackets {
				packets = "0"
			}
			probeScript := "#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffprobe version 8.1';exit 0;fi\nprintf '%s' '{\"streams\":[{\"codec_type\":\"video\",\"codec_name\":\"h264\",\"nb_read_packets\":\"" + packets + "\"}]}'\n"
			if err := os.WriteFile(probe, []byte(probeScript), 0700); err != nil {
				t.Fatal(err)
			}
			budget, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
				return generationbudget.Resources{CPUs: 4, MemoryAvailable: 6 << 30, GPUAvailable: -1}
			}, func(int) generationbudget.ProcessResources {
				return generationbudget.ProcessResources{Memory: 32 << 20, GPU: 64 << 20}
			})
			if err != nil {
				t.Fatal(err)
			}
			source := ffmpeg.IntelSource{Codec: "hevc", Profile: "Main 10", BitDepth: 10, PixelFormat: "p010le", Width: 8192, Height: 4096, StreamIndex: 0, SampleAspectRatio: "1:1", ColorSpace: "bt709", ColorRange: "tv", RuntimeFingerprint: "fixture-runtime"}
			plan, err := ffmpeg.NewIntelPreviewPlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, source, "input", 0, 640)
			if err != nil {
				t.Fatal(err)
			}
			g := Generator{Encoder: ffmpeg.NewEncoder(binary), Probe: ffmpeg.NewFFProbe(probe), LockManager: fsutil.NewReadLockManager(), ScenePaths: paths, Budget: budget, previewIntelPlan: &plan}
			g = g.withGenerationWorkload(plan, "scene-preview", "input")
			if budget.Settings().MaxGPUProcesses != 1 {
				t.Fatal("fixture did not start with oversized estimate")
			}
			done := make(chan struct{})
			lock := g.LockManager.ReadLockWithCompletion(context.Background(), "input", done)
			defer lock.Cancel()
			defer close(done)
			output := paths.GetVideoPreviewPath("")
			if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			err = g.previewVideo("input", 150, PreviewOptions{Segments: 15, SegmentDuration: 0.75}, "", false, false)(lock, output)
			g.finishCapacityWork(lock, err)
			data, _ := os.ReadFile(output)
			if validPackets {
				if err != nil {
					t.Fatal(err)
				}
				var expected strings.Builder
				for i := 0; i < 15; i++ {
					expected.WriteString(strconv.Itoa(i*10) + "\n")
				}
				if string(data) != expected.String() {
					t.Fatal("adaptation omitted/reordered requested chunks", string(data))
				}
				if budget.Settings().MaxGPUProcesses != 2 || !budget.HasMeasuredCapacity(*g.capacityWorkload) || budget.HasCanonicalSample(*g.capacityWorkload, 15) {
					t.Fatalf("cold preview did not retain its exercised count: settings=%+v observation=%+v", budget.Settings(), g.capacityObservation)
				}
			} else {
				if err == nil || string(data) != "existing" || budget.Settings().MaxGPUProcesses != 1 {
					t.Fatal("invalid chunk published or taught capacity", err, string(data), budget.Settings())
				}
			}
			if entries, _ := os.ReadDir(paths.dir); len(entries) != 1 {
				t.Fatal("chunk/concat temporaries leaked", entries)
			}
		})
	}
}

func TestWarmAutoPreviewReplacesFinishedChunkBeforeSlowChunkDrains(t *testing.T) {
	budget, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
		return generationbudget.Resources{CPUs: 4, MemoryAvailable: 1 << 30, GPUAvailable: -1}
	}, func(int) generationbudget.ProcessResources {
		return generationbudget.ProcessResources{Memory: 32 << 20, GPU: 32 << 20}
	})
	if err != nil {
		t.Fatal(err)
	}
	w := generationbudget.Workload{Key: "runtime/preview/window", MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
	budget.PrepareWorkload(w)
	budget.RetainTestedCapacity(w, generationbudget.Sample{Elapsed: time.Second, MemoryPeak: 64 << 20, GPUPeak: 64 << 20, MemoryKnown: true, GPUKnown: true, Isolated: true, Processes: 2}, 2, 4)
	if !budget.HasMeasuredCapacity(w) || budget.PrepareWorkload(w) != 2 {
		t.Fatal("warm fixture did not retain two exercised slots")
	}
	probe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s' '{\"streams\":[{\"codec_type\":\"video\",\"codec_name\":\"h264\",\"nb_read_packets\":\"1\"}]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	chunks := make([]previewChunkOptions, 4)
	for i := range chunks {
		chunks[i].OutputPath = filepath.Join(t.TempDir(), "chunk.mp4")
		if err := os.WriteFile(chunks[i].OutputPath, []byte("valid fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := &capacityObservation{}
	g := Generator{Budget: budget, Probe: ffmpeg.NewFFProbe(probe), capacityWorkload: &w, capacityObservation: state}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	slowStarted, releaseSlow, replacement := make(chan struct{}), make(chan struct{}), make(chan struct{})
	run := func(ctx context.Context, i int) error {
		lanes, release, err := budget.AcquireWorkload(ctx, w, 1)
		if err != nil {
			return err
		}
		state.mu.Lock()
		state.active += lanes
		state.lanes = max(state.lanes, state.active)
		state.mu.Unlock()
		defer func() {
			state.mu.Lock()
			state.active -= lanes
			state.units++
			state.mu.Unlock()
			release()
		}()
		if i == 0 {
			close(slowStarted)
			select {
			case <-releaseSlow:
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			select {
			case <-slowStarted:
			case <-ctx.Done():
				return ctx.Err()
			}
			if i == 2 {
				close(replacement)
			}
		}
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- g.runAdaptivePreviewChunks(ctx, chunks, run) }()
	select {
	case <-replacement:
		// Chunk zero is still blocked: the freed worker filled its next slot.
		close(releaseSlow)
	case <-ctx.Done():
		cancel()
		<-result
		t.Fatal("completed chunk waited for a drained batch before admitting its replacement")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if state.lanes != 2 || state.units != 4 || state.coldPreview || state.canonicalUnits != 4 {
		t.Fatalf("rolling plan lost work or changed its tested count: %+v", state)
	}
	// Every slot is returned, including the initially blocked child.
	lanes, release, err := budget.AcquireWorkload(context.Background(), w, 2)
	if err != nil || lanes != 2 {
		t.Fatal("rolling pool leaked permits", lanes, err)
	}
	release()
}

func TestMeasuredFileIdentityKeepsEqualGeometryCodecsAndFilesSeparate(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.mp4"), filepath.Join(dir, "b.mp4")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan := ffmpeg.IntelGenerationPlan{RuntimeFingerprint: "runtime", InputSource: ffmpeg.IntelSource{Codec: "av1", Profile: "Main", PixelFormat: "p010le", BitDepth: 10, Width: 8000, Height: 4000}}
	first := generationFileWorkload(plan, "sprite/window-A", a)
	if first.Key == generationFileWorkload(plan, "sprite/window-A", b).Key || strings.Contains(first.Key, a) {
		t.Fatal("different equal-format files shared private/measurement identity")
	}
	plan.InputSource.Codec, plan.InputSource.Profile = "hevc", "Main 10"
	if first.Key == generationFileWorkload(plan, "sprite/window-A", a).Key {
		t.Fatal("AV1 learning crossed into HEVC")
	}
	if err := os.WriteFile(a, []byte("replaced-file"), 0600); err != nil {
		t.Fatal(err)
	}
	plan.InputSource.Codec, plan.InputSource.Profile = "av1", "Main"
	if first.Key == generationFileWorkload(plan, "sprite/window-A", a).Key {
		t.Fatal("replaced file retained evidence")
	}
}

func TestSpriteLearningDoesNotChangeFreshPreviewAdmission(t *testing.T) {
	resources := func() generationbudget.Resources {
		return generationbudget.Resources{CPUs: 20, MemoryAvailable: 76 << 30, GPUAvailable: -1}
	}
	newBudget := func() *generationbudget.Budget {
		b, err := generationbudget.NewWithResources(generationbudget.Settings{}, resources, nil)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	plan := ffmpeg.IntelGenerationPlan{
		RuntimeFingerprint: "actual-runtime",
		Config:             ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"},
		InputSource:        ffmpeg.IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "p010le", BitDepth: 10, Width: 8192, Height: 4096},
		Filter:             "scale_vaapi=w=160:h=80",
	}
	sprite := generationFileWorkload(plan, "sprite/seeks/9x9/81/canonical-seek-hash", "same-input")
	plan.Filter = "scale_vaapi=w=640:h=320"
	preview := generationFileWorkload(plan, "scene-preview/15/1/3465.078283/229.00521886666668/30/true/false", "same-input")
	if sprite.Key == preview.Key {
		t.Fatal("sprite and preview shared a workload key")
	}
	b := newBudget()
	scoped, cleanup := b.ScopeWorkload(sprite)
	b.PrepareWorkload(scoped)
	b.RetainTestedCapacity(scoped, generationbudget.Sample{Elapsed: time.Second, MemoryPeak: 128 << 20, GPUPeak: 256 << 20, MemoryKnown: true, GPUKnown: true, Isolated: true, Processes: 1}, 6, 81)
	cleanup(true)
	if b.Settings().MaxGPUProcesses != 6 {
		t.Fatal("sprite fixture did not retain its exercised count", b.Settings())
	}
	freshCount := newBudget().PrepareWorkload(preview)
	previewScoped, previewCleanup := b.ScopeWorkload(preview)
	defer previewCleanup(false)
	if count := b.PrepareWorkload(previewScoped); count != freshCount || count != 3 || b.HasMeasuredCapacity(previewScoped) {
		t.Fatal("sprite evidence changed new preview controller", count, freshCount)
	}
	lanes, release, err := b.AcquireWorkload(context.Background(), previewScoped, 15)
	if err != nil || lanes != freshCount {
		t.Fatal("shared GPU ceiling bypassed fresh preview leaf capacity", lanes, err)
	}
	release()
}

func TestContendedWholePreviewDoesNotRefineUncomparedTrial(t *testing.T) {
	b, err := generationbudget.NewWithResources(generationbudget.Settings{}, func() generationbudget.Resources {
		return generationbudget.Resources{CPUs: 4, MemoryAvailable: 1 << 30, GPUAvailable: -1}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := generationbudget.Workload{Key: "preview/steady-window", MemoryPerSlot: 128 << 20, GPUPerSlot: 128 << 20}
	b.PrepareWorkload(w)
	s := generationbudget.Sample{Elapsed: time.Second, MemoryPeak: 64 << 20, GPUPeak: 64 << 20, MemoryKnown: true, GPUKnown: true, Isolated: true, Processes: 2}
	b.RecordCanonicalSample(w, s, 2, 4, 4)
	b.FinishTuning(w)
	if count, _ := b.PrepareCanonicalTrial(w, 4, 4); count != 2 || b.PrepareWorkload(w) != 4 {
		t.Fatal("fixture did not select a four-slot whole-window trial")
	}
	s.Isolated = false
	g := Generator{Budget: b, capacityWorkload: &w, capacityObservation: &capacityObservation{
		lanes: 4, canonicalUnits: 4, resourceSample: s, started: time.Now(),
	}}
	g.finishCapacityWork(context.Background(), nil)
	// Contention says nothing about whether four slots are slower. Keep the
	// prior two-slot reference and its pending four-slot trial, rather than
	// recording a rejection and refining to an unmeasured three-slot trial.
	if !b.HasCanonicalSample(w, 4) || b.PrepareWorkload(w) != 2 {
		t.Fatal("contended window replaced the validated reference")
	}
	if count, _ := b.PrepareCanonicalTrial(w, 4, 4); count != 2 || b.PrepareWorkload(w) != 4 {
		t.Fatal("contended window refined an uncompared trial")
	}
}
