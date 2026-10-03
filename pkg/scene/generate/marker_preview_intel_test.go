package generate

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
)

func TestMarkerIntelCommandPreservesContract(t *testing.T) {
	for _, backend := range []string{"vaapi", "qsv"} {
		for _, audio := range []bool{false, true} {
			source := ffmpeg.IntelSource{Codec: "hevc", PixelFormat: "yuv420p", Width: 1920, Height: 1080, StreamIndex: 0}
			p, err := ffmpeg.NewIntelGenerationPlan(ffmpeg.IntelGenerationConfig{Backend: backend, Device: "/dev/dri/renderD128"}, source, "in.mp4", 3.25, 640, false)
			if err != nil {
				t.Fatal(err)
			}
			a := strings.Join(markerIntelArgs("in.mp4", "tmp.mp4", sceneMarkerOptions{Seconds: 3.25, Duration: 7.5, Audio: audio}, p), " ")
			for _, want := range []string{"-ss 3.25", "-t 7.5", "-c:v h264_" + backend, "-profile:v high", "-level:v 4.2", "scale_" + backend + "=w=640:h=360:format=nv12", "-movflags +faststart", "-map 0:0", "tmp.mp4"} {
				if !strings.Contains(a, want) {
					t.Fatalf("missing %q: %s", want, a)
				}
			}
			if strings.Contains(a, "hwdownload") || strings.Contains(a, "-crf") || strings.Contains(a, "-pix_fmt yuv420p") {
				t.Fatalf("CPU transfer or incompatible quality: %s", a)
			}
			if audio {
				if !strings.Contains(a, "-c:a aac") || !strings.Contains(a, "-b:a 64k") || !strings.Contains(a, "-map 0:a:0?") {
					t.Fatal(a)
				}
			} else if !strings.Contains(a, "-an") {
				t.Fatal(a)
			}
		}
	}
}

func TestMarkerExistingOutputAndInvalidTiming(t *testing.T) {
	p := markerTestPaths{t.TempDir()}
	output := p.GetVideoPreviewPath("", 0)
	if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	g := Generator{LockManager: fsutil.NewReadLockManager(), MarkerPaths: p, IntelMarker: &ffmpeg.IntelGenerationConfig{Backend: "qsv"}}
	if err := g.MarkerPreviewVideo(context.Background(), "input", "hash", 0, nil, false, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(output)
	if string(data) != "existing" {
		t.Fatal(string(data))
	}
	g.Overwrite = true
	zero := -1.0
	for _, start := range []float64{-1, math.NaN(), math.Inf(1), 0} {
		if err := g.MarkerPreviewVideo(context.Background(), "input", "hash", start, &zero, false, ""); err == nil {
			t.Fatal("invalid timing accepted")
		}
	}
}

func TestMarkerFailedFallbackKeepsExistingOutput(t *testing.T) {
	p := markerTestPaths{t.TempDir()}
	output := p.GetVideoPreviewPath("", 0)
	os.WriteFile(output, []byte("existing"), 0600)
	binary := filepath.Join(t.TempDir(), "ffmpeg")
	// A partial failed software attempt must never replace the final file.
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.1'; exit 0; fi\nfor last do :; done\nprintf partial > \"$last\"\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	g := Generator{Encoder: ffmpeg.NewEncoder(binary), LockManager: fsutil.NewReadLockManager(), MarkerPaths: p, Overwrite: true, IntelMarker: &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}}
	var diagnostics []ffmpeg.IntelGenerationDiagnostic
	g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }
	if err := g.MarkerPreviewVideo(context.Background(), "input", "hash", 0, nil, false, ""); err == nil {
		t.Fatal("failed attempt succeeded")
	}
	data, _ := os.ReadFile(output)
	if string(data) != "existing" {
		t.Fatal(string(data))
	}
	entries, _ := os.ReadDir(p.dir)
	if len(entries) != 1 {
		t.Fatalf("temporary artifacts leaked: %v", entries)
	}
	if len(diagnostics) != 1 || diagnostics[0].Actual != "software" || diagnostics[0].Stage != "metadata" {
		t.Fatal(diagnostics)
	}
}

func TestMarkerCancelledDoesNotFallback(t *testing.T) {
	p := markerTestPaths{t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := Generator{LockManager: fsutil.NewReadLockManager(), MarkerPaths: p, IntelMarker: &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}}
	// No encoder is deliberately provided: any software retry would panic.
	err := g.MarkerPreviewVideo(ctx, "input", "hash", 0, nil, false, "")
	if err == nil {
		t.Fatal("cancelled marker succeeded")
	}
	if entries, _ := os.ReadDir(p.dir); len(entries) != 0 {
		t.Fatal("temporary file leaked")
	}
}

