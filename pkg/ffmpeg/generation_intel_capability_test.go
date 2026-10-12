package ffmpeg

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Successful actual-source probes, rather than codec names or a universal 4096
// ceiling, authorize rendering. Device validation is substituted only here;
// production continues to validate the explicitly selected render device.
func TestVEX101ActualCapabilityProbesContinueStrictHardwareWork(t *testing.T) {
	for _, tc := range []struct {
		codec, profile string
		width, height  int
	}{
		{"wmv3", "Main", 1920, 1080},
		{"vc1", "Advanced", 1920, 1080},
		{"h264", "High", 1920, 1080},
		{"h264", "High", 5760, 2880},
	} {
		t.Run(fmt.Sprintf("%s-%dx%d", tc.codec, tc.width, tc.height), func(t *testing.T) {
			source := IntelSource{Codec: tc.codec, Profile: tc.profile, Width: tc.width, Height: tc.height, PixelFormat: "yuv420p", BitDepth: 8, ColorRange: "tv", ColorSpace: "bt709", ColorPrimaries: "bt709", ColorTransfer: "bt709", SampleAspectRatio: "1:1"}
			config := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
			plan, err := NewIntelPreviewPlan(config, source, "synthetic", 0, 640)
			if err != nil {
				t.Fatal("header blacklist overruled possible actual capability", err)
			}
			var stages []string
			run := func(_ context.Context, args Args) error {
				joined := strings.Join(args, " ")
				for _, required := range []string{"-hwaccel vaapi", "-hwaccel_output_format vaapi", "-hwaccel_strict 1", "-abort_on empty_output", "-frames:v 1", "-map 0:0"} {
					if !strings.Contains(joined, required) {
						t.Fatalf("probe lost strict actual-source evidence (%s): %s", required, joined)
					}
				}
				stages = append(stages, plan.Probes[len(stages)].Stage)
				return nil
			}
			deviceChecked, rendered := false, false
			d, err := runIntelGenerationWork(context.Background(), plan, func(context.Context) error {
				if !deviceChecked || !reflect.DeepEqual(stages, []string{"decode", "filter", "encode"}) {
					t.Fatal("render bypassed device/actual-source capability evidence", stages)
				}
				rendered = true
				return nil
			}, nil, run, func(device string) error {
				deviceChecked = device == config.Device
				return nil
			})
			if err != nil || !rendered || d.Selected != "vaapi" || d.Actual != "vaapi" {
				t.Fatalf("supported input did not continue as strict hardware: %+v %v", d, err)
			}
		})
	}
}
