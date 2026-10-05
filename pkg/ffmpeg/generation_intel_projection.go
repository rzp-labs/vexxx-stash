package ffmpeg

import (
	"encoding/hex"
	"fmt"
	"math"
)

const intelProjectionWidth, intelProjectionHeight = 1280, 720

// IntelProjectionShader ports v360's rectilinear output ray and four canonical
// input mappings to an mpv/libplacebo GPU hook. Pixel centers use v360's
// (2*i+1)/size-1 output convention and (size-1) input coordinate convention.
// Stereo output is 2D: sample only the first (left/top) eye, never both eyes.
// Reference: FFmpeg n8.1.2 libavfilter/vf_v360.c, flat_to_xyz,
// xyz_to_hequirect, xyz_to_equirect, xyz_to_fisheye and fov_from_dfov.
func IntelProjectionShader(mode string, width, height int) (string, error) {
	if width <= 1 || height <= 1 {
		return "", fmt.Errorf("GPU projection source dimensions unavailable")
	}
	eyeWidth, eyeHeight := width, height
	var mapping string
	switch mode {
	case "LR180":
		eyeWidth /= 2
		mapping = "vec2 eye = vec2(0.5) + vec2(atan(ray.x, ray.z), asin(ray.y)) / PI;"
	case "TB360":
		eyeHeight /= 2
		mapping = "vec2 eye = vec2(0.5) + vec2(atan(ray.x, ray.z) / (2.0 * PI), asin(ray.y) / PI);"
	case "MONO360":
		mapping = "vec2 eye = vec2(0.5) + vec2(atan(ray.x, ray.z) / (2.0 * PI), asin(ray.y) / PI);"
	case "FISHEYE190":
		eyeWidth /= 2
		mapping = "float radius = length(ray.xy);\n    vec2 eye = vec2(0.5);\n    if (radius > 0.0) eye += ray.xy / radius * atan(radius, ray.z) / radians(190.0);"
	default:
		return "", fmt.Errorf("unsupported GPU VR projection %q", mode)
	}
	if eyeWidth <= 1 || eyeHeight <= 1 {
		return "", fmt.Errorf("GPU projection eye dimensions unavailable")
	}
	// MAIN runs after YUV-to-RGB interpretation and before output scaling. Fix
	// the projection at the canonical 1280x720; thumbnail resizing comes later.
	// Explicit bilinear sampling does not depend on libplacebo's sampler preset.
	diagonal := math.Hypot(intelProjectionWidth, intelProjectionHeight)
	return fmt.Sprintf(`//!HOOK MAIN
//!BIND HOOKED
//!WIDTH 1280
//!HEIGHT 720
//!DESC VEX canonical %s projection
#define PI 3.14159265358979323846
vec4 hook() {
    vec2 screen = HOOKED_pos * 2.0 - vec2(1.0);
    vec3 ray = normalize(vec3(screen * vec2(%.17g, %.17g), 1.0));
    %s
    vec2 eye_max = vec2(%d.0, %d.0);
    vec2 pixel = clamp(eye * eye_max, vec2(0.0), eye_max);
    vec2 base = floor(pixel);
    vec2 weight = pixel - base;
    vec2 next_pixel = min(base + vec2(1.0), eye_max);
    vec2 size = vec2(%d.0, %d.0);
    vec4 a = HOOKED_tex((base + vec2(0.5)) / size);
    vec4 b = HOOKED_tex((vec2(next_pixel.x, base.y) + vec2(0.5)) / size);
    vec4 c = HOOKED_tex((vec2(base.x, next_pixel.y) + vec2(0.5)) / size);
    vec4 d = HOOKED_tex((next_pixel + vec2(0.5)) / size);
    return mix(mix(a, b, weight.x), mix(c, d, weight.x), weight.y);
}
`, mode, math.Tan(math.Pi/3)*intelProjectionWidth/diagonal, math.Tan(math.Pi/3)*intelProjectionHeight/diagonal, mapping, eyeWidth-1, eyeHeight-1, width, height), nil
}

// NewIntelProjectedPreviewPlan preserves the stored VR transformation on GPU.
// The encoded surface is still VAAPI; libplacebo never negotiates CPU pixels.
func NewIntelProjectedPreviewPlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int, mode string) (IntelGenerationPlan, error) {
	if mode == "" {
		return NewIntelPreviewPlan(config, source, input, start, width)
	}
	return newIntelProjectionPlan(config, source, input, start, width, mode, false)
}

func NewIntelProjectedSpritePlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int, mode string) (IntelGenerationPlan, error) {
	if mode == "" {
		return NewIntelSpritePlan(config, source, input, start, width)
	}
	return newIntelProjectionPlan(config, source, input, start, width, mode, true)
}

func newIntelProjectionPlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int, mode string, jpeg bool) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source}
	if config.Backend != "vaapi" {
		return p, fmt.Errorf("GPU VR projection requires VAAPI/Vulkan interoperability")
	}
	if width <= 0 || width%2 != 0 || math.IsNaN(start) || math.IsInf(start, 0) || start < 0 {
		return p, fmt.Errorf("invalid GPU projection output width or timestamp")
	}
	if IntelSourceHDR(source) && !IntelHDRSourceValid(source) {
		return p, fmt.Errorf("GPU VR HDR requires HEVC Main10 with explicit PQ/HLG BT.2020 interpretation")
	}
	// libplacebo 7.360 maps these declared color systems/transfers to UNKNOWN.
	// A resolution-based guess would silently reinterpret known source pixels.
	switch source.ColorSpace {
	case "fcc", "smpte2085", "chroma-derived-nc", "chroma-derived-c", "ipt-c2":
		return p, fmt.Errorf("GPU projection color matrix %q has no supported libplacebo interpretation", source.ColorSpace)
	}
	switch source.ColorTransfer {
	case "log100", "log316":
		return p, fmt.Errorf("GPU projection transfer %q has no supported libplacebo interpretation", source.ColorTransfer)
	}
	var err error
	if jpeg {
		err = source.ValidateSprite(config.Backend)
	} else {
		err = source.ValidatePreview()
	}
	if err != nil {
		return p, err
	}
	if !source.HasSquareOrUnspecifiedSampleAspectRatio() {
		return p, fmt.Errorf("GPU projection does not support sample aspect ratio %q", source.SampleAspectRatio)
	}
	rotation, err := IntelRotationFilter(config, source)
	if err != nil {
		return p, err
	}
	displayWidth, displayHeight := IntelDisplayDimensions(source)
	shader, err := IntelProjectionShader(mode, displayWidth, displayHeight)
	if err != nil {
		return p, err
	}
	p.InputArgs = IntelVulkanInputArgs(config, source)
	if rotation != "" {
		p.Filter = rotation + ","
	}
	// swscale's unspecified matrix defaults to BT.601 even for HD sources.
	// Give libplacebo that same interpretation instead of its resolution guess.
	if !IntelSourceHDR(source) {
		matrix := source.ColorSpace
		if matrix == "" || matrix == "unknown" || matrix == "unspecified" {
			matrix = "bt470bg"
			p.Source.ColorSpace = matrix
		}
		pixelRange := "limited"
		if source.ColorRange == "pc" || source.ColorRange == "jpeg" || source.PixelFormat == "yuvj420p" {
			pixelRange = "full"
			p.Source.ColorRange = "pc"
		} else if source.ColorRange == "" || source.ColorRange == "unknown" || source.ColorRange == "unspecified" {
			p.Source.ColorRange = "tv"
		}
		p.Filter += "setparams=colorspace=" + matrix + ":range=" + pixelRange + ","
	}
	// Projection replaces the input aspect ratio. Reset SAR so an 8K 2:1
	// equirectangular source produces a square-pixel 16:9 rectilinear view.
	p.Filter += "libplacebo=w=1280:h=720:format=" + IntelVulkanOutputFormat(source) + ":reset_sar=1:fit_mode=fill:custom_shader_bin=" + hex.EncodeToString([]byte(shader))
	// Known source color interpretation remains unchanged for SDR projection.
	// HDR output tags change only alongside explicit tone/gamut conversion.
	if IntelSourceHDR(source) {
		p.Filter += ":" + IntelHDRFilterOptions()
		p.Source.ColorSpace, p.Source.ColorPrimaries, p.Source.ColorTransfer, p.Source.ColorRange = "bt709", "bt709", "bt709", "tv"
	} else {
		p.Filter += ":colorspace=gbr:range=pc"
		for _, tag := range []struct{ key, value string }{
			{"color_primaries", source.ColorPrimaries}, {"color_trc", source.ColorTransfer},
		} {
			if tag.value != "" && tag.value != "unknown" && tag.value != "unspecified" {
				p.Filter += ":" + tag.key + "=" + tag.value
			}
		}
	}
	p.Filter += "," + IntelVulkanVAAPIReturnFilter()
	if IntelSourceHDR(source) {
		p.Filter += "," + IntelHDRMetadataFilter()
	}
	p.Filter += "," + IntelRGBInterpretationFilter(p.Source)
	// Only output geometry changes. Input initialization and probes retain the
	// actual codec, stream, bit depth and physical dimensions above.
	p.Source.Width, p.Source.Height, p.Source.Rotation = intelProjectionWidth, intelProjectionHeight, 0
	p.Source.DisplayMatrix = nil
	p.Source.SampleAspectRatio, p.Source.DisplayAspectRatio = "1:1", "16:9"
	if jpeg {
		rgbSource := p.Source
		rgbSource.ColorRange = "pc"
		p.Filter += "," + IntelSpriteScaleFilter(config, rgbSource, width)
	} else {
		p.Filter += "," + IntelScaleFilter(config, p.Source, width, false) + ":mode=hq"
		if p.Source.ColorSpace != "" && p.Source.ColorSpace != "unknown" && p.Source.ColorSpace != "unspecified" {
			p.Filter += ":out_color_matrix=" + p.Source.ColorSpace
		}
		switch p.Source.ColorRange {
		case "tv":
			p.Filter += ":out_range=limited"
		case "pc":
			p.Filter += ":out_range=full"
		}
		p.Filter += "," + IntelOutputColorTags(p.Source)
	}
	base := Args{"-v", "error", "-nostdin", "-abort_on", "empty_output", "-threads", "1"}
	base = append(base, p.InputArgs...)
	base = base.Seek(start).Input(input)
	base = append(base, "-map", fmt.Sprintf("0:%d", source.StreamIndex), "-an", "-frames:v", "1")
	p.Probes = append(p.Probes, IntelProbeStep{"decode", append(append(Args{}, base...), "-f", "null", "-")})
	p.Probes = append(p.Probes, IntelProbeStep{"projection", append(append(Args{}, base...), "-vf", p.Filter, "-f", "null", "-")})
	encode := append(Args{}, base...)
	if jpeg {
		encode = append(encode, "-vf", p.Filter+","+IntelJPEGRangeFilter(), "-c:v", "mjpeg_vaapi", "-global_quality", "95", "-color_range", "tv")
	} else {
		encode = append(encode, "-vf", p.Filter, "-c:v", "h264_vaapi", "-qp", "21", "-profile:v", "high", "-level:v", "4.2")
	}
	p.Probes = append(p.Probes, IntelProbeStep{"encode", append(encode, "-f", "null", "-")})
	return p, nil
}
