package transcoder

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

func TestIntelSpriteCommandPreservesSeekAndDownloadsReducedFrame(t *testing.T) {
	source := ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StreamIndex: 1}
	for _, backend := range []string{"vaapi", "qsv"} {
		t.Run(backend, func(t *testing.T) {
			plan, err := ffmpeg.NewIntelGenerationPlan(ffmpeg.IntelGenerationConfig{Backend: backend, Device: "/dev/dri/renderD128"}, source, "scene.mp4", 3.125, 160, true)
			if err != nil {
				t.Fatal(err)
			}
			got := IntelSpriteScreenshot("scene.mp4", 3.125, plan)
			inputAt := -1
			for i, arg := range got {
				if arg == "-i" {
					inputAt = i
					break
				}
			}
			if inputAt < 2 || !reflect.DeepEqual(got[inputAt-2:inputAt+2], ffmpeg.Args{"-ss", "3.125", "-i", "scene.mp4"}) {
				t.Fatalf("input seek changed: %q", got)
			}
			joined := strings.Join(got, " ")
			for _, want := range []string{"-map 0:1 -an -frames:v 1", "scale_" + backend + "=w=160:h=90:format=nv12,hwdownload,format=nv12,format=bgr24", "-c:v bmp -f rawvideo -"} {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q in %q", want, joined)
				}
			}
			if strings.Contains(joined, "fps=") || strings.Contains(joined, "select=") {
				t.Fatal("Intel sprite must not change timestamp sampling")
			}
		})
	}
}

func TestIntelSpriteQSVDecoderDepthIsScopedToScreenshotInput(t *testing.T) {
	for _, codec := range []string{"h264", "hevc"} {
		for _, backend := range []string{"qsv", "vaapi"} {
			t.Run(codec+"/"+backend, func(t *testing.T) {
				source := ffmpeg.IntelSource{Codec: codec, PixelFormat: "yuv420p", Width: 1920, Height: 1080}
				plan, err := ffmpeg.NewIntelGenerationPlan(ffmpeg.IntelGenerationConfig{Backend: backend, Device: "/dev/dri/renderD128"}, source, "scene.mp4", 1.9876543209876543, 160, true)
				if err != nil {
					t.Fatal(err)
				}
				inputArgs := append(ffmpeg.Args(nil), plan.InputArgs...)
				got := IntelSpriteScreenshot("scene.mp4", 1.9876543209876543, plan)
				inputAt, depthAt, depthCount := -1, -1, 0
				for i, arg := range got {
					switch arg {
					case "-i":
						inputAt = i
					case "-async_depth":
						depthAt = i
						depthCount++
					}
				}
				if backend == "qsv" {
					if depthCount != 1 || depthAt+1 >= inputAt || got[depthAt+1] != "1" {
						t.Fatalf("QSV screenshot decoder must use depth one before its input: %q", got)
					}
				} else if depthCount != 0 {
					t.Fatalf("QSV decoder option leaked into VAAPI screenshot: %q", got)
				}
				if !reflect.DeepEqual(plan.InputArgs, inputArgs) {
					t.Fatalf("screenshot changed shared input arguments: %q", plan.InputArgs)
				}
				for _, probe := range plan.Probes {
					if strings.Contains(strings.Join(probe.Args, " "), "-async_depth") {
						t.Fatalf("screenshot decoder option leaked into shared capability probe: %q", probe.Args)
					}
				}
			})
		}
	}

	software := ScreenshotTime("scene.mp4", 1.9876543209876543, ScreenshotOptions{Width: 160, OutputType: ScreenshotOutputTypeBMP, OutputPath: "-"})
	if strings.Contains(strings.Join(software, " "), "-async_depth") {
		t.Fatalf("QSV decoder option leaked into software screenshot: %q", software)
	}
}

