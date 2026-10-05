package ffmpeg

import (
	"math"
	"strings"
	"testing"
)

func intelMain10SpriteSource() IntelSource {
	return IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", Width: 8192, Height: 4096, ColorRange: "tv", ColorTransfer: "bt709", ColorPrimaries: "bt709", ColorSpace: "bt709", StreamIndex: 2}
}
func TestIntelSpritePlanGPUResidentJPEG(t *testing.T) {
	for _, source := range []IntelSource{{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StreamIndex: 2}, intelMain10SpriteSource()} {
		p, err := NewIntelSpritePlan(IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, source, "source.mp4", 1.9876543209876543, 160)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.Filter, "scale_vaapi=w=160:h=") || !strings.Contains(p.Filter, "mode=hq:out_color_matrix=bt470bg:out_range=limited") {
			t.Fatal(p.Filter)
		}
		if len(p.Probes) != 3 || p.Probes[2].Stage != "encode" {
			t.Fatal(p.Probes)
		}
		for _, probe := range p.Probes {
			args := strings.Join(probe.Args, " ")
			if !strings.Contains(args, "-ss 1.9876543209876543 -i source.mp4 -map 0:2 -an -frames:v 1") {
				t.Fatal(args)
			}
			for _, bad := range []string{"hwdownload", "hwupload", "scale=", "bmp", "h264_vaapi"} {
				if strings.Contains(args, bad) {
					t.Fatalf("CPU/irrelevant stage %q: %s", bad, args)
				}
			}
		}
		if got := strings.Join(p.Probes[2].Args, " "); !strings.Contains(got, "mjpeg_vaapi -global_quality 95 -color_range tv") {
			t.Fatal(got)
		}
	}
}
func TestIntelSpritePlanUnsupportedIsExplicit(t *testing.T) {
	source := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080}
	for _, backend := range []string{"qsv", "software", "cuda"} {
		if _, err := NewIntelSpritePlan(IntelGenerationConfig{Backend: backend}, source, "source.mp4", 0, 160); err == nil {
			t.Fatalf("accepted %s", backend)
		}
	}
	for _, change := range []func(*IntelSource){func(s *IntelSource) { s.Rotation = 45 }, func(s *IntelSource) { s.SampleAspectRatio = "4:3" }, func(s *IntelSource) { s.ColorTransfer = "smpte2084" }, func(s *IntelSource) { s.PixelFormat = "yuv422p10le" }} {
		s := source
		change(&s)
		if _, err := NewIntelSpritePlan(IntelGenerationConfig{Backend: "vaapi"}, s, "source.mp4", 0, 160); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
func TestIntelSpriteSeekListPreservesOriginsDuplicatesAndQuoting(t *testing.T) {
	got, err := IntelSpriteSeekList("/tmp/it's \\ media.mp4", IntelSource{StartTime: "5.25"}, []float64{0.19, 0.19, 1.9876543209876543})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"file '/tmp/it'\\''s \\ media.mp4'", "inpoint 5.44\noutpoint 6.44\nduration 1", "inpoint 7.237654320987654"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if strings.Count(got, "inpoint 5.44\n") != 2 {
		t.Fatal(got)
	}
	for _, times := range [][]float64{nil, {-1}, {1, 0}, {math.NaN()}, {math.Inf(1)}} {
		if _, err := IntelSpriteSeekList("/tmp/source.mp4", IntelSource{}, times); err == nil {
			t.Fatalf("accepted %v", times)
		}
	}
	for _, origin := range []string{"N/A", "NaN", "+Inf"} {
		if _, err := IntelSpriteSeekList("/tmp/source.mp4", IntelSource{StartTime: origin}, []float64{0}); err == nil {
			t.Fatalf("accepted origin %s", origin)
		}
	}
	if _, err := IntelSpriteSeekList("/tmp/a\nb.mp4", IntelSource{}, []float64{0}); err == nil {
		t.Fatal("accepted multiline path")
	}
}
