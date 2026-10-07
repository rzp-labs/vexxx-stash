package ffmpeg

import (
	"strings"
	"testing"
)

func intelHDRFixture(transfer string) IntelSource {
	return IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", BitDepth: 10, Width: 3840, Height: 2160,
		SampleAspectRatio: "1:1", ColorPrimaries: "bt2020", ColorTransfer: transfer, ColorSpace: "bt2020nc", ColorRange: "tv", StreamIndex: 2}
}

func TestIntelHDRPlansConvertBeforeRetagging(t *testing.T) {
	for _, transfer := range []string{"smpte2084", "arib-std-b67"} {
		source := intelHDRFixture(transfer)
		for _, builder := range []func(IntelGenerationConfig, IntelSource, string, float64, int) (IntelGenerationPlan, error){NewIntelHDRPreviewPlan, NewIntelHDRSpritePlan} {
			p, err := builder(IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, source, "input", 3.125, 640)
			if err != nil {
				t.Fatal(err)
			}
			if p.Source.ColorTransfer != "bt709" || p.Source.ColorPrimaries != "bt709" || !strings.Contains(p.Filter, "tonemapping=bt.2390:gamut_mode=perceptual:peak_detect=1") {
				t.Fatalf("HDR tags changed without actual tone/gamut processing: %+v", p)
			}
			if !strings.Contains(p.Filter, IntelHDRMetadataFilter()) || strings.Index(p.Filter, "sidedata=") < strings.Index(p.Filter, "tonemapping=") {
				t.Fatalf("source HDR metadata survives or is removed before tone processing: %s", p.Filter)
			}
			if source.ColorTransfer != transfer || source.ColorPrimaries != "bt2020" || p.Source.StreamIndex != 2 {
				t.Fatal("source interpretation or mapping changed")
			}
			for _, step := range p.Probes {
				args := strings.Join(step.Args, " ")
				for _, forbidden := range []string{"hwdownload", "hwupload", "scale=", "libx264", "libwebp"} {
					if strings.Contains(args, forbidden) {
						t.Fatalf("CPU pixel operation %q in %s", forbidden, args)
					}
				}
			}
		}
	}
}

func TestIntelProjectedHDRRemovesOutputHDRSignalling(t *testing.T) {
	source := intelHDRFixture("smpte2084")
	for _, jpeg := range []bool{false, true} {
		plan, err := newIntelProjectionPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, 640, "LR180", jpeg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.Filter, IntelHDRMetadataFilter()) || strings.Index(plan.Filter, "sidedata=") < strings.Index(plan.Filter, "tonemapping=") {
			t.Fatalf("projected SDR output retains source HDR metadata: %s", plan.Filter)
		}
	}
	sdr := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, SampleAspectRatio: "1:1"}
	plan, err := newIntelProjectionPlan(IntelGenerationConfig{Backend: "vaapi"}, sdr, "input", 0, 640, "LR180", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Filter, "sidedata=") {
		t.Fatal("SDR projection unexpectedly strips side data")
	}
}

func TestIntelHDRAmbiguousColorFailsExplicitly(t *testing.T) {
	for _, mutate := range []func(*IntelSource){
		func(s *IntelSource) { s.ColorPrimaries = "unknown" },
		func(s *IntelSource) { s.ColorSpace = "unknown" },
		func(s *IntelSource) { s.ColorRange = "unknown" },
		func(s *IntelSource) { s.ColorPrimaries = "reserved" },
		func(s *IntelSource) { s.ColorSpace = "fcc" },
		func(s *IntelSource) { s.ColorSpace = "gbr"; s.IsRGB = false },
		func(s *IntelSource) { s.ColorTransfer = "unknown" },
		func(s *IntelSource) { s.BitDepth = 12 },
		func(s *IntelSource) { s.BitDepth = 0; s.PixelFormat = "unknown" },
	} {
		source := intelHDRFixture("smpte2084")
		mutate(&source)
		if _, err := NewIntelHDRPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, 640); err == nil {
			t.Fatalf("ambiguous HDR source accepted: %+v", source)
		}
	}
}

func TestIntelHDRRGBMatrixRequiresActualRGBPixels(t *testing.T) {
	source := intelHDRFixture("smpte2084")
	source.PixelFormat, source.IsRGB, source.ColorSpace = "gbrp10le", true, "gbr"
	if _, err := NewIntelHDRPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, 640); err != nil {
		t.Fatal("actual RGB HDR cannot reach device probes:", err)
	}
	source.IsRGB, source.PixelFormat = false, "p010le"
	if _, err := NewIntelHDRPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, 640); err == nil {
		t.Fatal("YUV tagged RGB would silently use libplacebo's resolution matrix guess")
	}
}

