package ffmpeg

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestIntelCanonicalScaleAspectEligibility(t *testing.T) {
	for _, c := range []struct {
		sar, dar       string
		want, wantPlan bool
	}{
		{"1:1", "16:9", true, true}, {"1/1", "", true, true}, {"1", "", true, true},
		{"", "", true, true}, {"N/A", "N/A", true, true}, {"0:1", "", true, true}, {"0/1", "0:1", true, true},
		{"", "16:9", true, true}, {"N/A", "16/9", true, true},
		{"", "4:3", false, true}, {"0:1", "malformed", false, true}, {"", "0:0", false, true},
		{"4:3", "64:27", false, true}, {"16:15", "", false, true},
		{"0:0", "", false, false}, {"1:0", "", false, false}, {"unknown", "", false, false}, {"-4:3", "", false, false},
	} {
		t.Run(fmt.Sprintf("sar_%s_dar_%s", c.sar, c.dar), func(t *testing.T) {
			s := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080,
				SampleAspectRatio: c.sar, DisplayAspectRatio: c.dar}
			if got := s.HasSquareOrUnspecifiedSampleAspectRatio(); got != c.want {
				t.Fatalf("aspect eligibility=%v want=%v", got, c.want)
			}
			plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, s, "source.mp4", 1.125, 640)
			if (err == nil) != c.wantPlan {
				t.Fatalf("preview plan: %v", err)
			}
			if err == nil && (plan.Source.SampleAspectRatio != c.sar || plan.Source.DisplayAspectRatio != c.dar) {
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
		{"conflicting DAR", `,"display_aspect_ratio":"4:3"`, "", "4:3", true},
		{"non-square", `,"sample_aspect_ratio":"4:3"`, "4:3", "", true},
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

func TestIntelPreviewNonSquareSARPreservesDisplayAspectAndPhysicalGeometry(t *testing.T) {
	for _, c := range []struct {
		name                     string
		source                   IntelSource
		width, height            int
		wantSAR, wantOrientedSAR string
	}{
		{"anamorphic PAL", IntelSource{Width: 720, Height: 576, SampleAspectRatio: "16:15"}, 640, 512, "16/15", "16:15"},
		{"rotated anamorphic PAL", IntelSource{Width: 720, Height: 576, SampleAspectRatio: "16:15", Rotation: 90}, 640, 800, "15/16", "15:16"},
		{"opposite rotation", IntelSource{Width: 720, Height: 576, SampleAspectRatio: "16:15", Rotation: 270}, 640, 800, "15/16", "15:16"},
		{"half turn", IntelSource{Width: 720, Height: 576, SampleAspectRatio: "16:15", Rotation: 180}, 640, 512, "16/15", "16:15"},
		{"rounding compensation", IntelSource{Width: 854, Height: 480, SampleAspectRatio: "4:3"}, 640, 360, "427/320", "4:3"},
		{"explicit square correction", IntelSource{Width: 854, Height: 480, SampleAspectRatio: "1280:1281"}, 640, 360, "1", "1280:1281"},
	} {
		t.Run(c.name, func(t *testing.T) {
			source := c.source
			source.Codec, source.PixelFormat = "h264", "yuv420p"
			source.RuntimeFingerprint = "verified-runtime"
			plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, c.width)
			if err != nil {
				t.Fatal(err)
			}
			wantScale := fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=nv12", c.width, c.height)
			wantSAR := "setsar=sar=" + c.wantSAR + ":max=2147483647"
			if !strings.Contains(plan.Filter, wantScale) || !strings.HasSuffix(plan.Filter, wantSAR) {
				t.Fatalf("canonical physical scale or display aspect lost: %s", plan.Filter)
			}
			if got := IntelOrientedSampleAspectRatio(source); got != c.wantOrientedSAR {
				t.Fatal("pixel SAR orientation lost", got)
			}
			if plan.InputSource.SampleAspectRatio != source.SampleAspectRatio || plan.InputSource.Width != source.Width || plan.RuntimeFingerprint != source.RuntimeFingerprint {
				t.Fatal("plan lost physical input or verified runtime identity", plan)
			}
		})
	}
}

func TestIntelAspectReflectionUsesAuthoritativeMatrix(t *testing.T) {
	for _, c := range []struct {
		matrix [9]int32
		want   string
	}{
		{[9]int32{-65536, 0, 0, 0, 65536, 0, 0, 0, 1 << 30}, "4:3"},
		{[9]int32{0, 65536, 0, 65536, 0, 0, 0, 0, 1 << 30}, "3:4"},
	} {
		source := IntelSource{Width: 720, Height: 576, SampleAspectRatio: "4:3", Rotation: 45, DisplayMatrix: &c.matrix}
		if got := IntelOrientedSampleAspectRatio(source); got != c.want {
			t.Fatal("reflection or authoritative orientation lost", got)
		}
	}
}

func TestIntelPreviewAspectMatchesCanonicalCPUScale(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable for canonical CPU geometry control")
	}
	for _, c := range []struct {
		width, height, rotation int
		sar                     string
	}{
		{720, 576, 0, "16:15"}, {720, 576, 90, "16:15"},
		{720, 576, 270, "16:15"}, {854, 480, 0, "4:3"},
		{854, 480, 0, "1280:1281"},
	} {
		t.Run(fmt.Sprintf("%dx%d_sar%s_rotation%d", c.width, c.height, c.sar, c.rotation), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			filter := "setsar=sar=" + strings.ReplaceAll(c.sar, ":", "/") + ":max=2147483647,"
			if c.rotation == 90 {
				filter += "transpose=cclock,"
			} else if c.rotation == 270 {
				filter += "transpose=clock,"
			}
			filter += "scale=640:-2,showinfo"
			output, err := exec.CommandContext(ctx, binary, "-hide_banner", "-nostdin", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=0.04", c.width, c.height), "-vf", filter, "-frames:v", "1", "-an", "-c:v", "wrapped_avframe", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Fatalf("CPU geometry control: %v %s", err, output)
			}
			frame := regexp.MustCompile(`sar:([0-9]+/[0-9]+) s:([0-9]+)x([0-9]+)`).FindStringSubmatch(string(output))
			if len(frame) != 4 {
				t.Fatalf("CPU geometry control omitted frame properties: %s", output)
			}
			source := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: c.width, Height: c.height, SampleAspectRatio: c.sar, Rotation: c.rotation}
			plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "source", 0, 640)
			if err != nil {
				t.Fatal(err)
			}
			sar, ok := new(big.Rat).SetString(frame[1])
			if !ok || !strings.Contains(plan.Filter, "scale_vaapi=w="+frame[2]+":h="+frame[3]+":") || !strings.HasSuffix(plan.Filter, "setsar=sar="+sar.RatString()+":max=2147483647") {
				t.Fatalf("GPU plan differs from canonical CPU geometry/SAR %v: %s", frame[1:], plan.Filter)
			}
		})
	}
}
