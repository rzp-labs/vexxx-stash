package transcoder

import (
	"reflect"
	"strings"
	"testing"

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
