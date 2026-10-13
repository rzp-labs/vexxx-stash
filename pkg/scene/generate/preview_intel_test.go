package generate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/diagnostics"
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
	for _, part := range []string{"-hwaccel vaapi", "-hwaccel_output_format vaapi", "-ss 1.25", "-t 0.75", "-c:v h264_vaapi", "-c:a aac", "-b:a 128k", "-vsync 2", "-colorspace bt709", "-color_range tv", "-qp 21"} {
		if !strings.Contains(args, part) {
			t.Errorf("missing %q: %s", part, args)
		}
	}
	for _, part := range []string{"-map ", "libx264", "-crf", "-preset veryslow", "-r ", "fps=", "hwdownload", "hwupload", "scale="} {
		if strings.Contains(args, part) {
			t.Errorf("unexpected %q: %s", part, args)
		}
	}
	silent := strings.Join(previewIntelArgs("in", previewChunkOptions{StartTime: 1.25, Duration: 0.75, OutputPath: "out.mp4", Audio: false}, plan, false), " ")
	if !strings.Contains(silent, "-an") || strings.Contains(silent, "-map ") || strings.Contains(silent, "-c:a ") {
		t.Fatalf("silent scene preview changed native video selection or enabled audio: %s", silent)
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

func TestPreviewSingleSegmentBypassesAdaptiveMeasurement(t *testing.T) {
	g := metadataTestGenerator(t, "for last do :;done\nprintf 'video' > \"$last\"\n", generationbudget.Settings{MaxProcesses: 1})
	lock := g.LockManager.ReadLock(context.Background(), "synthetic")
	defer lock.Cancel()
	dir := t.TempDir()
	output := filepath.Join(dir, "preview.mp4")
	// A clip shorter than one full segment window renders without a capacity workload.
	if err := g.previewVideo("synthetic", 0.5, PreviewOptions{Segments: 1, SegmentDuration: 0.75}, "", false, false)(lock, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "video" {
		t.Fatalf("single-segment render did not produce the encoder output: %q %v", data, err)
	}
}

func TestPreviewConcatFailureKeepsOriginalError(t *testing.T) {
	g := metadataTestGenerator(t, "echo concatfailure >&2\nexit 23\n", generationbudget.Settings{MaxProcesses: 1})
	lock := g.LockManager.ReadLock(context.Background(), "synthetic")
	defer lock.Cancel()
	err := g.previewVideoChunkCombine(lock, filepath.Join(t.TempDir(), "concat.txt"), filepath.Join(t.TempDir(), "out.mp4"))
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 || !strings.Contains(string(exitErr.Stderr), "concatfailure") {
		t.Fatalf("software concat lost the encoder failure: %v", err)
	}
	if r, ok := diagnostics.PreviewFailureSnapshot(err); ok {
		t.Fatalf("software concat gained GPU replay facts: %+v", r)
	}
}

func TestScenePreviewDeviceRefusalRecordsActualReplayFacts(t *testing.T) {
	record := intelMetadataTestRecord()
	record["pix_fmt"] = "nv12"
	record["bit_depth"] = 8
	record["color_space"], record["color_primaries"], record["color_transfer"] = "bt709", "bt709", "bt709"
	script := "for last do :;done\nif [ \"$last\" != '-' ];then printf 'video' > \"$last\";fi\nprintf '%s\\n' '" + string(intelMetadataTestOutput(record)) + "'\n"
	g := metadataTestGenerator(t, script, generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 4})
	g.IntelPreviews = &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}
	var saved []diagnostics.PreviewReplayRecord
	ctx := diagnostics.WithPreviewSource(context.Background(), 3, 4, "", func(r diagnostics.PreviewReplayRecord) error { saved = append(saved, r); return nil })
	lock := g.LockManager.ReadLock(ctx, "synthetic")
	defer lock.Cancel()
	var diagnostic ffmpeg.IntelGenerationDiagnostic
	g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostic = d }
	err := g.scenePreviewVideo("synthetic", 10, PreviewOptions{Segments: 1, SegmentDuration: 0.75}, "", false, false)(lock, filepath.Join(t.TempDir(), "preview.mp4"))
	if err == nil {
		t.Fatal("missing render device rendered")
	}
	r, ok := diagnostics.PreviewFailureSnapshot(err)
	if !ok || r.Command.Stage != "device" || r.Command.PlanID != "intel-scene-preview-v1" || r.Command.TargetWidth == nil || *r.Command.TargetWidth != PreviewWidth || r.Command.TargetHeight == nil || *r.Command.TargetHeight != 320 {
		t.Fatalf("device refusal lost typed replay facts: %+v", r)
	}
	if r.Command.Started == nil || *r.Command.Started {
		t.Fatal("never-executed render claimed a command start", r.Command.Started)
	}
	if len(saved) != 1 || diagnostic.Stage != "device" || diagnostic.Selected != "vaapi" || diagnostic.Actual != "none" {
		t.Fatalf("device refusal misreported or software retried: %+v %+v", diagnostic, saved)
	}
}