func TestIntelSpriteMain10SeekAndCanonicalConversion(t *testing.T) {
	source := ffmpeg.IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", Width: 320, Height: 180, StreamIndex: 0, ColorRange: "tv", ColorSpace: "bt709", ColorTransfer: "bt709", ColorPrimaries: "bt709"}
	plan, err := ffmpeg.NewIntelSpritePlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, source, "source.mkv", 1.9876543209876543, 160)
	if err != nil {
		t.Fatal(err)
	}
	got := IntelSpriteScreenshot("source.mkv", 1.9876543209876543, plan)
	joined := strings.Join(got, " ")
	for _, want := range []string{"-ss 1.9876543209876543 -i source.mkv", "-map 0:0 -an -frames:v 1", "hwdownload,format=p010le,format=yuv420p10le,scale=160:-2,format=bgr24", "-c:v bmp -f rawvideo -"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"scale_vaapi", "nv12", "fps=", "select=", "noaccurate_seek", "async_depth", "colorspace="} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("changed conversion/seek: %s", joined)
		}
	}
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable; command contract checked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	input := filepath.Join(t.TempDir(), "10bit.mkv")
	// Preserve low 10-bit values and vary every frame. Uneven PTS exercises
	// independent non-keyframe seeks without inferring cadence from declarations.
	fixture := "nullsrc=size=320x180:rate=10,format=yuv420p10le,geq=lum='64+mod(X*13+Y*7+N*11,876)':cb='64+mod(X*5+N*17,876)':cr='64+mod(Y*3+N*19,876)',setpts='(N+floor(N/3))/10/TB'"
	if out, err := exec.CommandContext(ctx, bin, "-v", "error", "-nostdin", "-f", "lavfi", "-i", fixture, "-frames:v", "30", "-fps_mode", "passthrough", "-c:v", "ffv1", "-color_range", "tv", "-colorspace", "bt709", "-color_trc", "bt709", "-color_primaries", "bt709", input).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	for _, at := range []float64{0, 0.19, 1.9876543209876543, 3.49} {
		// Project only the unavailable VAAPI decode/download onto a CPU-decoded
		// P010 surface. This proves conversion/seek parity, not B580 decoding.
		projected := IntelSpriteScreenshot(input, at, plan)
		var cpu ffmpeg.Args
		for i := 0; i < len(projected); i++ {
			switch projected[i] {
			case "-init_hw_device", "-filter_hw_device", "-hwaccel", "-hwaccel_device", "-hwaccel_output_format":
				i++
			case "-vf":
				i++
				cpu = append(cpu, "-vf", strings.Replace(projected[i], "hwdownload,format=p010le", "format=p010le", 1))
			default:
				cpu = append(cpu, projected[i])
			}
		}
		canonical := ScreenshotTime(input, at, ScreenshotOptions{Width: 160, OutputType: ScreenshotOutputTypeBMP, OutputPath: "-"})
		run := func(args ffmpeg.Args) []byte {
			cmd := exec.CommandContext(ctx, bin, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("seek %g: %v: %s", at, err, stderr.String())
			}
			return out
		}
		// Check all restored 10-bit planes too: a BMP match alone can hide
		// damage to low bits during the P010 layout conversion.
		raw := ffmpeg.Args{"-v", "error", "-nostdin"}.Seek(at).Input(input)
		raw = append(raw, "-map", "0:0", "-an", "-frames:v", "1")
		planar := append(append(ffmpeg.Args{}, raw...), "-vf", "format=yuv420p10le", "-c:v", "rawvideo", "-f", "rawvideo", "-")
		roundTrip := append(append(ffmpeg.Args{}, raw...), "-vf", "format=p010le,format=yuv420p10le", "-c:v", "rawvideo", "-f", "rawvideo", "-")
		if want, candidate := run(planar), run(roundTrip); len(want) == 0 || !bytes.Equal(want, candidate) {
			t.Fatalf("seek %g: P010 layout conversion changed 10-bit source planes", at)
		}
		want, candidate := run(canonical), run(cpu)
		if len(want) == 0 || !bytes.Equal(want, candidate) {
			t.Fatalf("seek %g: P010 conversion changed canonical BMP pixels (%d/%d bytes)", at, len(want), len(candidate))
		}
	}
}

func TestIntel8BitSpriteVAAPIUsesCanonicalScale(t *testing.T) {
	source := ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 3840, Height: 2160, StreamIndex: 0}
	p, err := ffmpeg.NewIntelSpritePlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, source, "six-audio.mp4", 98.85243025925925, 160)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(IntelSpriteScreenshot("six-audio.mp4", 98.85243025925925, p), " ")
	for _, want := range []string{"-ss 98.85243025925925 -i six-audio.mp4", "-map 0:0 -an -frames:v 1", "hwdownload,format=nv12,format=yuv420p,scale=160:-2,format=bgr24", "-c:v bmp -f rawvideo -"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "scale_vaapi") || strings.Contains(args, "fps=") {
		t.Fatal(args)
	}
}
