package ffmpeg

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestGenerationPressureDoesNotMisclassifyDriverOrCorrectnessErrors(t *testing.T) {
	for _, tt := range []struct {
		message  string
		pressure bool
	}{
		{"Cannot allocate memory", true}, {"Failed to allocate surface", true},
		{"vaEndPicture: VA_STATUS_ERROR_DECODING_ERROR (23)", false},
		{"VAAPI encode failed: 24", false}, {"Missing hardware frames", false},
		{"Error while opening encoder", false}, {"Invalid data found when processing input", false},
		{"No space left on device", false}, {"No such filter", false},
	} {
		err := &exec.ExitError{Stderr: []byte(tt.message)}
		got := GenerationPressure(err)
		if generationbudget.IsPressure(got) != tt.pressure || !errors.Is(got, err) {
			t.Fatalf("%s: %v", tt.message, got)
		}
	}
	if !generationbudget.IsPressure(GenerationPressure(syscall.ENOMEM)) {
		t.Fatal("ENOMEM unrecognized")
	}
}
func TestGenerationWorkloadUsesActualPlanAndOperation(t *testing.T) {
	p := IntelGenerationPlan{Config: IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, RuntimeFingerprint: "verified-runtime", InputSource: IntelSource{BitDepth: 10, Codec: "hevc", Profile: "Main10", PixelFormat: "yuv420p10le", Width: 7680, Height: 4320}, Filter: "scale_vaapi=160:90"}
	w := p.GenerationWorkload("sprite/9x9")
	if w.MemoryPerSlot <= 0 || w.GPUPerSlot <= 0 {
		t.Fatal("missing surface estimate")
	}
	small := p
	small.InputSource.Width = 1920
	small.InputSource.Height = 1080
	if got := small.GenerationWorkload("sprite/9x9"); got.Key == w.Key || got.GPUPerSlot >= w.GPUPerSlot {
		t.Fatal("workload dimensions did not affect costs/identity")
	}
	p.Filter = "libplacebo,scale_vaapi=160:90"
	if got := p.GenerationWorkload("sprite/9x9"); got.Key == w.Key || got.GPUPerSlot <= w.GPUPerSlot {
		t.Fatal("HDR/VR filter ignored")
	}
	if p.GenerationWorkload("marker").Key == p.GenerationWorkload("sprite").Key {
		t.Fatal("different output operations share evidence")
	}
}

func TestGenerationWorkloadUsesPhysicalInputAndVerifiedRuntime(t *testing.T) {
	p := IntelGenerationPlan{Config: IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, RuntimeFingerprint: "driver-kernel-ffmpeg-a", InputSource: IntelSource{Codec: "hevc", PixelFormat: "p010le", BitDepth: 10, Width: 7680, Height: 4320}, Source: IntelSource{Codec: "hevc", PixelFormat: "nv12", BitDepth: 8, Width: 1920, Height: 1080}, Filter: "libplacebo,scale_vaapi=160:90"}
	w := p.GenerationWorkload("sprite")
	if w.MemoryPerSlot < 7680*4320*96 || w.RuntimeUnidentified {
		t.Fatal("transformed plan replaced physical resource costs/runtime")
	}
	p.Source.Width, p.Source.Height, p.Source.BitDepth = 160, 90, 8
	if got := p.GenerationWorkload("sprite"); got.Key != w.Key || got.MemoryPerSlot != w.MemoryPerSlot {
		t.Fatal("output geometry changed source costs/identity")
	}
	p.RuntimeFingerprint = "driver-kernel-ffmpeg-b"
	if p.GenerationWorkload("sprite").Key == w.Key {
		t.Fatal("runtime update reused old workload identity")
	}
	p.RuntimeFingerprint = ""
	if !p.GenerationWorkload("sprite").RuntimeUnidentified {
		t.Fatal("incomplete runtime permitted cached evidence")
	}
}
