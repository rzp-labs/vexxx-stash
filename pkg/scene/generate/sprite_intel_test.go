package generate

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"golang.org/x/image/bmp"
)

func TestIntelSpriteEligibilityConservative(t *testing.T) {
	valid := ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, SampleAspectRatio: "1:1", FrameRate: "30/1", AverageFrameRate: "60/2", Duration: "10"}
	if err := intelSpriteEligibility(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*ffmpeg.IntelSource)
	}{
		{"short", func(s *ffmpeg.IntelSource) { s.Duration = "4.9" }},
		{"duration unknown", func(s *ffmpeg.IntelSource) { s.Duration = "N/A" }},
		{"VFR", func(s *ffmpeg.IntelSource) { s.AverageFrameRate = "29/1" }},
		{"frame rate unknown", func(s *ffmpeg.IntelSource) { s.FrameRate = "0/0" }},
		{"rotation", func(s *ffmpeg.IntelSource) { s.Rotation = 90 }},
		{"10-bit", func(s *ffmpeg.IntelSource) { s.PixelFormat = "yuv420p10le" }},
		{"HDR", func(s *ffmpeg.IntelSource) { s.ColorTransfer = "smpte2084" }},
		{"anamorphic", func(s *ffmpeg.IntelSource) { s.SampleAspectRatio = "4:3" }},
		{"SAR unknown", func(s *ffmpeg.IntelSource) { s.SampleAspectRatio = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.change(&s)
			if err := intelSpriteEligibility(s); err == nil {
				t.Fatal("unsupported input accepted")
			}
		})
	}
}

func TestSpriteSequenceOrderFailureAndCancellation(t *testing.T) {
	times := []float64{2.5, 3.125, 5.75}
	var seen []float64
	images, err := captureSpriteSequence(context.Background(), times, 160, 90, func(_ context.Context, at float64) (image.Image, error) {
		seen = append(seen, at)
		return image.NewNRGBA(image.Rect(0, 0, 160, 90)), nil
	})
	if err != nil || len(images) != len(times) || !reflect.DeepEqual(seen, times) {
		t.Fatalf("images=%d timestamps=%v error=%v", len(images), seen, err)
	}
	for _, mode := range []string{"failure", "size", "nil", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			images, err := captureSpriteSequence(ctx, times, 160, 90, func(_ context.Context, _ float64) (image.Image, error) {
				calls++
				if calls == 2 {
					switch mode {
					case "failure":
						return nil, errors.New("decode failed")
					case "size":
						return image.NewNRGBA(image.Rect(0, 0, 160, 88)), nil
					case "nil":
						return nil, nil
					case "cancel":
						cancel()
					}
				}
				return image.NewNRGBA(image.Rect(0, 0, 160, 90)), nil
			})
			if err == nil || images != nil || calls != 2 {
				t.Fatalf("partial images=%d calls=%d error=%v", len(images), calls, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestIntelSpriteFallbackDoesNotCancelSheetAfterFirstTile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses POSIX shell")
	}
	dir := t.TempDir()
	tilePath := filepath.Join(dir, "tile.bmp")
	f, err := os.Create(tilePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := bmp.Encode(f, image.NewNRGBA(image.Rect(0, 0, 160, 90))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ncat '"+tilePath+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var diagnostics []ffmpeg.IntelGenerationDiagnostic
	g := Generator{Encoder: ffmpeg.NewEncoder(command), LockManager: fsutil.NewReadLockManager(), IntelSprites: &ffmpeg.IntelGenerationConfig{Backend: "qsv", Device: "/dev/dri/renderD128"}, IntelDiagnostic: func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }}
	images, d, err := g.IntelSpriteTiles(context.Background(), "input.mp4", []float64{0, 1})
	if err != nil || len(images) != 2 {
		t.Fatalf("tiles=%d error=%v", len(images), err)
	}
	if d.Selected != "qsv" || d.Actual != "software" || d.Stage != "metadata" || len(diagnostics) != 1 || diagnostics[0] != d {
		t.Fatalf("diagnostic=%+v callbacks=%+v", d, diagnostics)
	}
}
