package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntelCanonicalScaleAspectEligibility(t *testing.T) {
	for _, c := range []struct {
		sar, dar string
		want     bool
	}{
		{"1:1", "16:9", true}, {"1/1", "", true}, {"1", "", true},
		{"", "", true}, {"N/A", "N/A", true}, {"0:1", "", true}, {"0/1", "0:1", true},
		{"", "16:9", true}, {"N/A", "16/9", true},
		{"", "4:3", false}, {"0:1", "malformed", false}, {"", "0:0", false},
		{"4:3", "64:27", false}, {"16:15", "", false},
		{"0:0", "", false}, {"1:0", "", false}, {"unknown", "", false},
	} {
		t.Run(fmt.Sprintf("sar_%s_dar_%s", c.sar, c.dar), func(t *testing.T) {
			s := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080,
				SampleAspectRatio: c.sar, DisplayAspectRatio: c.dar}
			if got := s.HasSquareOrUnspecifiedSampleAspectRatio(); got != c.want {
				t.Fatalf("aspect eligibility=%v want=%v", got, c.want)
			}
			plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, s, "source.mp4", 1.125, 640)
			if (err == nil) != c.want {
				t.Fatalf("preview plan: %v", err)
			}
			if err == nil && (plan.Source.SampleAspectRatio != c.sar || plan.Source.DisplayAspectRatio != c.dar || strings.Contains(plan.Filter, "setsar")) {
				t.Fatal("plan replaced source geometry", plan)
			}
		})
	}
}

func TestIntelPreviewMetadataKeepsUnspecifiedSARAndDeclaredDAR(t *testing.T) {
	for _, c := range []struct {
		name, metadata, sar, dar string
		want                     bool
	}{
		{"absent", "", "", "", true},
		{"zero", `,"sample_aspect_ratio":"0:1"`, "0:1", "", true},
		{"unavailable", `,"sample_aspect_ratio":"N/A"`, "N/A", "", true},
		{"matching DAR", `,"display_aspect_ratio":"16:9"`, "", "16:9", true},
		{"conflicting DAR", `,"display_aspect_ratio":"4:3"`, "", "4:3", false},
		{"non-square", `,"sample_aspect_ratio":"4:3"`, "4:3", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := `{"streams":[{"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":1920,"height":1080` + c.metadata + `}]}`
			path := filepath.Join(t.TempDir(), "ffprobe")
			if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'JSON'\n"+data+"\nJSON\n"), 0700); err != nil {
				t.Fatal(err)
			}
			s, err := (&FFProbe{path: path}).IntelPreviewSource(context.Background(), "source.mp4")
			if (err == nil) != c.want || s.SampleAspectRatio != c.sar || s.DisplayAspectRatio != c.dar {
				t.Fatalf("source=%+v error=%v", s, err)
			}
		})
	}
}
