package manager

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/scene/generate"
)

func TestGPUSpriteMetadataUsesRationalRateAndDecodedCount(t *testing.T) {
	info := &generatorInfo{ChunkCount: 81, VideoFile: ffmpeg.VideoFile{FrameCount: 6, VideoStreamDuration: 0.6, VideoStream: &ffmpeg.FFProbeStream{RFrameRate: "10/1", NbFrames: "999"}}}
	if err := configureGPUSpriteInfo(info, true); err != nil {
		t.Fatal(err)
	}
	if info.NumberOfFrames != 6 || info.FrameRate != 10 || info.NthFrame != 0 {
		t.Fatalf("metadata %+v", info)
	}
	info = &generatorInfo{ChunkCount: 81, VideoFile: ffmpeg.VideoFile{FrameRate: math.NaN(), VideoStreamDuration: 6, VideoStream: &ffmpeg.FFProbeStream{RFrameRate: "0/0", NbFrames: "N/A"}}}
	if err := configureGPUSpriteInfo(info, false); err != nil {
		t.Fatal("time-based sheets require only duration", err)
	}
	if info.FrameRate != 0 {
		t.Fatal(info.FrameRate)
	}
}

func TestGPUSpriteManagerForwardsStoredProjection(t *testing.T) {
	for _, slow := range []bool{false, true} {
		for _, mode := range []string{"LR180", "TB360", "MONO360", "FISHEYE190", "UNKNOWN"} {
			dir := t.TempDir()
			probe := filepath.Join(dir, "ffprobe")
			metadata := `{"format":{"start_time":"0"},"streams":[{"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":1920,"height":1080,"sample_aspect_ratio":"1:1"}]}`
			if err := os.WriteFile(probe, []byte("#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffprobe version 8.1.2';exit 0;fi\nprintf '%s' '"+metadata+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			g := &SpriteGenerator{Rows: 9, Columns: 9, ImageOutputPath: filepath.Join(dir, "sheet.jpg"), g: &generate.Generator{Probe: ffmpeg.NewFFProbe(probe), LockManager: fsutil.NewReadLockManager(), IntelSprites: &ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD99999"}}}
			var diagnostic ffmpeg.IntelGenerationDiagnostic
			g.g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostic = d }
			handled, err := g.intelSpriteSheet(context.Background(), spriteRequest{path: filepath.Join(dir, "input"), count: 81, streamDuration: 10, slowSeek: slow, frameCount: 6, vrMode: mode})
			if !handled || err == nil || diagnostic.Actual != "none" {
				t.Fatalf("mode=%s frames=%t handled=%t diagnostic=%+v error=%v", mode, slow, handled, diagnostic, err)
			}
			if mode == "UNKNOWN" {
				if diagnostic.Stage != "plan" || !strings.Contains(diagnostic.Reason, "unsupported GPU VR projection") {
					t.Fatal("stored mode silently bypassed", diagnostic)
				}
			} else if diagnostic.Stage != "device" {
				t.Fatal("supported projection rejected instead of reaching device probe", mode, diagnostic)
			}
			// No encoder is present; a fallback would panic. Neither path may
			// leave the temporary JPEG or concat seek list behind on failure.
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("temporary projected assets leaked", entries)
			}
		}
	}
}

func TestGPUSpriteFrameSamplingRejectsUnusableRate(t *testing.T) {
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		info := &generatorInfo{ChunkCount: 81, VideoFile: ffmpeg.VideoFile{FrameCount: 6, FrameRate: rate, VideoStreamDuration: 0.6, VideoStream: &ffmpeg.FFProbeStream{RFrameRate: "0/0"}}}
		if err := configureGPUSpriteInfo(info, true); err == nil {
			t.Fatalf("accepted unusable frame-sampled VTT rate %v", rate)
		}
	}
	// Frame sampling remains selected when an initial low count is corrected
	// upward; duration-only metadata must not accidentally enable a zero divisor.
	info := &generatorInfo{ChunkCount: 81, VideoFile: ffmpeg.VideoFile{FrameCount: 100, VideoStreamDuration: 6, VideoStream: &ffmpeg.FFProbeStream{RFrameRate: "N/A"}}}
	if err := configureGPUSpriteInfo(info, true); err == nil {
		t.Fatal("accepted unknown frame rate after decoded-count correction")
	}
}
