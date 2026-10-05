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

// IntelHDRSourceValid limits the HDR shader path to unambiguous HEVC Main10
// color interpretation. Decode/filter/encode probes still verify the actual
// selected device and source; metadata alone never certifies capabilities.
func IntelHDRSourceValid(source IntelSource) bool {
	return source.Codec == "hevc" && source.Profile == "Main 10" && source.PixelFormat == "yuv420p10le" &&
		IntelSourceHDR(source) && source.ColorPrimaries == "bt2020" &&
		(source.ColorSpace == "bt2020nc" || source.ColorSpace == "bt2020c") && source.ColorRange == "tv"
}

func intelHDRSource(source IntelSource) (IntelSource, error) {
	if !IntelHDRSourceValid(source) {
		return source, fmt.Errorf("GPU HDR requires HEVC Main10 4:2:0, explicit PQ/HLG, BT.2020 matrix/primaries and limited range")
	}
	if !source.HasSquareOrUnspecifiedSampleAspectRatio() {
		return source, fmt.Errorf("GPU HDR requires square sample aspect ratio")
	}
	output := source
	output.Width, output.Height = IntelDisplayDimensions(source)
	output.Rotation = 0
	output.DisplayMatrix = nil
	output.PixelFormat = "nv12"
	output.ColorSpace, output.ColorPrimaries, output.ColorTransfer, output.ColorRange = "bt709", "bt709", "bt709", "tv"
	if output.Width <= 0 || output.Height <= 0 {
		return output, fmt.Errorf("GPU HDR dimensions are unavailable")
	}
	return output, nil
}

// NewIntelHDRPreviewPlan converts HDR to the preview's SDR output with explicit
// GPU tone/gamut processing, retaining hardware frames through VAAPI encoding.
func NewIntelHDRPreviewPlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source}
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
	p.Filter += fmt.Sprintf("libplacebo=w=%d:h=%d:format=%s:%s,%s,%s,%s,scale_vaapi=w=%d:h=%d:format=nv12:out_color_matrix=bt709:out_range=limited,%s", width, height, IntelVulkanOutputFormat(source), IntelHDRFilterOptions(), IntelVulkanVAAPIReturnFilter(), IntelHDRMetadataFilter(), IntelRGBInterpretationFilter(output), width, height, IntelOutputColorTags(output))
	if sar := IntelPreviewSARFilter(output, width); sar != "" {
		p.Filter += "," + sar
	}
	p.Probes = intelHDRProbes(p, input, start, false)
	return p, nil
}

// NewIntelHDRSpritePlan retains full display dimensions during tone mapping,
// then uses the same staged GPU SDR reduction and JPEG conversion as sprites.
func NewIntelHDRSpritePlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source}
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
	p.Filter += fmt.Sprintf("libplacebo=w=%d:h=%d:format=%s:%s,%s,%s,%s,%s", output.Width, output.Height, IntelVulkanOutputFormat(source), IntelHDRFilterOptions(), IntelVulkanVAAPIReturnFilter(), IntelHDRMetadataFilter(), IntelRGBInterpretationFilter(output), IntelSpriteScaleFilter(config, rgb, width))
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
