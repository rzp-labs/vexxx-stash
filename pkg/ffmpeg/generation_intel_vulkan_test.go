package ffmpeg

import (
	"strings"
	"testing"
)

func TestIntelVulkanKeepsSelectedDecodeDevice(t *testing.T) {
	args := IntelVulkanInputArgs(IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD129"}, IntelSource{Codec: "hevc"})
	joined := strings.Join(args, " ")
	for _, required := range []string{"vaapi=vex:/dev/dri/renderD129", "vulkan=vexvk@vex", "-filter_hw_device vexvk", "-hwaccel_device vex", "-hwaccel_output_format vaapi", "-hwaccel_strict 1"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %s", required, joined)
		}
	}
}

func TestIntelInputRequiresStrictHardwareDecode(t *testing.T) {
	for _, backend := range []string{"vaapi", "qsv"} {
		args := IntelInputArgs(IntelGenerationConfig{Backend: backend}, IntelSource{Codec: "hevc"})
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-hwaccel_strict 1") || !strings.Contains(joined, "-hwaccel_output_format "+backend) {
			t.Fatalf("%s decoding can silently choose software: %s", backend, joined)
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

func TestIntelVulkanExportPrecisionUsesActualDecodedDescriptor(t *testing.T) {
	for _, test := range []struct {
		format string
		depth  int
		want   string
	}{
		{"nv12", 8, "bgra"}, {"p010le", 10, "x2rgb10le"},
		{"yuv422p10le", 10, "x2rgb10le"}, {"decoded-hardware-sw-format", 9, "x2rgb10le"},
		// The actual descriptor overrides even a contradictory format spelling.
		{"yuv420p", 10, "x2rgb10le"}, {"yuv420p10le", 8, "bgra"},
		{"yuv420p12le", 12, ""}, {"p016le", 16, ""}, {"gbrpf32le", 32, ""},
		{"unknown", 0, ""}, {"nv12", -1, ""},
		// Legacy synthetic inputs still have a known precise format description.
		{"yuv422p10le", 0, "x2rgb10le"}, {"yuv420p12le", 0, ""},
	} {
		source := IntelSource{PixelFormat: test.format, BitDepth: test.depth}
		if got := IntelVulkanOutputFormat(source); got != test.want {
			t.Errorf("%s depth%d: got %q, want %q", test.format, test.depth, got, test.want)
		}
		if err := intelValidateVulkanPrecision(source); (err == nil) != (test.want != "") {
			t.Errorf("%s depth%d: precision validation %v", test.format, test.depth, err)
		}
	}
}
