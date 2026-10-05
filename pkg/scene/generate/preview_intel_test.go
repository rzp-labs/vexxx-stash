package generate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

type previewTestPaths struct{ markerTestPaths }

func (p previewTestPaths) GetVideoPreviewPath(string) string {
	return filepath.Join(p.dir, "preview.mp4")
}
func (p previewTestPaths) GetWebpPreviewPath(string) string {
	return filepath.Join(p.dir, "preview.webp")
}
func (p previewTestPaths) GetSpriteImageFilePath(string) string {
	return filepath.Join(p.dir, "sprite.jpg")
}
func (p previewTestPaths) GetSpriteVttFilePath(string) string {
	return filepath.Join(p.dir, "sprite.vtt")
}
func (p previewTestPaths) GetTranscodePath(string) string {
	return filepath.Join(p.dir, "transcode.mp4")
}

func TestPreviewChunksShareConfiguredBudgetAcrossScenes(t *testing.T) {
	// More than two simultaneous GPU segments is intentional: reject hidden tiny caps.
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 5, MaxGPUProcesses: 4})
	cpuRelease, _ := budget.Acquire(context.Background(), generationbudget.CPU)
	defer cpuRelease()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{}, 16)
	drain := make(chan struct{})
	var active, peak atomic.Int32
	run := func(ctx context.Context, i int) error {
		release, err := budget.Acquire(ctx, generationbudget.GPU)
		if err != nil {
			return err
		}
		defer release()
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-drain:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for scene := 0; scene < 2; scene++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- runPreviewChunks(ctx, 8, 4, run) }()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("configured four GPU slots did not fill")
		}
	}
	if active.Load() != 4 {
		t.Fatal("shared budget did not bound concurrent scenes", active.Load())
	}
	close(drain)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() != 4 {
		t.Fatal("GPU/total limit exceeded", peak.Load())
	}
	cpuRelease()
	release, err := budget.Acquire(ctx, generationbudget.GPU)
	if err != nil {
		t.Fatal("permit leak", err)
	}
	release()
}

func TestPreviewChunksDrainAndKeepOriginalFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := make(chan struct{})
	want := errors.New("encode failure")
	var alive atomic.Int32
	err := runPreviewChunks(ctx, 4, 2, func(ctx context.Context, i int) error {
		alive.Add(1)
		defer alive.Add(-1)
		if i == 0 {
			<-ready
			return want
		}
		close(ready)
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, want) || errors.Is(err, context.Canceled) || alive.Load() != 0 {
		t.Fatalf("failure/drain %v active=%d", err, alive.Load())
	}
}

func TestScenePreviewHardwareArgumentsPreserveContract(t *testing.T) {
	s := ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StreamIndex: 2, SampleAspectRatio: "1:1", ColorSpace: "bt709", ColorRange: "tv"}
	plan, err := ffmpeg.NewIntelPreviewPlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, s, "in", 1.25, 640)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(previewIntelArgs("in", previewChunkOptions{StartTime: 1.25, Duration: 0.75, OutputPath: "out.mp4", Audio: true, Preset: "veryslow"}, plan, true), " ")
	for _, part := range []string{"-hwaccel vaapi", "-hwaccel_output_format vaapi", "-ss 1.25", "-t 0.75", "-c:v h264_vaapi", "-c:a aac", "-b:a 128k", "-map 0:2", "-map 0:a:0?", "-vsync 2", "-colorspace bt709", "-color_range tv", "-qp 21"} {
		if !strings.Contains(args, part) {
			t.Errorf("missing %q: %s", part, args)
		}
	}
	for _, part := range []string{"libx264", "-crf", "-preset veryslow", "-r ", "fps=", "hwdownload", "hwupload", "scale="} {
		if strings.Contains(args, part) {
			t.Errorf("unexpected %q: %s", part, args)
		}
	}
}