func TestIntelRequestedMarkerRejectsHeaderOnlyFallback(t *testing.T) {
	for _, c := range []struct {
		name, source, vr string
		valid            bool
	}{
		{"metadata", `{"streams":[]}`, "", false},
		{"vr", `{"streams":[]}`, "LR180", false},
		{"eligibility", `{"streams":[{"codec_type":"video","codec_name":"hevc","pix_fmt":"yuv420p10le","width":1920,"height":1080}]}`, "", false},
		{"device", `{"streams":[{"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":1920,"height":1080,"sample_aspect_ratio":"1:1"}]}`, "", false},
		{"valid_cpu_fallback", `{"streams":[]}`, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := markerTestPaths{t.TempDir()}
			output := p.GetVideoPreviewPath("", 0)
			if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			binary := filepath.Join(binDir, "ffmpeg")
			counter := filepath.Join(binDir, "calls")
			script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.1'; exit 0; fi\nprintf x >> '" + counter + "'\nfor last do :; done\nprintf header-only > \"$last\"\nexit 0\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			packetJSON := `{"streams":[]}`
			if c.valid {
				packetJSON = `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"1"}]}`
			}
			probeBinary := filepath.Join(binDir, "ffprobe")
			probeScript := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffprobe version 7.1'; exit 0; fi\nfor arg do\nif [ \"$arg\" = '-count_packets' ]; then\ncat <<'PACKETS'\n" + packetJSON + "\nPACKETS\nexit 0\nfi\ndone\ncat <<'SOURCE'\n" + c.source + "\nSOURCE\n"
			if err := os.WriteFile(probeBinary, []byte(probeScript), 0700); err != nil {
				t.Fatal(err)
			}
			g := Generator{Encoder: ffmpeg.NewEncoder(binary), Probe: ffmpeg.NewFFProbe(probeBinary), LockManager: fsutil.NewReadLockManager(), MarkerPaths: p, Overwrite: true, IntelMarker: &ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD99999"}}
			var diagnostics []ffmpeg.IntelGenerationDiagnostic
			g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }
			err := g.MarkerPreviewVideo(context.Background(), "input", "hash", 10, nil, false, c.vr)
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v err=%v", c.valid, err)
			}
			data, _ := os.ReadFile(output)
			if !c.valid && string(data) != "existing" {
				t.Fatalf("published header-only artifact: %q", data)
			}
			calls, _ := os.ReadFile(counter)
			if string(calls) != "x" {
				t.Fatalf("fallback retried: %q", calls)
			}
			entries, _ := os.ReadDir(p.dir)
			if len(entries) != 1 {
				t.Fatalf("temporary output leaked: %v", entries)
			}
			if len(diagnostics) != 1 || diagnostics[0].Actual != "software" || (!c.valid && diagnostics[0].Stage != "output") {
				t.Fatal(diagnostics)
			}
		})
	}
}

func TestQSVMarkerQualityGate(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ffmpeg")
	counter := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 7.1'; exit 0; fi\nprintf x >> '" + counter + "'\nfor last do :; done\nprintf output > \"$last\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	probeBinary := filepath.Join(dir, "ffprobe")
	probeScript := `#!/bin/sh
if [ "$1" = '-version' ]; then echo 'ffprobe version 7.1'; exit 0; fi
for arg do
if [ "$arg" = '-count_packets' ]; then
printf '%s' '{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"1"}]}'
exit 0
fi
done
exit 99
`
	if err := os.WriteFile(probeBinary, []byte(probeScript), 0700); err != nil {
		t.Fatal(err)
	}
	p := markerTestPaths{t.TempDir()}
	g := Generator{Encoder: ffmpeg.NewEncoder(binary), Probe: ffmpeg.NewFFProbe(probeBinary), LockManager: fsutil.NewReadLockManager(), MarkerPaths: p, IntelMarker: &ffmpeg.IntelGenerationConfig{Backend: "qsv", Device: "/dev/dri/renderD128"}}
	var diagnostics []ffmpeg.IntelGenerationDiagnostic
	g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }
	if err := g.MarkerPreviewVideo(context.Background(), "input", "hash", 0, nil, false, ""); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(counter)
	if string(calls) != "x" {
		t.Fatalf("unexpected GPU/probe attempt: %q", calls)
	}
	if len(diagnostics) != 1 || diagnostics[0].Stage != "quality" || diagnostics[0].Selected != "qsv" || diagnostics[0].Actual != "software" || !strings.Contains(diagnostics[0].Reason, "representative visual acceptance") {
		t.Fatal(diagnostics)
	}
}
