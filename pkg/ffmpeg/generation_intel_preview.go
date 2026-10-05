package ffmpeg

import (
	"context"
	"fmt"
)

// IntelPreviewSource preserves automatic audio selection, independently of the
// marker 8-bit guard and the sprite path's audio-free stream mapping.
func (f *FFProbe) IntelPreviewSource(ctx context.Context, input string) (IntelSource, error) {
	s, err := f.intelSource(ctx, input, true)
	if err != nil {
		return s, err
	}
	return s, s.ValidatePreview()
}

func (s IntelSource) ValidatePreview() error {
	if s.isMain10Sprite() {
		// Only this documented SDR Main10 combination is converted to the scene
		// preview's 8-bit output. HDR/wide gamut requires the canonical CPU path.
		s.PixelFormat = "yuv420p"
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if !s.HasSquareOrUnspecifiedSampleAspectRatio() {
		return fmt.Errorf("sample aspect ratio %q (display aspect ratio %q) requires software scene previews", s.SampleAspectRatio, s.DisplayAspectRatio)
	}
	return nil
}

// NewIntelPreviewPlan uses canonical swscale before VAAPI encoding: retain
// source precision and color conversion rather than implicitly reducing P010
// through VPP. CPU scaling remains, but both decode and H.264 encode use the GPU.
func NewIntelPreviewPlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source}
	if config.Backend != "vaapi" {
		return p, fmt.Errorf("scene previews support software or vaapi")
	}
	if err := source.ValidatePreview(); err != nil {
		return p, err
	}
	if width <= 0 || width%2 != 0 {
		return p, fmt.Errorf("preview output width must be positive and even")
	}
	p.InputArgs = IntelInputArgs(config, source)
	transfer := "hwdownload,format=nv12,format=yuv420p"
	if source.PixelFormat == "yuv420p10le" {
		transfer = "hwdownload,format=p010le,format=yuv420p10le"
	}
	p.Filter = fmt.Sprintf("%s,scale=%d:-2,format=yuv420p,format=nv12,hwupload", transfer, width)
	base := Args{"-v", "error", "-nostdin", "-abort_on", "empty_output", "-threads", "1"}
	base = append(base, p.InputArgs...)
	if start > 0 {
		base = base.Seek(start)
	}
	base = base.Input(input)
	base = append(base, "-map", fmt.Sprintf("0:%d", source.StreamIndex), "-an", "-frames:v", "1")
	probe := func(stage, filter string, encode bool) {
		args := append(Args{}, base...)
		if filter != "" {
			args = append(args, "-vf", filter)
		}
		if encode {
			args = append(args, "-c:v", "h264_vaapi", "-qp", "21", "-profile:v", "high", "-level:v", "4.2")
		}
		p.Probes = append(p.Probes, IntelProbeStep{stage, append(args, "-f", "null", "-")})
	}
	probe("decode", "", false)
	probe("download", transfer, false)
	probe("filter", p.Filter, false)
	probe("encode", p.Filter, true)
	return p, nil
}
