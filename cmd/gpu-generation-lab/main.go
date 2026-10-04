// gpu-generation-lab exercises the real candidate generators using an explicitly
// supplied synthetic fixture. It does not configure hosts, network or services.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/scene/generate"
)

type labPaths struct{ dir string }

func (p labPaths) TempFile(pattern string) (*os.File, error) { return os.CreateTemp(p.dir, pattern) }
func (p labPaths) GetVideoPreviewPath(_ string, _ int) string {
	return filepath.Join(p.dir, "marker.mp4")
}
func (p labPaths) GetWebpPreviewPath(_ string, _ int) string {
	return filepath.Join(p.dir, "marker.webp")
}
func (p labPaths) GetScreenshotPath(_ string, _ int) string {
	return filepath.Join(p.dir, "marker.jpg")
}

type labScenePaths struct{ dir string }

func (p labScenePaths) TempFile(pattern string) (*os.File, error) {
	return os.CreateTemp(p.dir, pattern)
}
func (p labScenePaths) GetVideoPreviewPath(string) string { return filepath.Join(p.dir, "preview.mp4") }
func (p labScenePaths) GetWebpPreviewPath(string) string  { return filepath.Join(p.dir, "preview.webp") }
func (p labScenePaths) GetSpriteImageFilePath(string) string {
	return filepath.Join(p.dir, "sprite.jpg")
}
func (p labScenePaths) GetSpriteVttFilePath(string) string { return filepath.Join(p.dir, "sprite.vtt") }
func (p labScenePaths) GetTranscodePath(string) string     { return filepath.Join(p.dir, "transcode.mp4") }

