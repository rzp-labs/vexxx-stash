package ffmpeg

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntelRotationPlansKeepResidentOrientationAndDisplayGeometry(t *testing.T) {
	config := IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}
	for _, c := range []struct {
		angle, width, height int
		direction            string
	}{
		{0, 1920, 1080, ""}, {360, 1920, 1080, ""},
		{90, 1080, 1920, "cclock"}, {-270, 1080, 1920, "cclock"},
		{-90, 1080, 1920, "clock"}, {270, 1080, 1920, "clock"},
		{180, 1920, 1080, "reversal"}, {-180, 1920, 1080, "reversal"},
	} {
		source := intelTestSource()
		source.Rotation = c.angle
		source.ColorSpace, source.ColorRange = "bt709", "tv"
		w, h := IntelDisplayDimensions(source)
		if w != c.width || h != c.height {
			t.Fatalf("angle%d display %dx%d", c.angle, w, h)
		}
		rotation, err := IntelRotationFilter(config, source)
		if err != nil {
			t.Fatal(err)
		}
		if (rotation == "") != (c.direction == "") || (c.direction != "" && rotation != "transpose_vaapi=dir="+c.direction+":passthrough=none") {
			t.Fatalf("angle%d filter %s", c.angle, rotation)
		}
		plans := []func() (IntelGenerationPlan, error){
			func() (IntelGenerationPlan, error) {
				return NewIntelGenerationPlan(config, source, "source.mp4", 1.25, 640, false)
			},
			func() (IntelGenerationPlan, error) {
				return NewIntelPreviewPlan(config, source, "source.mp4", 1.25, 640)
			},
			func() (IntelGenerationPlan, error) {
				return NewIntelSpritePlan(config, source, "source.mp4", 1.25, 160)
			},
		}
		for _, build := range plans {
			plan, err := build()
			if err != nil {
				t.Fatal(err)
			}
			if plan.Source.Width != 1920 || plan.Source.Height != 1080 || plan.Source.Rotation != c.angle {
				t.Fatal("physical source metadata changed", plan.Source)
			}
			input := strings.Join(plan.InputArgs, " ")
			if c.angle != 0 && !strings.Contains(input, "-noautorotate -display_rotation 0") {
				t.Fatal(input)
			}
			if rotation != "" && !strings.HasPrefix(plan.Filter, rotation+",") {
				t.Fatal(plan.Filter)
			}
			for _, probe := range plan.Probes {
				args := strings.Join(probe.Args, " ")
				if c.angle != 0 && !strings.Contains(args, "-noautorotate -display_rotation 0") {
					t.Fatal(args)
				}
				if probe.Stage != "decode" && rotation != "" && !strings.Contains(args, "-vf "+rotation+",") {
					t.Fatal(args)
				}
				for _, bad := range []string{"hwdownload", "hwupload", "-f lavfi", "-vf transpose="} {
					if strings.Contains(args, bad) {
						t.Fatal("CPU rotation/probe pixels", args)
					}
				}
			}
		}
		preview, _ := NewIntelPreviewPlan(config, source, "source.mp4", 1.25, 640)
		wantHeight := "360"
		if c.width == 1080 {
			wantHeight = "1138"
		}
		if !strings.Contains(preview.Filter, "scale_vaapi=w=640:h="+wantHeight+":format=nv12:mode=hq:out_color_matrix=bt709:out_range=limited") {
			t.Fatal(preview.Filter)
		}
	}
	for _, angle := range []int{1, 45, -45, 91} {
		source := intelTestSource()
		source.Rotation = angle
		if _, err := IntelRotationFilter(config, source); err == nil {
			t.Fatalf("accepted arbitraryangle%d", angle)
		}
		if _, err := NewIntelPreviewPlan(config, source, "source", 0, 640); err == nil {
			t.Fatalf("accepted arbitraryangle%d plan", angle)
		}
	}
	source := intelTestSource()
	source.Rotation = 90
	if _, err := NewIntelGenerationPlan(IntelGenerationConfig{Backend: "qsv"}, source, "source", 0, 640, false); err == nil {
		t.Fatal("unvalidated QSV rotation accepted")
	}
}

