package ffmpeg

import (
	"fmt"
	"math"
	"strings"
)

// IntelHDRMetadataFilter removes source HDR signalling after tone/gamut
// conversion. setparams changes color tags but retains side data, which the
// H.264 encoder otherwise republishes alongside the SDR pixels. These filters
// manipulate AVFrame metadata only and preserve resident hardware surfaces.
func IntelHDRMetadataFilter() string {
	var filters []string
	for _, kind := range []string{"MASTERING_DISPLAY_METADATA", "CONTENT_LIGHT_LEVEL", "DYNAMIC_HDR_PLUS", "DOVI_RPU_BUFFER", "DOVI_METADATA", "DYNAMIC_HDR_VIVID", "AMBIENT_VIEWING_ENVIRONMENT"} {
		filters = append(filters, "sidedata=mode=delete:type="+kind)
	}
	return strings.Join(filters, ",")
}

// IntelHDRSourceValid requires unambiguous HDR color interpretation and a
// precision-preserving GPU bridge. Codec/profile support is established by the
// actual decode/filter/encode probes, never by a metadata allowlist.
func IntelHDRSourceValid(source IntelSource) bool {
	return intelValidateHDRSource(source) == nil
}

func intelValidateHDRSource(source IntelSource) error {
	if !IntelSourceHDR(source) {
		return fmt.Errorf("GPU HDR requires an explicit PQ or HLG transfer function")
	}
	if err := intelValidateVulkanColor(source, true); err != nil {
		return err
	}
	return intelValidateVulkanPrecision(source)
}

// These are the actual libplacebo 7.360 libav mappings with the packaged
// FFmpeg 8.1.2 headers. Explicitly unmapped enums cannot authorize a color guess.
// Unspecified SDR metadata retains the canonical CPU interpretation separately.
func intelValidateVulkanColor(source IntelSource, requireKnown bool) error {
	// libplacebo overrides a non-YCbCr matrix on YUV with a resolution guess.
	// The actual decoded descriptor must authorize RGB interpretation instead.
	if source.ColorSpace == "gbr" && !source.IsRGB {
		return fmt.Errorf("GPU Vulkan RGB matrix requires an actual decoded RGB pixel descriptor")
	}
	unknown := func(value string) bool { return value == "" || value == "unknown" || value == "unspecified" }
	for _, field := range []struct {
		name, value string
		supported   bool
	}{
		{"primaries", source.ColorPrimaries, intelVulkanPrimariesMapped(source.ColorPrimaries)},
		{"matrix", source.ColorSpace, intelVulkanMatrixMapped(source.ColorSpace)},
		{"transfer", source.ColorTransfer, intelVulkanTransferMapped(source.ColorTransfer)},
		{"range", source.ColorRange, source.ColorRange == "tv" || source.ColorRange == "pc" || (!requireKnown && source.ColorRange == "jpeg")},
	} {
		if field.supported || (!requireKnown && unknown(field.value)) {
			continue
		}
		return fmt.Errorf("GPU Vulkan %s %q has no unambiguous supported color interpretation", field.name, field.value)
	}
	return nil
}

func intelVulkanPrimariesMapped(value string) bool {
	switch value {
	case "bt709", "bt470m", "bt470bg", "smpte170m", "smpte240m", "film", "bt2020", "smpte428", "smpte431", "smpte432", "ebu3213", "jedec-p22", "vgamut":
		return true
	}
	return false
}

func intelVulkanMatrixMapped(value string) bool {
	switch value {
	case "gbr", "bt709", "bt470bg", "smpte170m", "smpte240m", "ycgco", "bt2020nc", "bt2020c", "ictcp", "ycgco-re", "ycgco-ro":
		return true
	}
	return false
}

func intelVulkanTransferMapped(value string) bool {
	switch value {
	case "bt709", "bt470m", "bt470bg", "smpte170m", "smpte240m", "linear", "iec61966-2-4", "bt1361e", "iec61966-2-1", "bt2020-10", "bt2020-12", "smpte2084", "smpte428", "arib-std-b67", "vlog":
		return true
	}
	return false
}

func intelHDRSource(source IntelSource) (IntelSource, error) {
	if err := intelValidateHDRSource(source); err != nil {
		return source, err
	}
	if err := source.ValidateSampleAspectRatio(); err != nil {
		return source, err
	}
	output := source
	output.Width, output.Height = IntelDisplayDimensions(source)
	output.SampleAspectRatio = IntelOrientedSampleAspectRatio(source)
	output.Rotation = 0
	output.DisplayMatrix = nil
	output.PixelFormat = "nv12"
	output.BitDepth = 8
	output.ColorSpace, output.ColorPrimaries, output.ColorTransfer, output.ColorRange = "bt709", "bt709", "bt709", "tv"
	if output.Width <= 0 || output.Height <= 0 {
		return output, fmt.Errorf("GPU HDR dimensions are unavailable")
	}
	return output, nil
}