func main() {
	input := flag.String("fixture", "", "explicit authorized synthetic fixture")
	out := flag.String("output-dir", "", "new result directory (never overwrite)")
	workload := flag.String("workload", "marker", "marker, webp, sprites or mixed")
	hashFixture := flag.String("phash-fixture", "", "mixed only: separate authorized 640x360 synthetic fixture")
	cancelAfter := flag.Duration("cancel-after", 0, "cancel generation after this duration (0 disables; maximum50s)")
	backend := flag.String("backend", "software", "software, qsv or vaapi")
	device := flag.String("device", "/dev/dri/renderD128", "explicit render device")
	legacyCPU := flag.Bool("legacy-cpu", false, "software baseline without candidate budget")
	vr := flag.String("vr", "", "explicit VR projection for marker/webp")
	audio := flag.Bool("audio", false, "include marker audio")
	start := flag.Float64("start", 1, "start seconds")
	duration := flag.Float64("duration", 2, "marker duration, at most20 seconds")
	tiles := flag.Int("tiles", 3, "sprite sample count1..81")
	flag.Parse()
	if *input == "" || *out == "" || !validLabTiming(*start, *duration) || *tiles < 1 || *tiles > 81 {
		fmt.Fprintln(os.Stderr, "explicit fixture/new output-dir and bounded positive duration/tile count required")
		os.Exit(2)
	}
	if *backend != "software" && *backend != "qsv" && *backend != "vaapi" {
		fmt.Fprintln(os.Stderr, "invalid backend")
		os.Exit(2)
	}
	if *cancelAfter < 0 || *cancelAfter > 50*time.Second || (*workload == "mixed" && (*legacyCPU || *hashFixture == "" || *vr != "" || *audio)) {
		fmt.Fprintln(os.Stderr, "mixed requires phash-fixture, shared budget, non-VR/no-audio fixtures; cancel-after must be0..50s")
		os.Exit(2)
	}
	if err := os.Mkdir(*out, 0750); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	if *cancelAfter > 0 {
		timer := time.AfterFunc(*cancelAfter, cancel)
		defer timer.Stop()
	}
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1, Threads: 1})
	if *legacyCPU {
		if *backend != "software" {
			fmt.Fprintln(os.Stderr, "legacy-cpu requires software backend")
			os.Exit(2)
		}
		budget = nil
	}
	paths := labPaths{*out}
	g := generate.Generator{Encoder: ffmpeg.NewEncoder("/usr/bin/ffmpeg"), Probe: ffmpeg.NewFFProbe("/usr/bin/ffprobe"), LockManager: fsutil.NewReadLockManager(), MarkerPaths: paths, ScenePaths: labScenePaths{*out}, Budget: budget}
	diagnostics := []ffmpeg.IntelGenerationDiagnostic{}
	var diagnosticMu sync.Mutex
	g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) {
		diagnosticMu.Lock()
		defer diagnosticMu.Unlock()
		diagnostics = append(diagnostics, d)
	}
	if *backend != "software" {
		cfg := &ffmpeg.IntelGenerationConfig{Backend: *backend, Device: *device, ProbeTimeout: 10 * time.Second}
		g.IntelMarker = cfg
		g.IntelSprites = cfg
	}
	var err error
	validation := map[string]any{"status": "untested"}
	var output string
	switch *workload {
	case "mixed":
		validation, err = runMixed(ctx, &g, paths, *input, *hashFixture, *out)
	case "marker":
		var expectedDuration float64
		inspect := func(ctx context.Context) error {
			var e error
			expectedDuration, e = markerExpectedDuration(ctx, "/usr/bin/ffprobe", *input, *start, *duration)
			return e
		}
		if budget != nil {
			err = budget.Run(ctx, generationbudget.CPU, inspect)
		} else {
			err = inspect(ctx)
		}
		end := *start + *duration
		if err == nil {
			err = g.MarkerPreviewVideo(ctx, *input, "synthetic", *start, &end, *audio, *vr)
		}
		output = paths.GetVideoPreviewPath("", 0)
		if err == nil {
			validation, err = validateMarker(ctx, output, expectedDuration)
		}
	case "webp":
		err = g.SceneMarkerWebp(ctx, *input, "synthetic", *start, *vr)
		output = paths.GetWebpPreviewPath("", 0)
		if err == nil {
			data, e := os.ReadFile(output)
			err = e
			if e == nil {
				validation, err = validateAnimatedWebP(data)
			}
		}
	case "sprites":
		times := make([]float64, *tiles)
		for i := range times {
			times[i] = *start + float64(i)*(*duration/float64(*tiles))
		}
		images, _, e := g.IntelSpriteTiles(ctx, *input, times)
		err = e
		if err == nil {
			if len(images) != *tiles {
				err = fmt.Errorf("sprite tile count %d, expected%d", len(images), *tiles)
				break
			}
			for _, img := range images {
				if img.Bounds().Dx() != 160 || img.Bounds().Dy() != 90 {
					err = fmt.Errorf("synthetic tile geometry %v", img.Bounds())
					break
				}
			}
			if err != nil {
				break
			}
			output = filepath.Join(*out, "sprite.jpg")
			err = g.SaveSprite(ctx, images, output)
			if err == nil && *tiles == 81 {
				err = g.SpriteVTT(ctx, filepath.Join(*out, "sprite.vtt"), output, *duration/81, *start)
			}

			validation = map[string]any{"status": "passed", "tile_count": len(images), "tile_width": 160, "tile_height": 90, "timestamps": times, "visual": "untested", "vtt": "generated for full81 only"}
		}
	default:
		err = fmt.Errorf("unknown workload")
	}
	if len(diagnostics) == 0 {
		diagnostics = append(diagnostics, ffmpeg.IntelGenerationDiagnostic{Selected: *backend, Actual: "software"})
	}
	report := map[string]any{"workload": *workload, "diagnostics": diagnostics, "output_validation": validation, "output": output}
	if *workload == "mixed" {
		// A last diagnostic from the CPU hash cannot summarize an Intel sprite
		// workload. Actual backends are attributed in output_validation.jobs.
		report["selected_backend"] = *backend
		report["actual_backend"] = "per_job"
		report["diagnostics"] = []ffmpeg.IntelGenerationDiagnostic{{Selected: *backend, Actual: "per_job", Reason: "mixed workload; inspect each job's attributed diagnostics"}}
	}
	if output != "" {
		if data, e := os.ReadFile(output); e == nil {
			digest := sha256.Sum256(data)
			report["output_sha256"] = hex.EncodeToString(digest[:])
			report["output_bytes"] = len(data)
		}
	}
	if err != nil {
		report["error"] = err.Error()
		validation["status"] = "failed"
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		os.Exit(1)
	}
}

