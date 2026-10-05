package generate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

// Actual missing-SAR metadata must preserve the canonical tile dimensions.
// GPU pixel quality is verified on hardware, not by a CPU projection of VPP.
func TestIntelUnspecifiedSARCanonicalGeometry(t *testing.T) {
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
	input := filepath.Join(t.TempDir(), "unset-sar.mp4")
	cmd := exec.CommandContext(ctx, binary, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=5:duration=6", "-vf", "setsar=0", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	// The stock local binary is a CPU reference, not the patched production
	// header-only probe. Feed its recorded metadata to the strict parser.
	metadata, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_streams", "-show_format", "-of", "json", input).Output()
	if err != nil {
		t.Fatal(err)
	}
	fixtureProbe := filepath.Join(t.TempDir(), "ffprobe")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffprobe version 8.1';exit 0;fi\ncat <<'METADATA'\n" + string(metadata) + "\nMETADATA\n"
	if err := os.WriteFile(fixtureProbe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	source, err := ffmpeg.NewFFProbe(fixtureProbe).IntelSpriteSource(ctx, input, "vaapi")
	if err != nil {
		t.Fatal(err)
	}
	if source.SampleAspectRatio != "" && source.SampleAspectRatio != "N/A" && source.SampleAspectRatio != "0:1" {
		t.Fatal("fixture acquired SAR", source.SampleAspectRatio)
	}
	if err := intelSpriteEligibility(source, "vaapi"); err != nil {
		t.Fatal(err)
	}
	plan, err := ffmpeg.NewIntelSpritePlan(ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, source, input, 1.125, 160)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Filter, "scale_vaapi=w=160:h=90:") {
		t.Fatal(plan.Filter)
	}
}