func TestIntelSceneUnsupportedSourceNeverFallsBackAndPreservesExisting(t *testing.T) {
	p := previewTestPaths{markerTestPaths{t.TempDir()}}
	output := p.GetVideoPreviewPath("")
	if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "ffmpeg")
	counter := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.1';exit 0;fi\nprintf x >> '" + counter + "'\nfor last do :;done\nprintf header > \"$last\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffprobe version 7.1';exit 0;fi\nprintf '%s' '{\"streams\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := Generator{Encoder: ffmpeg.NewEncoder(binary), Probe: ffmpeg.NewFFProbe(probe), LockManager: fsutil.NewReadLockManager(), ScenePaths: p, Overwrite: true, Budget: budget, IntelPreviews: &ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD99999"}}
	err := g.PreviewVideo(context.Background(), "input", 1, "hash", PreviewOptions{Segments: 2, SegmentDuration: 1}, "", false, false)
	if err == nil {
		t.Fatal("unsupported source published")
	}
	data, _ := os.ReadFile(output)
	if string(data) != "existing" {
		t.Fatalf("existing artifact replaced %q", data)
	}
	data, _ = os.ReadFile(counter)
	if len(data) != 0 {
		t.Fatalf("CPU fallback invoked %q", data)
	}
	entries, _ := os.ReadDir(p.dir)
	if len(entries) != 1 {
		t.Fatal("temporary files leaked", entries)
	}
}

func TestScenePreviewConcurrentChunksConcatInSourceOrder(t *testing.T) {
	p := previewTestPaths{markerTestPaths{t.TempDir()}}
	binary := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
if [ "$1" = '-version' ];then echo 'ffmpeg version 7.1';exit 0;fi
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
 # Force the first chunk to finish after later chunks.
 if [ "$seek" = '0' ];then sleep 0.1;fi
 printf '%s\n' "$seek" > "$output"
fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 4})
	plan := ffmpeg.IntelGenerationPlan{Filter: "dummy", InputArgs: ffmpeg.Args{"-hwaccel", "vaapi"}}
	g := Generator{Encoder: ffmpeg.NewEncoder(binary), LockManager: fsutil.NewReadLockManager(), ScenePaths: p, Budget: budget, previewIntelPlan: &plan}
	done := make(chan struct{})
	lock := g.LockManager.ReadLockWithCompletion(context.Background(), "input", done)
	defer lock.Cancel()
	defer close(done)
	output := p.GetVideoPreviewPath("")
	if err := g.previewVideo("input", 12, PreviewOptions{Segments: 4, SegmentDuration: 0.75}, "", false, false)(lock, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "0\n3\n6\n9\n" {
		t.Fatalf("concat followed completion order: %q %v", data, err)
	}
	entries, _ := os.ReadDir(p.dir)
	if len(entries) != 1 {
		t.Fatal("chunk/concat temp files leaked", entries)
	}
}

func TestGPUAnimatedWebPRejectedWithoutCPUEncoder(t *testing.T) {
	for _, asset := range []string{"scene", "marker"} {
		t.Run(asset, func(t *testing.T) {
			p := previewTestPaths{markerTestPaths{t.TempDir()}}
			config := &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}
			g := Generator{LockManager: fsutil.NewReadLockManager(), ScenePaths: p, MarkerPaths: p.markerTestPaths,
				IntelPreviews: config, IntelMarker: config}
			var diagnostics []ffmpeg.IntelGenerationDiagnostic
			g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }
			var err error
			if asset == "scene" {
				err = g.PreviewWebp(context.Background(), "input", "hash")
			} else {
				err = g.SceneMarkerWebp(context.Background(), "input", "hash", 1.25, "")
			}
			// A software retry would panic because no encoder is supplied.
			if err == nil || !strings.Contains(err.Error(), "lossless animated WebP") {
				t.Fatalf("unsupported GPU format accepted: %v", err)
			}
			if len(diagnostics) != 1 || diagnostics[0].Actual != "none" || diagnostics[0].Stage != "webp" {
				t.Fatal(diagnostics)
			}
			if entries, _ := os.ReadDir(p.dir); len(entries) != 0 {
				t.Fatal("temporary output leaked", entries)
			}
		})
	}
}

func TestGPUMarkerStillUnsupportedSourceDoesNotEncodeOnCPU(t *testing.T) {
	p := markerTestPaths{t.TempDir()}
	g := Generator{LockManager: fsutil.NewReadLockManager(), MarkerPaths: p, IntelMarker: &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}}
	for _, vr := range []string{"", "MONO360"} {
		if err := g.SceneMarkerScreenshot(context.Background(), "input", "hash", 1.25, 640, vr); err == nil {
			t.Fatal("unsupported GPU still accepted")
		}
		if entries, _ := os.ReadDir(p.dir); len(entries) != 0 {
			t.Fatal("temporary output leaked", entries)
		}
	}
}
