package ffmpeg

import "fmt"

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

// IntelVulkanOutputFormat retains Main10 precision until the final VPP resize
// and 8-bit output conversion. Both formats occupy one exportable DRM object.
func IntelVulkanOutputFormat(source IntelSource) string {
	if source.PixelFormat == "yuv420p10le" {
		return "x2rgb10le"
	}
	return "bgra"
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
	default:
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