// The CPU oracle establishes FFmpeg display-matrix sign and clears output
// orientation metadata. GPU VPP pixel equivalence is verified on the B580.
func TestIntelRotationMatchesCanonicalFFmpegAutorotation(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	base := filepath.Join(dir, "base.mp4")
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
		return out
	}
	run("-v", "error", "-f", "lavfi", "-i", "testsrc2=size=96x64:rate=1:duration=1", "-c:v", "libx264", "-qp", "1", "-pix_fmt", "yuv420p", "-threads", "1", base)
	for _, c := range []struct{ angle, flip, direction, gpuDirection string }{
		{"90", "", "transpose=cclock", "cclock"}, {"180", "", "hflip,vflip", "reversal"}, {"270", "", "transpose=clock", "clock"},
		{"0", "hflip", "hflip", "hflip"}, {"0", "vflip", "vflip", "vflip"},
		{"90", "hflip", "transpose=clock_flip", "clock_flip"}, {"90", "vflip", "transpose=cclock_flip", "cclock_flip"},
		{"180", "hflip", "vflip", "vflip"}, {"180", "vflip", "hflip", "hflip"},
		{"270", "hflip", "transpose=cclock_flip", "cclock_flip"}, {"270", "vflip", "transpose=clock_flip", "clock_flip"},
	} {
		input := filepath.Join(dir, c.angle+"-"+c.flip+".mp4")
		output := filepath.Join(dir, c.angle+"-"+c.flip+"-pixels.mp4")
		args := []string{"-v", "error", "-display_rotation", c.angle}
		if c.flip != "" {
			args = append(args, "-display_"+c.flip)
		}
		run(append(args, "-i", base, "-c", "copy", input)...)
		// This is the explicit CPU geometry oracle, so its canonical metadata
		// query may use stock FFProbe. Feed the captured JSON to the generation
		// parser without weakening production's mandatory header-only policy.
		metadata, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_streams", "-show_format", "-of", "json", input).Output()
		if err != nil {
			t.Fatal(err)
		}
		metadataPath := filepath.Join(dir, "canonical-metadata.json")
		metadataProbe := filepath.Join(dir, "canonical-metadata-probe")
		if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metadataProbe, []byte("#!/bin/sh\ncat '"+metadataPath+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		source, err := (&FFProbe{path: metadataProbe}).IntelSource(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		rotation, err := IntelRotationFilter(IntelGenerationConfig{Backend: "vaapi"}, source)
		if err != nil {
			t.Fatal(err)
		}
		if source.DisplayMatrix == nil || rotation != "transpose_vaapi=dir="+c.gpuDirection+":passthrough=none" {
			t.Fatalf("matrix orientation %d %s flip%s", source.Rotation, rotation, c.flip)
		}
		if !strings.Contains(strings.Join(IntelInputArgs(IntelGenerationConfig{Backend: "vaapi"}, source), " "), "-noautorotate -display_rotation 0") {
			t.Fatal("reflected matrix could be propagated or autorotated twice")
		}
		canonical := run("-v", "error", "-i", input, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
		explicit := run("-v", "error", "-noautorotate", "-display_rotation", "0", "-i", input, "-frames:v", "1", "-vf", c.direction, "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
		if !bytes.Equal(canonical, explicit) {
			t.Fatalf("angle%s flip%s differs from canonical autorotation", c.angle, c.flip)
		}
		run("-v", "error", "-noautorotate", "-display_rotation", "0", "-i", input, "-frames:v", "1", "-vf", c.direction, "-c:v", "libx264", "-threads", "1", output)
		metadata, err = exec.CommandContext(ctx, probe, "-v", "error", "-show_streams", "-show_format", "-of", "json", output).Output()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
			t.Fatal(err)
		}
		encoded, err := (&FFProbe{path: metadataProbe}).IntelSource(ctx, output)
		if err != nil {
			t.Fatal(err)
		}
		w, h := IntelDisplayDimensions(source)
		if encoded.Rotation != 0 || encoded.Width != w || encoded.Height != h {
			t.Fatalf("double rotation or geometry mismatch: %+v", encoded)
		}
	}
}

func TestIntelDisplayMatrixValidationRejectsUnsupportedTransforms(t *testing.T) {
	identity := [9]int32{65536, 0, 0, 0, 65536, 0, 0, 0, 1 << 30}
	for _, mutate := range []func(*[9]int32){
		func(m *[9]int32) { m[2] = 1 },
		func(m *[9]int32) { m[5] = 1 },
		func(m *[9]int32) { m[8] = 0 },
		func(m *[9]int32) { m[1] = 65536 },
		func(m *[9]int32) { m[0] = 32768 },
		func(m *[9]int32) { m[0], m[4] = 0, 0 },
	} {
		matrix := identity
		mutate(&matrix)
		source := intelTestSource()
		source.DisplayMatrix = &matrix
		if source.Validate() == nil {
			t.Fatal("noncanonical matrix accepted", matrix)
		}
		if _, err := NewIntelSpritePlan(IntelGenerationConfig{Backend: "vaapi"}, source, "input", 0, 160); err == nil {
			t.Fatal("unsupported matrix lost during plan construction", matrix)
		}
	}
	// FFmpeg autorotation ignores matrix translation, retaining only orientation.
	identity[6], identity[7] = 123*65536, -45*65536
	source := intelTestSource()
	source.DisplayMatrix = &identity
	if err := source.Validate(); err != nil {
		t.Fatal(err)
	}
	if rotation, err := IntelRotationFilter(IntelGenerationConfig{Backend: "vaapi"}, source); err != nil || rotation != "" {
		t.Fatalf("translation changed canonical orientation: %s %v", rotation, err)
	}
	for _, value := range []string{"", "0: 1 2 3", "0: 65536 0 0\n1: 0 65536 0\n2: 0 0 2147483648", "0: 65536 0 0\n0: 0 65536 0\n2: 0 0 1073741824"} {
		if _, err := intelParseDisplayMatrix(value); err == nil {
			t.Fatal("malformed matrix accepted", value)
		}
	}
}

func TestIntelPreviewSARPreservesCanonicalDisplayAspect(t *testing.T) {
	for _, c := range []struct {
		source IntelSource
		want   string
	}{
		{IntelSource{Width: 1920, Height: 1080}, ""},
		{IntelSource{Width: 1920, Height: 1080, Rotation: 90}, "setsar=sar=5121/5120:max=2147483647"},
		{IntelSource{Width: 1920, Height: 1080, Rotation: 270}, "setsar=sar=5121/5120:max=2147483647"},
		{IntelSource{Width: 854, Height: 480}, "setsar=sar=1281/1280:max=2147483647"},
	} {
		if got := IntelPreviewSARFilter(c.source, 640); got != c.want {
			t.Fatalf("geometry %+v SAR %q expected %q", c.source, got, c.want)
		}
		c.source.Codec, c.source.PixelFormat = "h264", "yuv420p"
		plan, err := NewIntelPreviewPlan(IntelGenerationConfig{Backend: "vaapi"}, c.source, "input", 0, 640)
		if err != nil || (c.want != "" && !strings.HasSuffix(plan.Filter, ","+c.want)) {
			t.Fatalf("resident filter would lose display aspect: %s %v", plan.Filter, err)
		}
	}
}
