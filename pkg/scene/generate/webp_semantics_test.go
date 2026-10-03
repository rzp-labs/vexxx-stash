package generate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
)

// CT102 found that libwebp's default preset resets both lossless and method.
// Capture the real generation command to guard both callers and VR filtering.
func TestAnimatedWebPExplicitEncodingSemantics(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.1'; exit 0; fi\nfor last do :; done\nprintf '%s\\n' \"$@\" > \"$last\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	g := Generator{Encoder: ffmpeg.NewEncoder(binary), FFMpegConfig: webpTestConfig{}}
	cases := []struct {
		name   string
		fn     generateFn
		vf     string
		marker bool
	}{
		{"scene", g.previewVideoToImage("input.mp4"), "scale=640:-2,fps=12", false},
		{"marker", g.sceneMarkerWebp("input.mp4", sceneMarkerOptions{Seconds: 3.25}), "scale=640:-2,fps=12", true},
	}
	vrFilters := map[string]string{
		"LR180":      "v360=input=hequirect:output=flat:in_stereo=sbs:out_stereo=2d:d_fov=120:w=1280:h=720",
		"TB360":      "v360=input=equirect:output=flat:in_stereo=tb:out_stereo=2d:d_fov=120:w=1280:h=720",
		"MONO360":    "v360=input=equirect:output=flat:in_stereo=2d:out_stereo=2d:d_fov=120:w=1280:h=720",
		"FISHEYE190": "v360=input=fisheye:ih_fov=190:iv_fov=190:in_stereo=sbs:out_stereo=2d:output=flat:d_fov=120:w=1280:h=720",
	}
	for vr, vf := range vrFilters {
		cases = append(cases, struct {
			name   string
			fn     generateFn
			vf     string
			marker bool
		}{vr, g.sceneMarkerWebp("input.mp4", sceneMarkerOptions{Seconds: 3.25, VRMode: vr}), vf + ",scale=640:-2,fps=12", true})
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			output := filepath.Join(dir, fmt.Sprintf("args-%d", i))
			if err := c.fn(&fsutil.LockContext{Context: context.Background()}, output); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			values := map[string]string{}
			for i := 0; i+1 < len(args); i++ {
				if strings.HasPrefix(args[i], "-") {
					values[args[i]] = args[i+1]
				}
			}
			for key, want := range map[string]string{"-c:v": "libwebp", "-preset": "none", "-lossless": "1", "-q:v": "70", "-compression_level": "6", "-loop": "0", "-vf": c.vf} {
				if values[key] != want {
					t.Fatalf("%s=%q want%q args%v", key, values[key], want, args)
				}
			}
			if c.marker && (values["-ss"] != "3.25" || values["-t"] != "5") {
				t.Fatalf("timing changed: %v", args)
			}
		})
	}
}