// NewIntelHDRPreviewPlan converts HDR to the preview's SDR output with explicit
// GPU tone/gamut processing, retaining hardware frames through VAAPI encoding.
func NewIntelHDRPreviewPlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source, InputSource: source, RuntimeFingerprint: source.RuntimeFingerprint}
	if config.Backend != "vaapi" || width <= 0 || width%2 != 0 {
		return p, fmt.Errorf("GPU HDR preview requires VAAPI and a positive even output width")
	}
	if math.IsNaN(start) || math.IsInf(start, 0) || start < 0 {
		return p, fmt.Errorf("invalid GPU HDR timestamp")
	}
	output, err := intelHDRSource(source)
	if err != nil {
		return p, err
	}
	rotation, err := IntelRotationFilter(config, source)
	if err != nil {
		return p, err
	}
	height := intelHDRHeight(output, width)
	p.Source = output
	p.InputArgs = IntelVulkanInputArgs(config, source)
	if rotation != "" {
		p.Filter = rotation + ","
	}
	p.Filter += fmt.Sprintf("libplacebo=w=%d:h=%d:format=%s:reset_sar=1:fit_mode=fill:%s,%s,%s,%s,scale_vaapi=w=%d:h=%d:format=nv12:out_color_matrix=bt709:out_range=limited,%s", width, height, IntelVulkanOutputFormat(source), IntelHDRFilterOptions(), IntelVulkanVAAPIReturnFilter(), IntelHDRMetadataFilter(), IntelRGBInterpretationFilter(output), width, height, IntelOutputColorTags(output))
	if sar := IntelPreviewSARFilter(output, width); sar != "" {
		p.Filter += "," + sar
	}
	p.Probes = intelHDRProbes(p, input, start, false)
	return p, nil
}

// NewIntelHDRSpritePlan retains full display dimensions during tone mapping,
// then uses the same staged GPU SDR reduction and JPEG conversion as sprites.
func NewIntelHDRSpritePlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source, InputSource: source, RuntimeFingerprint: source.RuntimeFingerprint}
	if config.Backend != "vaapi" || width <= 0 || width%2 != 0 {
		return p, fmt.Errorf("GPU HDR JPEG requires VAAPI and a positive even output width")
	}
	if math.IsNaN(start) || math.IsInf(start, 0) || start < 0 {
		return p, fmt.Errorf("invalid GPU HDR timestamp")
	}
	output, err := intelHDRSource(source)
	if err != nil {
		return p, err
	}
	rotation, err := IntelRotationFilter(config, source)
	if err != nil {
		return p, err
	}
	p.Source = output
	p.InputArgs = IntelVulkanInputArgs(config, source)
	if rotation != "" {
		p.Filter = rotation + ","
	}
	rgb := output
	rgb.ColorRange = "pc"
	p.Filter += fmt.Sprintf("libplacebo=w=%d:h=%d:format=%s:reset_sar=1:fit_mode=fill:%s,%s,%s,%s,%s", output.Width, output.Height, IntelVulkanOutputFormat(source), IntelHDRFilterOptions(), IntelVulkanVAAPIReturnFilter(), IntelHDRMetadataFilter(), IntelRGBInterpretationFilter(output), IntelSpriteScaleFilter(config, rgb, width))
	p.Probes = intelHDRProbes(p, input, start, true)
	return p, nil
}

func intelHDRHeight(source IntelSource, width int) int {
	// Reuse the canonical even-height geometry used by the VAAPI scale builder.
	height := int(math.Round(float64(source.Height)*float64(width)/float64(source.Width)/2)) * 2
	if height < 2 {
		height = 2
	}
	return height
}

func intelHDRProbes(plan IntelGenerationPlan, input string, start float64, jpeg bool) []IntelProbeStep {
	base := Args{"-v", "error", "-nostdin", "-abort_on", "empty_output", "-threads", "1"}
	base = append(base, plan.InputArgs...)
	if start > 0 {
		base = base.Seek(start)
	}
	base = base.Input(input)
	base = append(base, "-map", fmt.Sprintf("0:%d", plan.Source.StreamIndex), "-an", "-frames:v", "1")
	probes := []IntelProbeStep{{"decode", append(append(Args{}, base...), "-f", "null", "-")}, {"filter", append(append(Args{}, base...), "-vf", plan.Filter, "-f", "null", "-")}}
	encode := append(Args{}, base...)
	if jpeg {
		encode = append(encode, "-vf", plan.Filter+","+IntelJPEGRangeFilter(), "-c:v", "mjpeg_vaapi", "-global_quality", "95", "-color_range", "tv")
	} else {
		encode = append(encode, "-vf", plan.Filter, "-c:v", "h264_vaapi", "-qp", "21", "-profile:v", "high", "-level:v", "4.2")
	}
	probes = append(probes, IntelProbeStep{"encode", append(encode, "-f", "null", "-")})
	return probes
}
