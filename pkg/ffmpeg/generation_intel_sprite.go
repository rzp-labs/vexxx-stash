package ffmpeg

import "fmt"

// UsesCanonicalSpriteScale identifies VAAPI sprites that use CPU scaling.
// Downloading before scale preserves the software path's source precision and
// swscale color conversion rather than converting to 8-bit NV12 in VAAPI VPP.
func (s IntelSource) UsesCanonicalSpriteScale(backend string) bool {
	return backend == "vaapi" && (s.PixelFormat == "yuv420p" || s.PixelFormat == "nv12" || s.isMain10Sprite())
}

func (s IntelSource) isMain10Sprite() bool {
	return s.Codec == "hevc" && s.Profile == "Main 10" &&
		s.PixelFormat == "yuv420p10le" && s.ColorTransfer == "bt709" &&
		s.ColorPrimaries == "bt709" && s.ColorSpace == "bt709" && s.ColorRange == "tv"
}

func (s IntelSource) ValidateSprite(backend string) error {
	if backend == "vaapi" && s.isMain10Sprite() {
		// Reuse dimension/rotation/SDR checks without broadening the shared
		// marker or QSV pixel-format eligibility contract.
		s.PixelFormat = "yuv420p"
	}
	return s.Validate()
}

// NewIntelSpritePlan probes the actual sprite pipeline, never a video encoder.
// VAAPI transfers full-resolution NV12/P010 and restores the canonical planar
// format, then uses the same CPU scale as ScreenshotTime. QSV is unchanged.
func NewIntelSpritePlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	if !source.UsesCanonicalSpriteScale(config.Backend) {
		p, err := NewIntelGenerationPlan(config, source, input, start, width, true)
		if err != nil {
			return p, err
		}
		p.Probes = p.Probes[:len(p.Probes)-1] // BMP does not need the H.264 encode probe.
		return p, nil
	}
	p := IntelGenerationPlan{Config: config, Source: source}
	if err := source.ValidateSprite(config.Backend); err != nil {
		return p, err
	}
	if width <= 0 || width%2 != 0 {
		return p, fmt.Errorf("Intel output width must be positive and even")
	}
	p.InputArgs = IntelInputArgs(config, source)
	transfer := "hwdownload,format=nv12,format=yuv420p"
	if source.PixelFormat == "yuv420p10le" {
		transfer = "hwdownload,format=p010le,format=yuv420p10le"
	}
	p.Filter = fmt.Sprintf("%s,scale=%d:-2", transfer, width)
	base := Args{"-v", "error", "-nostdin", "-abort_on", "empty_output", "-threads", "1"}
	base = append(base, p.InputArgs...)
	base = base.Seek(start).Input(input)
	base = append(base, "-map", fmt.Sprintf("0:%d", source.StreamIndex), "-an", "-frames:v", "1")
	probe := func(stage, filter string) {
		args := append(Args{}, base...)
		if filter != "" {
			args = append(args, "-vf", filter)
		}
		p.Probes = append(p.Probes, IntelProbeStep{stage, append(args, "-f", "null", "-")})
	}
	probe("decode", "")
	probe("download", transfer)
	probe("scale", p.Filter+",format=bgr24")
	return p, nil
}
