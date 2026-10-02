// gpu-generation-lab exercises the real candidate generators using an explicitly
// supplied synthetic fixture. It does not configure hosts, network or services.
package main

import (
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
	if *input == "" || *out == "" || *duration <= 0 || *duration > 20 || *start < 0 || *tiles < 1 || *tiles > 81 {
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
		end := *start + *duration
		err = g.MarkerPreviewVideo(ctx, *input, "synthetic", *start, &end, *audio, *vr)
		output = paths.GetVideoPreviewPath("", 0)
		if err == nil {
			validation, err = validateMarker(ctx, output, math.Min(*duration, 10-*start))
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
	data, err := exec.CommandContext(ctx, "/usr/bin/ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=codec_name,pix_fmt,width,height,duration,nb_read_frames", "-of", "json", path).Output()
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
		} `json:"streams"`
	}
	if err = json.Unmarshal(data, &result); err != nil || len(result.Streams) != 1 {
		return map[string]any{"status": "failed"}, fmt.Errorf("invalid ffprobe result: %s", data)
	}
	s := result.Streams[0]
	d, _ := strconv.ParseFloat(s.Duration, 64)
	n, _ := strconv.Atoi(s.Frames)
	if s.Codec != "h264" || s.Pixel != "yuv420p" || s.Width != 640 || s.Height != 360 || d < duration-0.1 || d > duration+0.1 || n != int(duration*30) {
		return map[string]any{"status": "failed"}, fmt.Errorf("unexpected synthetic marker stream: %s", data)
	}
	return map[string]any{"status": "passed", "probe": json.RawMessage(data), "visual": "untested", "playback": "decoded all frames"}, nil
}
