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
