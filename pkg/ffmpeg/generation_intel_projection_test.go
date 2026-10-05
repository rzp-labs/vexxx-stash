package ffmpeg

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIntelProjectedPlansPreserveVRAndHardwareSurfaces(t *testing.T) {
	c := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	s := IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", Width: 8192, Height: 4096, StreamIndex: 2, SampleAspectRatio: "1:1", ColorTransfer: "bt709", ColorPrimaries: "bt709", ColorSpace: "bt709", ColorRange: "tv"}
	for _, mode := range []string{"LR180", "TB360", "MONO360", "FISHEYE190"} {
		for _, jpeg := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/jpeg=%t", mode, jpeg), func(t *testing.T) {
				width := 640
				if jpeg {
					width = 320
				}
				p, err := newIntelProjectionPlan(c, s, "input.mp4", 30.125, width, mode, jpeg)
				if err != nil {
					t.Fatal(err)
				}
				if p.Source.Width != 1280 || p.Source.Height != 720 || p.Source.Rotation != 0 {
					t.Fatal(p.Source)
				}
				for _, part := range []string{"libplacebo=w=1280:h=720:format=x2rgb10le:reset_sar=1:fit_mode=fill:custom_shader_bin=", "hwmap=derive_device=vaapi:mode=read+direct", "format=vaapi", "colorspace=gbr:range=pc", "colorspace=bt709"} {
					if !strings.Contains(p.Filter, part) {
						t.Fatalf("missing %q: %s", part, p.Filter)
					}
				}
				if !strings.Contains(strings.Join(p.InputArgs, " "), "vulkan=vexvk@vex") {
					t.Fatal(p.InputArgs)
				}
				for _, part := range []string{"hwdownload", "hwupload", "v360=", "scale=", "reverse=1", "scale_vulkan="} {
					if strings.Contains(p.Filter, part) {
						t.Fatalf("CPU pixel path %q", part)
					}
				}
				_, data, _ := strings.Cut(p.Filter, "custom_shader_bin=")
				data = strings.SplitN(data, ":", 2)[0]
				shader, err := hex.DecodeString(data)
				if err != nil || !bytes.Contains(shader, []byte("VEX canonical "+mode)) {
					t.Fatalf("embedded shader %v", err)
				}
				if len(p.Probes) != 3 || p.Probes[1].Stage != "projection" {
					t.Fatal(p.Probes)
				}
				for _, step := range p.Probes {
					args := strings.Join(step.Args, " ")
					if !strings.Contains(args, "-ss 30.125") || !strings.Contains(args, "-map 0:2 -an -frames:v 1") {
						t.Fatal(args)
					}
				}
				if jpeg && !strings.Contains(p.Filter, "scale_vaapi=w=320:h=180") {
					t.Fatal("VR JPEG geometry", p.Filter)
				}
				if !jpeg && !strings.Contains(p.Filter, "scale_vaapi=w=640:h=360") {
					t.Fatal("VR MP4 geometry", p.Filter)
				}
			})
		}
	}
}

func TestIntelProjectedRotationAndUnknownMode(t *testing.T) {
	c := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	s := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1080, Height: 1920, Rotation: 90, SampleAspectRatio: "1:1"}
	p, err := NewIntelProjectedPreviewPlan(c, s, "input", 0, 640, "LR180")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Filter, "transpose_vaapi=dir=cclock:passthrough=none,setparams=") {
		t.Fatal(p.Filter)
	}
	shader, err := IntelProjectionShader("LR180", 1920, 1080)
	if err != nil || !strings.Contains(shader, "eye_max = vec2(959.0, 1079.0)") {
		t.Fatalf("wrong rotated first-eye geometry: %v", err)
	}
	if !strings.Contains(p.Filter, "custom_shader_bin="+hex.EncodeToString([]byte(shader))) {
		t.Fatal("projection shader used physical dimensions before rotation")
	}
	if _, err := NewIntelProjectedPreviewPlan(c, s, "input", 0, 640, "UNKNOWN"); err == nil {
		t.Fatal("unknown stored projection silently bypassed")
	}
	if _, err := NewIntelProjectedSpritePlan(IntelGenerationConfig{Backend: "qsv"}, s, "input", 0, 320, "LR180"); err == nil {
		t.Fatal("unsupported backend accepted")
	}
}