func TestScenePreviewGPUSuccessValidatesOutput(t *testing.T) {
	if err := ffmpeg.ValidateIntelDevice("/dev/dri/renderD128"); err != nil {
		t.Skip("Mock Intel scene preview success needs an existing authorized render device; run this test in the isolated GPU lab")
	}
	p := previewTestPaths{markerTestPaths{t.TempDir()}}
	dir := t.TempDir()
	header := `{"streams":[{"codec_type":"video","codec_name":"h264","index":0,"width":1920,"height":1080,"pix_fmt":"yuv420p","duration":"10","r_frame_rate":"30/1","sample_aspect_ratio":"1:1"}],"format":{"start_time":"0"}}`
	packets := `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"30"}]}`
	probe := filepath.Join(dir, "ffprobe")
	probeScript := "#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffprobe version 8.1';exit 0;fi\ncase \" $* \" in *count_packets*) cat <<'PACKETS'\n" + packets + "\nPACKETS\n;; *) cat <<'HEADER'\n" + header + "\nHEADER\n;; esac\n"
	if err := os.WriteFile(probe, []byte(probeScript), 0700); err != nil {
		t.Fatal(err)
	}
	record := intelMetadataTestRecord()
	record["pix_fmt"] = "nv12"
	record["bit_depth"] = 8
	record["color_space"], record["color_primaries"], record["color_transfer"] = "bt709", "bt709", "bt709"
	encoder := filepath.Join(dir, "ffmpeg")
	encoderScript := "#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffmpeg version 8.1';exit 0;fi\nfor last do :;done\nif [ \"$last\" = '-' ];then\n case \" $* \" in *wrapped_avframe*) printf '%s\\n' '" + string(intelMetadataTestOutput(record)) + "';; esac\n exit 0\nfi\nprintf 'video' > \"$last\"\n"
	if err := os.WriteFile(encoder, []byte(encoderScript), 0700); err != nil {
		t.Fatal(err)
	}
	budget, err := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 4})
	if err != nil {
		t.Fatal(err)
	}
	g := Generator{Probe: ffmpeg.NewFFProbe(probe), Encoder: ffmpeg.NewEncoder(encoder), Budget: budget, LockManager: fsutil.NewReadLockManager(), ScenePaths: p,
		IntelPreviews: &ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}}
	var diagnostic ffmpeg.IntelGenerationDiagnostic
	g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostic = d }
	lock := g.LockManager.ReadLock(context.Background(), "synthetic")
	defer lock.Cancel()
	output := filepath.Join(dir, "preview.mp4")
	if err := g.scenePreviewVideo("synthetic", 10, PreviewOptions{Segments: 2, SegmentDuration: 0.75}, "", false, false)(lock, output); err != nil {
		t.Fatal("GPU preview rejected a supported synthetic pipeline", err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "video" {
		t.Fatalf("validated GPU preview did not produce the encoder output: %q %v", data, err)
	}
	if diagnostic.Actual != "vaapi" || diagnostic.Stage != "" || len(diagnostic.ProbeStages) != 3 {
		t.Fatal("successful GPU preview misreported its execution", diagnostic)
	}
}
