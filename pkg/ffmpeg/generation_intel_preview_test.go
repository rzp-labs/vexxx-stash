package ffmpeg

import (
	"strings"
	"testing"
)

func TestIntelPreviewSDRDepthRequiresActualHardwareProbes(t *testing.T) {
	source := IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", Width: 3840, Height: 2160,
		SampleAspectRatio: "1:1", ColorPrimaries: "bt709", ColorTransfer: "bt709", ColorSpace: "bt709", ColorRange: "tv"}
	if err := source.ValidatePreview(); err != nil {
		t.Fatal(err)
	}
	if err := source.Validate(); err != nil {
		t.Fatal("generic validity must not whitelist SDR bit depth", err)
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
		func(s *IntelSource) { s.ColorTransfer, s.ColorPrimaries = "smpte2084", "unknown" },
		func(s *IntelSource) { s.Rotation = 45 }, func(s *IntelSource) { s.SampleAspectRatio = "1:0" },
	} {
		s := source
		mutate(&s)
		if s.ValidatePreview() == nil {
			t.Fatalf("unsupported source accepted %+v", s)
		}
	}
	for _, candidate := range []struct{ codec, profile, format string }{
		{"av1", "Main", "yuv420p10le"}, {"vp9", "Profile 2", "yuv422p10le"},
		{"hevc", "Main 12", "yuv420p12le"}, {"h264", "High 4:4:4 Predictive", "yuv444p"},
		{"mpeg2video", "Main", "yuv420p"},
	} {
		s := source
		s.Codec, s.Profile, s.PixelFormat = candidate.codec, candidate.profile, candidate.format
		s.ColorRange, s.SampleAspectRatio = "pc", "4:3"
		plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, s, "source", 0, 640)
		if err != nil || len(plan.Probes) != 3 {
			t.Fatalf("metadata whitelist replaced actual capability probes: %+v %v", candidate, err)
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

func TestIntelHDRRoutingRetainsStrictSourceInterpretation(t *testing.T) {
	config := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	for _, transfer := range []string{"smpte2084", "arib-std-b67"} {
		source := intelHDRFixture(transfer)
		source.Rotation = 90
		if err := source.ValidatePreview(); err != nil {
			t.Fatal(err)
		}
		if err := source.ValidateSprite("vaapi"); err != nil {
			t.Fatal(err)
		}
		if source.Validate() == nil || source.ValidateSprite("qsv") == nil {
			t.Fatal("HDR support broadened generic or unvalidated backend eligibility")
		}
		for _, build := range []func(IntelGenerationConfig, IntelSource, string, float64, int) (IntelGenerationPlan, error){NewIntelPreviewPlan, NewIntelSpritePlan} {
			plan, err := build(config, source, "HDR source.mp4", 1.125, 160)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(plan.Filter, "transpose_vaapi=dir=cclock:passthrough=none,") || !strings.Contains(plan.Filter, "tonemapping=bt.2390") || !strings.Contains(plan.Filter, "gamut_mode=perceptual") {
				t.Fatalf("HDR went through SDR scaling or lost rotation: %s", plan.Filter)
			}
			if plan.Source.Width != source.Height || plan.Source.Height != source.Width || plan.Source.Rotation != 0 || plan.Source.ColorTransfer != "bt709" {
				t.Fatalf("incorrect converted output geometry/interpretation: %+v", plan.Source)
			}
			if !strings.Contains(strings.Join(plan.InputArgs, " "), "-noautorotate -display_rotation 0") {
				t.Fatal("HDR decoder would apply CPU autorotation", plan.InputArgs)
			}
		}
		if source.ColorTransfer != transfer || source.ColorPrimaries != "bt2020" || source.PixelFormat != "yuv420p10le" || source.Rotation != 90 {
			t.Fatal("validation changed original HDR input", source)
		}
		for _, mutate := range []func(*IntelSource){
			func(s *IntelSource) { s.Width = 0 },
			func(s *IntelSource) { s.Height = -1 },
			func(s *IntelSource) { s.Rotation = 45 },
			func(s *IntelSource) { s.SampleAspectRatio = "1:0" },
			func(s *IntelSource) { s.ColorSpace = "unknown" },
			func(s *IntelSource) { s.ColorPrimaries = "unknown" },
			func(s *IntelSource) { s.ColorRange = "unknown" },
		} {
			invalid := source
			mutate(&invalid)
			for _, build := range []func(IntelGenerationConfig, IntelSource, string, float64, int) (IntelGenerationPlan, error){NewIntelPreviewPlan, NewIntelSpritePlan} {
				if _, err := build(config, invalid, "input", 0, 160); err == nil {
					t.Fatalf("invalid HDR metadata accepted: %+v", invalid)
				}
			}
		}
	}
}