func TestIntelProjectedReflectionAppliedOnce(t *testing.T) {
	c := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	matrix := [9]int32{0, 65536, 0, 65536, 0, 0, 0, 0, 1 << 30}
	s := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1080, Height: 1920, SampleAspectRatio: "1:1", DisplayMatrix: &matrix}
	shader, err := IntelProjectionShader("LR180", 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	for _, jpeg := range []bool{false, true} {
		width, height := 640, 360
		if jpeg {
			width, height = 320, 180
		}
		p, err := newIntelProjectionPlan(c, s, "input", 0, width, "LR180", jpeg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.Filter, "transpose_vaapi=dir=cclock_flip:passthrough=none,") || strings.Count(p.Filter, "transpose_vaapi=") != 1 {
			t.Fatal("reflection not applied exactly once", p.Filter)
		}
		if !strings.Contains(p.Filter, "custom_shader_bin="+hex.EncodeToString([]byte(shader))) {
			t.Fatal("projection did not use oriented source dimensions")
		}
		if p.Source.DisplayMatrix != nil || p.Source.Rotation != 0 {
			t.Fatal("projected output retained source orientation", p.Source)
		}
		if w, h := IntelDisplayDimensions(p.Source); w != 1280 || h != 720 {
			t.Fatalf("projected geometry transformed twice: %dx%d", w, h)
		}
		if !strings.Contains(p.Filter, fmt.Sprintf("scale_vaapi=w=%d:h=%d", width, height)) {
			t.Fatal("output resize inherited source reflection", p.Filter)
		}
		if !strings.Contains(strings.Join(p.InputArgs, " "), "-noautorotate -display_rotation 0") {
			t.Fatal("scalar-zero reflection did not disable CPU autorotation", p.InputArgs)
		}
	}
	if s.DisplayMatrix != &matrix {
		t.Fatal("projection changed original source orientation")
	}
}

func TestIntelProjectedRGBBridgeKeepsColorInterpretation(t *testing.T) {
	c := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	s := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, ColorSpace: "bt709", ColorPrimaries: "bt709", ColorTransfer: "bt709", ColorRange: "tv"}
	for _, jpeg := range []bool{false, true} {
		width := 640
		if jpeg {
			width = 320
		}
		p, err := newIntelProjectionPlan(c, s, "input", 0, width, "MONO360", jpeg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.Filter, "libplacebo=w=1280:h=720:format=bgra:") || !strings.Contains(p.Filter, ":colorspace=gbr:range=pc:color_primaries=bt709:color_trc=bt709,") {
			t.Fatal("RGB bridge changed known source transfer or precision", p.Filter)
		}
		if !strings.Contains(p.Filter, "format=vaapi,setparams=range=full:colorspace=bt709:color_primaries=bt709:color_trc=bt709,") {
			t.Fatal("RGB input lacked complete CSC interpretation", p.Filter)
		}
		if p.Source.ColorSpace != "bt709" || p.Source.ColorRange != "tv" || p.Source.ColorTransfer != "bt709" || p.Source.ColorPrimaries != "bt709" {
			t.Fatal("temporary RGB interpretation changed plan output tags", p.Source)
		}
		if jpeg {
			if !strings.Contains(p.Filter, ",setparams=range=full:colorspace=bt709,scale_vaapi=") || strings.Contains(p.Filter, ",setparams=range=limited:colorspace=bt709,scale_vaapi=") {
				t.Fatal("JPEG resize interpreted full-range RGB as limited", p.Filter)
			}
		} else if !strings.Contains(p.Filter, ":out_color_matrix=bt709:out_range=limited,") || !strings.HasSuffix(p.Filter, IntelOutputColorTags(p.Source)) {
			t.Fatal("MP4 conversion did not restore actual output interpretation", p.Filter)
		}
	}
}

func TestIntelProjectedHDRChangesTagsOnlyWithConversion(t *testing.T) {
	c := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	s := IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "yuv420p10le", Width: 8192, Height: 4096, SampleAspectRatio: "1:1", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", ColorRange: "tv"}
	p, err := NewIntelProjectedPreviewPlan(c, s, "input", 0, 640, "LR180")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.ColorSpace != "bt709" || p.Source.ColorPrimaries != "bt709" || p.Source.ColorTransfer != "bt709" || !strings.Contains(p.Filter, IntelHDRFilterOptions()) {
		t.Fatal("HDR retag without conversion", p.Source, p.Filter)
	}
	s.ColorPrimaries = "unknown"
	if _, err := NewIntelProjectedPreviewPlan(c, s, "input", 0, 640, "LR180"); err == nil {
		t.Fatal("ambiguous HDR accepted")
	}
}

func TestIntelProjectedUnknownSDRMatchesCanonicalMatrixAndRange(t *testing.T) {
	c := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	s := IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, SampleAspectRatio: "1:1"}
	p, err := NewIntelProjectedPreviewPlan(c, s, "input", 0, 640, "MONO360")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Filter, "setparams=colorspace=bt470bg:range=limited,libplacebo=") || p.Source.ColorSpace != "bt470bg" || p.Source.ColorRange != "tv" {
		t.Fatal(p.Source, p.Filter)
	}
	_, rgbStage, _ := strings.Cut(p.Filter, "libplacebo=")
	rgbStage = strings.SplitN(rgbStage, ",", 2)[0]
	if strings.Contains(rgbStage, "color_trc=") || strings.Contains(rgbStage, "color_primaries=") || strings.Contains(p.Filter, "tonemapping=") {
		t.Fatal("invented libplacebo transfer/primaries or HDR conversion", p.Filter)
	}
	if !strings.HasSuffix(p.Filter, "setparams=range=limited:colorspace=bt470bg:color_primaries=unknown:color_trc=unknown") {
		t.Fatal("temporary RGB CSC tags leaked into output", p.Filter)
	}
	unsupported := s
	unsupported.ColorSpace = "fcc"
	if _, err := NewIntelProjectedPreviewPlan(c, unsupported, "input", 0, 640, "MONO360"); err == nil {
		t.Fatal("declared unsupported matrix silently guessed")
	}
	s.PixelFormat = "yuvj420p"
	p, err = NewIntelProjectedSpritePlan(c, s, "input", 0, 320, "LR180")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Filter, "setparams=colorspace=bt470bg:range=full,libplacebo=") || p.Source.ColorRange != "pc" {
		t.Fatal(p.Source, p.Filter)
	}
}