func validateMarker(ctx context.Context, path string, duration float64) (map[string]any, error) {
	return validateMarkerWithTools(ctx, path, duration, "/usr/bin/ffprobe", "/usr/bin/ffmpeg")
}

func validateMarkerWithTools(ctx context.Context, path string, duration float64, probe, encoder string) (map[string]any, error) {
	data, err := exec.CommandContext(ctx, probe, "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=codec_name,pix_fmt,width,height,duration,nb_read_frames,avg_frame_rate", "-of", "json", path).Output()
	if err != nil {
		return map[string]any{"status": "failed"}, err
	}
	var result struct {
		Streams []struct {
			Codec    string `json:"codec_name"`
			Pixel    string `json:"pix_fmt"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Duration string `json:"duration"`
			Frames   string `json:"nb_read_frames"`
			Rate     string `json:"avg_frame_rate"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(data, &result); err != nil || len(result.Streams) != 1 {
		return map[string]any{"status": "failed"}, fmt.Errorf("invalid ffprobe result: %s", data)
	}
	s := result.Streams[0]
	d, durationErr := strconv.ParseFloat(s.Duration, 64)
	n, frameErr := strconv.Atoi(s.Frames)
	parts := strings.Split(s.Rate, "/")
	var rate float64
	if len(parts) == 2 {
		numerator, e1 := strconv.ParseFloat(parts[0], 64)
		denominator, e2 := strconv.ParseFloat(parts[1], 64)
		if e1 == nil && e2 == nil && denominator > 0 {
			rate = numerator / denominator
		}
	}
	if !validLabTiming(0, duration) || durationErr != nil || frameErr != nil || n <= 0 || math.IsNaN(d) || math.IsInf(d, 0) || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
		return map[string]any{"status": "failed"}, fmt.Errorf("invalid marker timing: %s", data)
	}
	// Use the generated stream's cadence and permit one frame of trim rounding.
	// This checks structural timing, not source-frame or VFR equivalence.
	tolerance := math.Max(0.1, 1/rate)
	if s.Codec != "h264" || s.Pixel != "yuv420p" || s.Width != 640 || s.Height != 360 || math.Abs(d-duration) > tolerance || math.Abs(float64(n)-duration*rate) > 1.000001 {
		return map[string]any{"status": "failed"}, fmt.Errorf("unexpected synthetic marker stream: %s", data)
	}
	decode := exec.CommandContext(ctx, encoder, "-v", "error", "-xerror", "-err_detect", "explode", "-threads", "1", "-i", path, "-map", "0:v:0", "-an", "-sn", "-dn", "-threads", "1", "-fps_mode", "passthrough", "-progress", "pipe:1", "-nostats", "-f", "null", "-")
	decode.WaitDelay = time.Second
	progress, err := decode.Output()
	if err != nil {
		return map[string]any{"status": "failed", "playback": "decode failed"}, fmt.Errorf("marker decode: %w", err)
	}
	decoded := -1
	complete := false
	for _, line := range strings.Split(string(progress), "\n") {
		if value, ok := strings.CutPrefix(line, "frame="); ok {
			decoded, _ = strconv.Atoi(strings.TrimSpace(value))
		}
		if line == "progress=end" {
			complete = true
		}
	}
	if !complete || decoded != n {
		return map[string]any{"status": "failed", "playback": "incomplete decode"}, fmt.Errorf("decoded%d frames, probe counted%d; complete=%t", decoded, n, complete)
	}
	return map[string]any{"status": "passed", "probe": json.RawMessage(data), "decoded_frames": decoded, "visual": "untested", "source_frame_equivalence": "untested", "playback": "decoded all frames without reported errors"}, nil
}

func validLabTiming(start, duration float64) bool {
	return !math.IsNaN(start) && !math.IsInf(start, 0) && start >= 0 && !math.IsNaN(duration) && !math.IsInf(duration, 0) && duration > 0 && duration <= 20
}

func markerExpectedDuration(ctx context.Context, probe, input string, start, duration float64) (float64, error) {
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=duration,start_time:format=start_time", "-of", "json", input)
	cmd.WaitDelay = time.Second
	data, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var result struct {
		Streams []struct {
			Duration string `json:"duration"`
			Start    string `json:"start_time"`
		} `json:"streams"`
		Format struct {
			Start string `json:"start_time"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &result); err != nil || len(result.Streams) != 1 {
		return 0, fmt.Errorf("invalid fixture duration probe")
	}
	value := result.Streams[0].Duration
	var sourceDuration float64
	if value == "" || value == "N/A" {
		// Container duration may end with a longer audio track. Establish the
		// selected video endpoint from packets instead; never infer it from audio.
		origin := result.Format.Start
		if origin == "" || origin == "N/A" {
			origin = result.Streams[0].Start
		}
		sourceDuration, err = markerVideoPacketDuration(ctx, probe, input, origin)
	} else {
		sourceDuration, err = strconv.ParseFloat(value, 64)
	}
	if err != nil {
		return 0, fmt.Errorf("fixture video duration: %w", err)
	}
	if math.IsNaN(sourceDuration) || math.IsInf(sourceDuration, 0) || sourceDuration <= start || !validLabTiming(start, duration) {
		return 0, fmt.Errorf("fixture requires finite duration beyond start")
	}
	return math.Min(duration, sourceDuration-start), nil
}

// A packet scan remains cancellable and cannot retain more than 1MiB of probe
// evidence. Inputs exceeding this lab bound fail without acceptance evidence.
type markerPacketBuffer struct{ data bytes.Buffer }

func (b *markerPacketBuffer) Write(data []byte) (int, error) {
	if len(data) > (1<<20)-b.data.Len() {
		return 0, fmt.Errorf("video packet evidence exceeds1MiB lab bound")
	}
	return b.data.Write(data)
}

func markerVideoPacketDuration(ctx context.Context, probe, input, origin string) (float64, error) {
	start, err := strconv.ParseFloat(origin, 64)
	if err != nil || math.IsNaN(start) || math.IsInf(start, 0) {
		return 0, fmt.Errorf("video packet duration requires a finite timestamp origin")
	}
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-select_streams", "v:0", "-show_packets", "-show_entries", "packet=pts_time,duration_time", "-of", "json", input)
	cmd.WaitDelay = time.Second
	var output markerPacketBuffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("video endpoint packet probe: %w", err)
	}
	var result struct {
		Packets []struct {
			PTS      string `json:"pts_time"`
			Duration string `json:"duration_time"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(output.data.Bytes(), &result); err != nil || len(result.Packets) == 0 {
		return 0, fmt.Errorf("missing video packet endpoint evidence")
	}
	end := math.Inf(-1)
	for _, packet := range result.Packets {
		pts, e1 := strconv.ParseFloat(packet.PTS, 64)
		duration, e2 := strconv.ParseFloat(packet.Duration, 64)
		if e1 != nil || e2 != nil || math.IsNaN(pts) || math.IsInf(pts, 0) || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || math.IsInf(pts+duration, 0) {
			return 0, fmt.Errorf("invalid video packet endpoint evidence")
		}
		end = math.Max(end, pts+duration)
	}
	if end <= start {
		return 0, fmt.Errorf("video packet endpoint does not follow timestamp origin")
	}
	return end - start, nil
}
