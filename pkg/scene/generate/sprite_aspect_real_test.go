package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"golang.org/x/image/bmp"
)

// This CPU-only regression checks the unchanged seek/scale/format contract on an
// actual H.264 source without SAR. Transfers and VAAPI encoding are projected to
// software; this is not a claim of GPU decoder/encoder pixel or quality parity.
func TestIntelUnspecifiedSARCanonicalGeometryAndPixels(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, stderr.String())
		}
		return out
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "unset-sar.mp4")
	run("-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=5:duration=6",
		"-vf", "setsar=0", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", "-pix_fmt", "yuv420p", input)
	source, err := ffmpeg.NewFFProbe(probe).IntelSpriteSource(ctx, input, "vaapi")
	if err != nil {
		t.Fatal(err)
	}
	if source.SampleAspectRatio != "" && source.SampleAspectRatio != "N/A" && source.SampleAspectRatio != "0:1" {
		t.Fatal("fixture unexpectedly acquired SAR", source.SampleAspectRatio)
	}
	if err := intelSpriteEligibility(source, "vaapi"); err != nil {
		t.Fatal("real missing-SAR metadata declined", err)
	}
	plan, err := ffmpeg.NewIntelSpritePlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, source, input, 1.125, 160)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []float64{1.125, 3.375, 5.625} {
		control := transcoder.ScreenshotTime(input, at, transcoder.ScreenshotOptions{
			OutputPath: "-", OutputType: transcoder.ScreenshotOutputTypeBMP, Width: 160})
		candidate := transcoder.IntelSpriteScreenshot(input, at, plan)
		want, got := run(control...), run(aspectCPUProjection(candidate)...)
		if !bytes.Equal(want, got) {
			t.Fatalf("canonical BMP bytes differ at timestamp %g", at)
		}
		img, err := bmp.Decode(bytes.NewReader(got))
		if err != nil || img.Bounds().Dx() != 160 || img.Bounds().Dy() != 90 {
			t.Fatalf("sprite geometry: image=%v error=%v", img, err)
		}
	}
	previewPlan, err := ffmpeg.NewIntelPreviewPlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, source, input, 1.125, 640)
	if err != nil {
		t.Fatal(err)
	}
	var artifacts [][]byte
	var geometries []string
	for i, filter := range []string{"scale=640:-2,format=yuv420p", aspectCPUFilter(previewPlan.Filter)} {
		output := filepath.Join(dir, fmt.Sprintf("preview-%d.mp4", i))
		run("-v", "error", "-nostdin", "-ss", "1.125", "-i", input, "-t", "0.6", "-an", "-vf", filter,
			"-c:v", "libx264", "-profile:v", "high", "-level:v", "4.2", "-preset", "veryfast", "-crf", "21", "-threads", "1", output)
		// Decode the lossy artifacts, so exact parity includes the projected
		// preview's filters and software encode, rather than just stream headers.
		artifacts = append(artifacts, run("-v", "error", "-i", output, "-an", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-"))
		out, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "stream=width,height,sample_aspect_ratio,display_aspect_ratio", "-of", "json", output).Output()
		if err != nil {
			t.Fatal(err)
		}
		var data struct {
			Streams []struct {
				Width, Height int
				SAR           string `json:"sample_aspect_ratio"`
				DAR           string `json:"display_aspect_ratio"`
			}
		}
		if err := json.Unmarshal(out, &data); err != nil || len(data.Streams) != 1 {
			t.Fatalf("preview metadata: %s %v", out, err)
		}
		s := data.Streams[0]
		if s.Width != 640 || s.Height != 360 {
			t.Fatal("preview dimensions changed", s)
		}
		geometries = append(geometries, fmt.Sprintf("%dx%d SAR=%s DAR=%s", s.Width, s.Height, s.SAR, s.DAR))
	}
	if !bytes.Equal(artifacts[0], artifacts[1]) || geometries[0] != geometries[1] {
		t.Fatalf("projected preview pixels/geometry differ: %v", geometries)
	}
	t.Log("preview canonical CPU projection:", geometries[0])
}

func aspectCPUFilter(filter string) string {
	var out []string
	for _, part := range strings.Split(filter, ",") {
		if part != "hwdownload" && part != "hwupload" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ",")
}

func aspectCPUProjection(args ffmpeg.Args) ffmpeg.Args {
	var out ffmpeg.Args
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-init_hw_device", "-filter_hw_device", "-hwaccel", "-hwaccel_device", "-hwaccel_output_format":
			i++
		case "-vf":
			i++
			out = append(out, "-vf", aspectCPUFilter(args[i]))
		default:
			out = append(out, args[i])
		}
	}
	return out
}
