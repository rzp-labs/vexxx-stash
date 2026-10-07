package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"
)

// IntelVulkanInputArgs keeps decode on the selected VAAPI device and derives
// Vulkan from that same device. The packaged pool fix permits direct GPU
// import of exportable RGB surfaces into VAAPI.
func IntelVulkanInputArgs(config IntelGenerationConfig, source IntelSource) Args {
	args := IntelInputArgs(config, source)
	args = append(Args{args[0], args[1], "-init_hw_device", "vulkan=vexvk@vex"}, args[2:]...)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-filter_hw_device" {
			args[i+1] = "vexvk"
		}
	}
	return args
}

// IntelVulkanVAAPIReturnFilter imports one exportable RGB GPU allocation.
// The packaged Vulkan pool selects a LINEAR DRM modifier and supplies actual
// plane descriptors. No reverse mapped writes or host pixels are involved.
func IntelVulkanVAAPIReturnFilter() string {
	return "hwmap=derive_device=vaapi:mode=read+direct,format=vaapi"
}

// IntelVulkanOutputFormat retains the decoded component precision until final
// VPP conversion. The current single-object DRM bridge exports 8- or 10-bit RGB;
// a higher-precision source must not silently negotiate one of those formats.
func IntelVulkanOutputFormat(source IntelSource) string {
	depth := intelVulkanSourceBitDepth(source)
	if depth > 8 && depth <= 10 {
		return "x2rgb10le"
	}
	if depth > 0 && depth <= 8 {
		return "bgra"
	}
	return ""
}

func intelVulkanSourceBitDepth(source IntelSource) int {
	if source.BitDepth != 0 {
		return source.BitDepth
	}
	// Production metadata supplies the actual AVPixFmtDescriptor depth. Keep
	// named fixture formats usable for older callers/tests without interpreting
	// an unknown format as eight-bit.
	switch source.PixelFormat {
	case "yuv420p", "yuv422p", "yuv444p", "yuv440p", "yuv410p", "yuv411p", "yuvj420p", "yuvj422p", "yuvj444p", "nv12", "nv21", "rgb24", "bgr24", "rgba", "bgra", "argb", "abgr", "gbrp", "gray":
		return 8
	case "p010le", "p010be", "x2rgb10le", "x2rgb10be", "x2bgr10le", "x2bgr10be":
		return 10
	case "p012le", "p012be":
		return 12
	case "p016le", "p016be":
		return 16
	}
	format := strings.TrimSuffix(strings.TrimSuffix(source.PixelFormat, "le"), "be")
	for _, prefix := range []string{"yuv420p", "yuv422p", "yuv444p", "yuv440p", "gbrp", "gbrap", "gray"} {
		if suffix, ok := strings.CutPrefix(format, prefix); ok {
			if depth, err := strconv.Atoi(suffix); err == nil {
				return depth
			}
		}
	}
	return 0
}

func intelValidateVulkanPrecision(source IntelSource) error {
	if IntelVulkanOutputFormat(source) != "" {
		return nil
	}
	depth := intelVulkanSourceBitDepth(source)
	if depth <= 0 {
		return fmt.Errorf("GPU Vulkan conversion requires the decoded component bit depth")
	}
	return fmt.Errorf("GPU Vulkan/VAAPI RGB export supports at most 10-bit components; decoded source has %d-bit components", depth)
}

// IntelRGBInterpretationFilter supplies iHD's complete CSC selector on full-range
// RGB input. It selects a matrix, not a tone/transfer/gamut operation. Restore the
// actual output metadata after VPP conversion; HDR conversion happens earlier.
func IntelRGBInterpretationFilter(source IntelSource) string {
	matrix, primaries, transfer := source.ColorSpace, "bt470bg", "smpte170m"
	switch matrix {
	case "bt709":
		primaries, transfer = "bt709", "bt709"
	case "smpte170m":
		primaries = "smpte170m"
	case "smpte240m":
		primaries, transfer = "smpte240m", "smpte240m"
	case "bt2020nc", "bt2020c":
		primaries, transfer = "bt2020", "bt2020-10"
	case "", "unknown", "unspecified":
		matrix = "bt470bg"
	}
	return fmt.Sprintf("setparams=range=full:colorspace=%s:color_primaries=%s:color_trc=%s", matrix, primaries, transfer)
}

// IntelOutputColorTags restores the actual interpretation of encoded samples.
// Unknown source primaries/transfer stay unknown after temporary CSC selectors.
func IntelOutputColorTags(source IntelSource) string {
	known := func(value string) string {
		if value == "" || value == "unspecified" {
			return "unknown"
		}
		return value
	}
	pixelRange := "limited"
	if source.ColorRange == "pc" || source.ColorRange == "jpeg" {
		pixelRange = "full"
	}
	return fmt.Sprintf("setparams=range=%s:colorspace=%s:color_primaries=%s:color_trc=%s", pixelRange, known(source.ColorSpace), known(source.ColorPrimaries), known(source.ColorTransfer))
}

// IntelSourceHDR identifies transfer functions which require an explicit HDR
// output policy. Wide gamut alone is not assumed to be HDR.
func IntelSourceHDR(source IntelSource) bool {
	return source.ColorTransfer == "smpte2084" || source.ColorTransfer == "arib-std-b67"
}

// IntelHDRFilterOptions specifies an actual GPU HDR-to-SDR conversion. Output
// tags may become BT.709 only when these tone/gamut operations have run.
func IntelHDRFilterOptions() string {
	return "colorspace=gbr:color_primaries=bt709:color_trc=bt709:range=pc:tonemapping=bt.2390:gamut_mode=perceptual:peak_detect=1"
}
