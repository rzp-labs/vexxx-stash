package ffmpeg

import (
	"context"
	"fmt"
	"math"
	"math/big"
)

// IntelPreviewSARFilter preserves source display aspect after rotation and the
// canonical physical-dimension/even-height scaling. VPP can propagate stale
// frame SAR; setsar repairs only metadata on resident hardware surfaces.
func IntelPreviewSARFilter(source IntelSource, width int) string {
	displayWidth, displayHeight := IntelDisplayDimensions(source)
	if displayWidth <= 0 || displayHeight <= 0 || width <= 0 {
		return ""
	}
	height := int(math.Round(float64(displayHeight)*float64(width)/float64(displayWidth)/2)) * 2
	if height < 2 {
		height = 2
	}
	sar := big.NewRat(int64(displayWidth), int64(displayHeight))
	inputSAR, err := intelSampleAspectRatio(source.SampleAspectRatio)
	if err != nil {
		return ""
	}
	if inputSAR != nil {
		angle, err := intelRotationDegrees(source)
		if err != nil {
			return ""
		}
		orientedSAR := new(big.Rat).Set(inputSAR)
		if angle == 90 || angle == 270 {
			orientedSAR.Inv(orientedSAR)
		}
		sar.Mul(sar, orientedSAR)
	}
	sar.Mul(sar, big.NewRat(int64(height), int64(width)))
	if sar.Cmp(big.NewRat(1, 1)) == 0 && (inputSAR == nil || inputSAR.Cmp(big.NewRat(1, 1)) == 0) {
		return ""
	}
	return "setsar=sar=" + sar.RatString() + ":max=2147483647"
}

// IntelPreviewSource preserves automatic audio selection independently of the
// sprite path's audio-free stream mapping.
func (f *FFProbe) IntelPreviewSource(ctx context.Context, input string) (IntelSource, error) {
	s, err := f.intelSource(ctx, input, true)
	if err != nil {
		return s, err
	}
	return s, s.ValidatePreview()
}

func (s IntelSource) ValidatePreview() error {
	if IntelSourceHDR(s) {
		if !IntelHDRSourceValid(s) {
			return fmt.Errorf("GPU HDR preview requires explicit supported HDR color interpretation")
		}
		// This validation copy checks generic geometry and orientation while
		// retaining the original HDR tags for the explicit tone mapping plan.
		s.ColorTransfer, s.ColorPrimaries, s.ColorSpace = "", "", ""
	}
	if err := s.Validate(); err != nil {
		return err
	}
	return s.ValidateSampleAspectRatio()
}

// NewIntelPreviewPlan keeps hardware frames resident through VAAPI scaling,
// color/pixel conversion and H.264 encoding. Actual-source probes exercise the
// same pipeline; unsupported hardware never authorizes a software retry.
func NewIntelPreviewPlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source, InputSource: source, RuntimeFingerprint: source.RuntimeFingerprint}
	if config.Backend != "vaapi" {
		return p, fmt.Errorf("scene previews support software or vaapi")
	}
	if err := source.ValidatePreview(); err != nil {
		return p, err
	}
	if IntelSourceHDR(source) {
		return NewIntelHDRPreviewPlan(config, source, input, start, width)
	}
	if width <= 0 || width%2 != 0 {
		return p, fmt.Errorf("preview output width must be positive and even")
	}
	rotation, err := IntelRotationFilter(config, source)
	if err != nil {
		return p, err
	}
	p.InputArgs = IntelInputArgs(config, source)
	p.Filter = intelPrependRotation(rotation, IntelScaleFilter(config, source, width, false)+":mode=hq")
	// VPP must preserve the source interpretation even after resizing below
	// common SD/HD matrix boundaries. Unspecified tags remain unspecified.
	if source.ColorSpace != "" && source.ColorSpace != "unknown" && source.ColorSpace != "unspecified" {
		p.Filter += ":out_color_matrix=" + source.ColorSpace
	}
	switch source.ColorRange {
	case "tv":
		p.Filter += ":out_range=limited"
	case "pc":
		p.Filter += ":out_range=full"
	}
	if sar := IntelPreviewSARFilter(source, width); sar != "" {
		p.Filter += "," + sar
	}
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
	probe("filter", p.Filter, false)
	probe("encode", p.Filter, true)
	return p, nil
}