func TestIntelHDRUsesActualColorAndPrecisionRatherThanCodecProfile(t *testing.T) {
	config := IntelGenerationConfig{Backend: "vaapi"}
	for _, transfer := range []string{"smpte2084", "arib-std-b67"} {
		for _, interpretation := range []struct{ primaries, matrix, pixelRange string }{
			{"bt709", "bt709", "pc"},
			{"smpte432", "bt2020nc", "tv"},
			{"smpte428", "ictcp", "pc"},
			{"vgamut", "ycgco-re", "tv"},
		} {
			for _, codec := range []string{"av1", "vp9", "h264", "hevc"} {
				source := intelHDRFixture(transfer)
				source.Codec, source.Profile, source.PixelFormat = codec, "actual GPU profile", "p010le"
				source.ColorPrimaries, source.ColorSpace, source.ColorRange = interpretation.primaries, interpretation.matrix, interpretation.pixelRange
				source.RuntimeFingerprint = "runtime-for-actual-frame"
				for _, builder := range []func(IntelGenerationConfig, IntelSource, string, float64, int) (IntelGenerationPlan, error){NewIntelHDRPreviewPlan, NewIntelHDRSpritePlan} {
					plan, err := builder(config, source, "input", 0, 640)
					if err != nil {
						t.Fatalf("%s/%+v: %v", codec, interpretation, err)
					}
					if plan.InputSource.Codec != codec || plan.InputSource.ColorTransfer != transfer || plan.InputSource.ColorSpace != interpretation.matrix || plan.RuntimeFingerprint != source.RuntimeFingerprint || plan.Source.BitDepth != 8 {
						t.Fatalf("actual input identity lost: %+v", plan)
					}
					if len(plan.Probes) != 3 || !strings.Contains(plan.Filter, "format=x2rgb10le:") || !strings.Contains(plan.Filter, IntelHDRFilterOptions()) {
						t.Fatal("actual device probes or precision-preserving tone conversion lost", plan)
					}
				}
			}
		}
	}
}

func TestIntelHDRNonSquareSARPreservesPhysicalGeometryAndOrientation(t *testing.T) {
	for _, rotation := range []int{0, 90, 270} {
		source := intelHDRFixture("smpte2084")
		source.Width, source.Height, source.SampleAspectRatio, source.Rotation = 720, 576, "16:15", rotation
		for _, jpeg := range []bool{false, true} {
			builder := NewIntelHDRPreviewPlan
			if jpeg {
				builder = NewIntelHDRSpritePlan
			}
			plan, err := builder(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, 640)
			if err != nil {
				t.Fatal(err)
			}
			w, h, sar := 720, 576, "16:15"
			if rotation != 0 {
				w, h, sar = 576, 720, "15:16"
			}
			if plan.Source.Width != w || plan.Source.Height != h || plan.Source.SampleAspectRatio != sar || plan.InputSource.SampleAspectRatio != "16:15" {
				t.Fatalf("physical geometry or oriented pixel ratio changed: %+v", plan)
			}
			if !strings.Contains(plan.Filter, ":reset_sar=1:fit_mode=fill:") {
				t.Fatal("libplacebo may crop/stretch by source DAR instead of canonical physical geometry", plan.Filter)
			}
			if !jpeg && !strings.HasSuffix(plan.Filter, IntelPreviewSARFilter(plan.Source, 640)) {
				t.Fatal("preview encoded SAR lost", plan.Filter)
			}
		}
	}
}

func TestIntelHDRReflectedOrientationAppliedOnce(t *testing.T) {
	source := intelHDRFixture("smpte2084")
	matrix := [9]int32{0, 65536, 0, 65536, 0, 0, 0, 0, 1 << 30}
	source.DisplayMatrix = &matrix
	for _, builder := range []func(IntelGenerationConfig, IntelSource, string, float64, int) (IntelGenerationPlan, error){NewIntelHDRPreviewPlan, NewIntelHDRSpritePlan} {
		plan, err := builder(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, 640)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(plan.Filter, "transpose_vaapi=dir=cclock_flip:passthrough=none,") || plan.Source.DisplayMatrix != nil || plan.Source.Rotation != 0 {
			t.Fatalf("HDR orientation retained or lost: %+v", plan)
		}
		if w, h := IntelDisplayDimensions(plan.Source); w != 2160 || h != 3840 {
			t.Fatalf("HDR display dimensions transformed twice: %dx%d", w, h)
		}
		if source.DisplayMatrix != &matrix || source.ColorTransfer != "smpte2084" {
			t.Fatal("HDR input interpretation changed")
		}
	}
}
