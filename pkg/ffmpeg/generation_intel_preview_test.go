package ffmpeg

import (
	"strings"
	"testing"
)

func TestIntelPreviewMain10IndependentEligibility(t *testing.T) {
	source := IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", Width: 3840, Height: 2160,
		SampleAspectRatio: "1:1", ColorPrimaries: "bt709", ColorTransfer: "bt709", ColorSpace: "bt709", ColorRange: "tv"}
	if err := source.ValidatePreview(); err != nil {
		t.Fatal(err)
	}
	if err := source.Validate(); err == nil {
		t.Fatal("scene Main10 support broadened marker eligibility")
	}
	plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, source, "input.mp4", 12.125, 640)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Filter != "scale_vaapi=w=640:h=360:format=nv12:mode=hq:out_color_matrix=bt709:out_range=limited" {
		t.Fatal(plan.Filter)
	}
	if len(plan.Probes) != 3 || !strings.Contains(strings.Join(plan.Probes[2].Args, " "), "h264_vaapi") {
		t.Fatal("missing actual-source encode probe", plan.Probes)
	}
	for _, probe := range plan.Probes {
		args := strings.Join(probe.Args, " ")
		for _, forbidden := range []string{"hwdownload", "hwupload", "scale=", "format=yuv420p"} {
			if strings.Contains(args, forbidden) {
				t.Fatalf("CPU video operation %q in probe %s", forbidden, args)
			}
		}
	}
	for _, mutate := range []func(*IntelSource){
		func(s *IntelSource) { s.ColorTransfer = "smpte2084" }, func(s *IntelSource) { s.ColorRange = "pc" },
		func(s *IntelSource) { s.Rotation = 90 }, func(s *IntelSource) { s.SampleAspectRatio = "4:3" },
	} {
		s := source
		mutate(&s)
		if s.ValidatePreview() == nil {
			t.Fatalf("unsupported source accepted %+v", s)
		}
	}
	source.Codec = "h264"
	source.PixelFormat = "yuv420p"
	source.Profile = "High"
	plan, err = NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input.mp4", 0, 640)
	if err != nil || !strings.Contains(plan.Filter, "scale_vaapi=w=640:h=360:format=nv12:mode=hq") {
		t.Fatalf("8-bit path %+v %v", plan, err)
	}
	if _, err = NewIntelPreviewPlan(IntelGenerationConfig{Backend: "qsv"}, source, "input", 0, 640); err == nil {
		t.Fatal("unvalidated QSV previews accepted")
	}
}

func TestIntelPreviewPreservesColorInterpretation(t *testing.T) {
	s := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, ColorSpace: "bt709", ColorRange: "pc"}
	plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, s, "input", 0, 640)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Filter, ":out_color_matrix=bt709:out_range=full") {
		t.Fatal("VPP would lose source matrix/range", plan.Filter)
	}
	s.ColorSpace, s.ColorRange = "unknown", "unknown"
	plan, err = NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, s, "input", 0, 640)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Filter, "out_color") || strings.Contains(plan.Filter, "out_range") {
		t.Fatal("invented unspecified source tags", plan.Filter)
	}
}
