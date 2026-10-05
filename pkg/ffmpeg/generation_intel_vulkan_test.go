package ffmpeg

import (
	"strings"
	"testing"
)

func TestIntelVulkanKeepsSelectedDecodeDevice(t *testing.T) {
	args := IntelVulkanInputArgs(IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD129"}, IntelSource{Codec: "hevc"})
	joined := strings.Join(args, " ")
	for _, required := range []string{"vaapi=vex:/dev/dri/renderD129", "vulkan=vexvk@vex", "-filter_hw_device vexvk", "-hwaccel_device vex", "-hwaccel_output_format vaapi"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %s", required, joined)
		}
	}
}

func TestIntelVulkanReturnHasNoHostPixels(t *testing.T) {
	filter := IntelVulkanVAAPIReturnFilter()
	for _, forbidden := range []string{"hwdownload", "hwupload", "scale=", "format=nv12"} {
		if strings.Contains(filter, forbidden) {
			t.Fatalf("host transfer or format conversion %q in %s", forbidden, filter)
		}
	}
	if !strings.Contains(filter, "mode=read+direct") || strings.Contains(filter, "reverse=") || strings.Contains(filter, "scale_vulkan") {
		t.Fatal(filter)
	}
}

func TestIntelVulkanRGBPrecisionAndCSCMetadata(t *testing.T) {
	if IntelVulkanOutputFormat(IntelSource{PixelFormat: "yuv420p10le"}) != "x2rgb10le" || IntelVulkanOutputFormat(IntelSource{PixelFormat: "yuv420p"}) != "bgra" {
		t.Fatal("source precision changed before output conversion")
	}
	source := IntelSource{ColorSpace: "bt709", ColorRange: "tv"}
	if selector := IntelRGBInterpretationFilter(source); !strings.Contains(selector, "range=full:colorspace=bt709:color_primaries=bt709:color_trc=bt709") {
		t.Fatal(selector)
	}
	if restored := IntelOutputColorTags(source); !strings.Contains(restored, "range=limited:colorspace=bt709:color_primaries=unknown:color_trc=unknown") {
		t.Fatal("temporary CSC tags leaked into output:", restored)
	}
}