// Verify the shader's actual numeric ray constants and mapping against a real
// independent v360 control. A 16-bit linear coordinate ramp exposes incorrect
// FOV, eye, axis or half-pixel conventions without involving JPEG quality.
func TestIntelProjectionCoordinatesMatchV360Controls(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable for independent v360 geometry control")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inventory, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-filters").Output()
	if err != nil || !bytes.Contains(inventory, []byte(" v360 ")) {
		t.Skip("local ffmpeg has no v360 geometry control")
	}
	const w, h = 256, 128
	points := [][2]int{{0, 0}, {1279, 0}, {0, 719}, {1279, 719}, {640, 360}, {127, 239}, {937, 517}}
	for _, mode := range []string{"LR180", "TB360", "MONO360", "FISHEYE190"} {
		shader, err := IntelProjectionShader(mode, w, h)
		if err != nil {
			t.Fatal(err)
		}
		coeff := regexp.MustCompile(`screen \* vec2\(([^,]+), ([^)]+)\)`).FindStringSubmatch(shader)
		if len(coeff) != 3 {
			t.Fatal("shader ray constants missing")
		}
		xscale, _ := strconv.ParseFloat(coeff[1], 64)
		yscale, _ := strconv.ParseFloat(coeff[2], 64)
		input := "equirect:in_stereo=2d"
		switch mode {
		case "LR180":
			input = "hequirect:in_stereo=sbs"
		case "TB360":
			input = "equirect:in_stereo=tb"
		case "FISHEYE190":
			input = "fisheye:ih_fov=190:iv_fov=190:in_stereo=sbs"
		}
		for axis := 0; axis < 2; axis++ {
			ramp := make([]byte, w*h*2)
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					value := float64(x) / float64(w-1)
					if axis == 1 {
						value = float64(y) / float64(h-1)
					}
					binary.LittleEndian.PutUint16(ramp[(y*w+x)*2:], uint16(math.Round(value*65535)))
				}
			}
			path := filepath.Join(t.TempDir(), "ramp.gray16")
			if err := os.WriteFile(path, ramp, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-threads", "1", "-filter_threads", "1", "-f", "rawvideo", "-pixel_format", "gray16le", "-video_size", "256x128", "-i", path, "-vf", "v360=input="+input+":output=flat:out_stereo=2d:d_fov=120:w=1280:h=720", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray16le", "pipe:1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			control, err := cmd.Output()
			if err != nil {
				t.Fatalf("v360 %s: %v %s", mode, err, stderr.String())
			}
			if len(control) != 1280*720*2 {
				t.Fatal("control geometry", len(control))
			}
			for _, point := range points {
				px, py := projectionShaderPixel(mode, w, h, point[0], point[1], xscale, yscale)
				value := px / float64(w-1)
				if axis == 1 {
					value = py / float64(h-1)
				}
				want := value * 65535
				got := float64(binary.LittleEndian.Uint16(control[(point[1]*1280+point[0])*2:]))
				// v360 quantizes bilinear coefficients to signed 16-bit weights.
				// Eight ramp code values cover coefficient/sample rounding; this
				// is a geometry control, not a media-quality acceptance threshold.
				if math.Abs(got-want) > 8 {
					t.Errorf("%s axis%d output%v source(%.6f,%.6f): v360=%g shader=%g", mode, axis, point, px, py, got, want)
				}
			}
		}
	}
}

func projectionShaderPixel(mode string, width, height, x, y int, xscale, yscale float64) (float64, float64) {
	rx := (float64(2*x+1)/1280 - 1) * xscale
	ry := (float64(2*y+1)/720 - 1) * yscale
	norm := math.Sqrt(rx*rx + ry*ry + 1)
	rx, ry, rz := rx/norm, ry/norm, 1/norm
	u, v := .5+math.Atan2(rx, rz)/(2*math.Pi), .5+math.Asin(ry)/math.Pi
	switch mode {
	case "LR180":
		width /= 2
		u = .5 + math.Atan2(rx, rz)/math.Pi
	case "TB360":
		height /= 2
	case "FISHEYE190":
		width /= 2
		radius := math.Hypot(rx, ry)
		u, v = .5, .5
		if radius > 0 {
			angle := math.Atan2(radius, rz) / (190 * math.Pi / 180)
			u += rx / radius * angle
			v += ry / radius * angle
		}
	}
	return u * float64(width-1), v * float64(height-1)
}
