package generate

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"golang.org/x/image/bmp"
)

type spriteCancelImage struct {
	image.Image
	cancel context.CancelFunc
}

func (i spriteCancelImage) Bounds() image.Rectangle {
	i.cancel()
	return i.Image.Bounds()
}

func TestSaveSpriteAtomicOutputAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "encode failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output := filepath.Join(dir, "sprite.png")
			if mode == "encode failure" {
				output = filepath.Join(dir, "sprite.invalid")
			}
			prior := []byte("existing output")
			if err := os.WriteFile(output, prior, 0600); err != nil {
				t.Fatal(err)
			}
			budget, err := generationbudget.New(generationbudget.Settings{})
			if err != nil {
				t.Fatal(err)
			}
			g := Generator{Budget: budget}
			var tile image.Image = image.NewNRGBA(image.Rect(0, 0, 160, 90))
			if mode == "cancel" {
				tile = spriteCancelImage{Image: tile, cancel: cancel}
			}
			err = g.SaveSprite(ctx, []image.Image{tile}, output)
			data, readErr := os.ReadFile(output)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				f, err := os.Open(output)
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := png.DecodeConfig(f)
				f.Close()
				if err != nil || cfg.Width != 1440 || cfg.Height != 810 {
					t.Fatalf("published config=%+v error=%v", cfg, err)
				}
			} else if err == nil || !reflect.DeepEqual(data, prior) {
				t.Fatalf("failure changed prior output: error=%v data=%q", err, data)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(output) {
				t.Fatalf("temp files=%v error=%v", files, err)
			}
			release, err := budget.Acquire(context.Background(), generationbudget.CPU)
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestSaveSpriteRenameFailurePreservesDestinationAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "sprite.png")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(output, "existing")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	err := (Generator{}).SaveSprite(context.Background(), []image.Image{image.NewNRGBA(image.Rect(0, 0, 160, 90))}, output)
	if err == nil {
		t.Fatal("rename to directory should fail")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("prior destination damaged: %q %v", data, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "sprite.png" {
		t.Fatalf("temporary output leaked: %v %v", files, err)
	}
}

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
