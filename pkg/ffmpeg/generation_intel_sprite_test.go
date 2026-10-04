package ffmpeg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func intelMain10SpriteSource() IntelSource {
	return IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", Width: 8192, Height: 4096, ColorRange: "tv", ColorTransfer: "bt709", ColorPrimaries: "bt709", ColorSpace: "bt709", StreamIndex: 2}
}

func TestIntelMain10SpritePlanIsScoped(t *testing.T) {
	source := intelMain10SpriteSource()
	config := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	p, err := NewIntelSpritePlan(config, source, "actual.mp4", 98.85243025925925, 160)
	if err != nil {
		t.Fatal(err)
	}
	if p.Filter != "hwdownload,format=p010le,format=yuv420p10le,scale=160:-2" {
		t.Fatal(p.Filter)
	}
	var stages []string
	for _, probe := range p.Probes {
		stages = append(stages, probe.Stage)
		args := strings.Join(probe.Args, " ")
		for _, want := range []string{"-hwaccel vaapi", "-hwaccel_output_format vaapi", "-ss 98.85243025925925 -i actual.mp4", "-map 0:2 -an -frames:v 1", "-abort_on empty_output"} {
			if !strings.Contains(args, want) {
				t.Fatalf("missing %q: %s", want, args)
			}
		}
		if strings.Contains(args, "scale_vaapi") || strings.Contains(args, "nv12") || strings.Contains(args, "h264_vaapi") {
			t.Fatalf("noncanonical conversion/encoder: %s", args)
		}
	}
	if !reflect.DeepEqual(stages, []string{"decode", "download", "scale"}) {
		t.Fatal(stages)
	}
	if source.Validate() == nil {
		t.Fatal("shared source validator broadened")
	}
	if _, err := NewIntelGenerationPlan(config, source, "actual.mp4", 0, 640, false); err == nil {
		t.Fatal("marker plan broadened")
	}
	config.Backend = "qsv"
	if _, err := NewIntelSpritePlan(config, source, "actual.mp4", 0, 160); err == nil {
		t.Fatal("QSV broadened")
	}
}

func TestIntelMain10SpriteRejectsOtherFormatsAndColor(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*IntelSource)
	}{
		{"h264", func(s *IntelSource) { s.Codec = "h264" }},
		{"profile", func(s *IntelSource) { s.Profile = "Rext" }},
		{"unknown profile", func(s *IntelSource) { s.Profile = "" }},
		{"12-bit", func(s *IntelSource) { s.PixelFormat = "yuv420p12le" }},
		{"422", func(s *IntelSource) { s.PixelFormat = "yuv422p10le" }},
		{"PQ", func(s *IntelSource) { s.ColorTransfer = "smpte2084" }},
		{"HLG", func(s *IntelSource) { s.ColorTransfer = "arib-std-b67" }},
		{"wide primaries", func(s *IntelSource) { s.ColorPrimaries = "bt2020" }},
		{"wide matrix", func(s *IntelSource) { s.ColorSpace = "bt2020nc" }},
		{"full range", func(s *IntelSource) { s.ColorRange = "pc" }},
		{"unknown matrix", func(s *IntelSource) { s.ColorSpace = "" }},
		{"rotation", func(s *IntelSource) { s.Rotation = 90 }},
		{"dimensions", func(s *IntelSource) { s.Width = 0 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := intelMain10SpriteSource()
			c.edit(&s)
			if _, err := NewIntelSpritePlan(IntelGenerationConfig{Backend: "vaapi"}, s, "input", 0, 160); err == nil {
				t.Fatal("unsupported candidate accepted")
			}
		})
	}
}

func TestIntelMain10SpriteFallbackAndCancellation(t *testing.T) {
	for _, failure := range []string{"decode", "download", "scale", "tile", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			p, err := NewIntelSpritePlan(IntelGenerationConfig{Backend: "vaapi"}, intelMain10SpriteSource(), "input", 0, 160)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hw, sw, calls := 0, 0, 0
			d, err := runIntelGenerationWork(ctx, p,
				func(context.Context) error { hw++; return errors.New("tile failure") },
				func(context.Context) error { sw++; return nil },
				func(context.Context, Args) error {
					stage := p.Probes[calls].Stage
					calls++
					if failure == "cancel" {
						cancel()
						return context.Canceled
					}
					if stage == failure {
						return errors.New("unsupported stage")
					}
					return nil
				}, func(string) error { return nil })
			if failure == "cancel" {
				if !errors.Is(err, context.Canceled) || sw != 0 || hw != 0 {
					t.Fatalf("cancel err=%v hw=%d sw=%d", err, hw, sw)
				}
			} else if err != nil || sw != 1 || d.Actual != "software" || (failure != "tile" && (hw != 0 || d.Stage != failure)) {
				t.Fatalf("diagnostic=%+v err=%v hw=%d sw=%d", d, err, hw, sw)
			}
		})
	}
}

func TestIntel8BitSpriteCanonicalVAAPIScaleKeepsQSVAndMarkers(t *testing.T) {
	for _, pixelFormat := range []string{"yuv420p", "nv12"} {
		source := IntelSource{Codec: "h264", PixelFormat: pixelFormat, Width: 3840, Height: 2160}
		config := IntelGenerationConfig{Backend: "vaapi"}
		p, err := NewIntelSpritePlan(config, source, "six-audio.mp4", 98.85243025925925, 160)
		if err != nil {
			t.Fatal(err)
		}
		if p.Filter != "hwdownload,format=nv12,format=yuv420p,scale=160:-2" {
			t.Fatal(p.Filter)
		}
		var stages []string
		for _, probe := range p.Probes {
			stages = append(stages, probe.Stage)
			if strings.Contains(strings.Join(probe.Args, " "), "scale_vaapi") {
				t.Fatalf("VPP remained in sprite probe: %v", probe)
			}
		}
		if !reflect.DeepEqual(stages, []string{"decode", "download", "scale"}) {
			t.Fatal(stages)
		}
		marker, err := NewIntelGenerationPlan(config, source, "six-audio.mp4", 0, 640, false)
		if err != nil || marker.Filter != "scale_vaapi=w=640:h=360:format=nv12" {
			t.Fatalf("marker changed: %+v err=%v", marker, err)
		}
		config.Backend = "qsv"
		qsv, err := NewIntelSpritePlan(config, source, "six-audio.mp4", 0, 160)
		if err != nil || qsv.Filter != "scale_qsv=w=160:h=90:format=nv12,hwdownload,format=nv12" {
			t.Fatalf("QSV changed: %+v err=%v", qsv, err)
		}
	}
}
