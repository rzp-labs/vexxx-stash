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
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
)

func TestIntelSpriteEligibilityGPU(t *testing.T) {
	source := ffmpeg.IntelSource{Codec: "h264", PixelFormat: "yuv420p", Width: 1920, Height: 1080, SampleAspectRatio: "1:1", FrameRate: "30/1", AverageFrameRate: "29/1", Duration: "10"}
	if err := intelSpriteEligibility(source, "vaapi"); err != nil {
		t.Fatal("timestamp-based VFR seeking rejected", err)
	}
	for _, sar := range []string{"", "N/A", "0:1", "1:1"} {
		s := source
		s.SampleAspectRatio = sar
		if err := intelSpriteEligibility(s, "vaapi"); err != nil {
			t.Fatal(sar, err)
		}
	}
	for _, change := range []func(*ffmpeg.IntelSource){func(s *ffmpeg.IntelSource) { s.Rotation = 90 }, func(s *ffmpeg.IntelSource) { s.SampleAspectRatio = "4:3" }, func(s *ffmpeg.IntelSource) { s.ColorTransfer = "smpte2084" }} {
		s := source
		change(&s)
		if err := intelSpriteEligibility(s, "vaapi"); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestSpriteSequenceOrderFailureAndCancellation(t *testing.T) {
	times := []float64{2.5, 3.125, 5.75}
	var seen []float64
	images, err := captureSpriteSequence(context.Background(), times, 1, 160, 90, func(_ context.Context, at float64) (image.Image, error) {
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
			images, err := captureSpriteSequence(ctx, times, 1, 160, 90, func(_ context.Context, _ float64) (image.Image, error) {
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

func TestIntelSpriteTileAPIRefusesGPUDownloads(t *testing.T) {
	for _, backend := range []string{"vaapi", "qsv"} {
		var called int
		g := Generator{IntelSprites: &ffmpeg.IntelGenerationConfig{Backend: backend}, IntelDiagnostic: func(ffmpeg.IntelGenerationDiagnostic) { called++ }}
		images, d, err := g.IntelSpriteTiles(context.Background(), "input.mp4", []float64{0, 1})
		if err == nil || images != nil || d.Actual != "none" || called != 1 {
			t.Fatalf("images=%v diagnostic=%+v error=%v callbacks=%d", images, d, err, called)
		}
	}
}

func TestIntelSpriteMetadataDeletionWaitsForOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixture")
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	released := filepath.Join(dir, "released")
	binary := filepath.Join(dir, "ffprobe")
	script := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffprobe version 7.1'; exit 0; fi\n(sleep 0.3; : > '" + released + "') &\n: > '" + started + "'\nexec sleep 30\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "synthetic.mp4")
	locks := fsutil.NewReadLockManager()
	ownerCleaned := make(chan struct{})
	g := Generator{Probe: ffmpeg.NewFFProbe(binary), LockManager: locks, IntelSprites: &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, IntelDiagnostic: func(ffmpeg.IntelGenerationDiagnostic) { close(ownerCleaned) }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := g.IntelSpriteSheet(ctx, input, []float64{0, 1}, 9, 9, filepath.Join(dir, "out.jpg"))
		returned <- err
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("metadata fixture did not start")
		}
		runtime.Gosched()
	}
	locks.Cancel(input)
	if _, err := os.Stat(released); err != nil {
		t.Error("deletion returned before process pipes released")
	}
	select {
	case <-ownerCleaned:
	case <-ctx.Done():
		t.Fatal("owner cleanup timed out")
	}
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("cancelled metadata succeeded")
		}
	case <-ctx.Done():
		t.Fatal("metadata did not return")
	}
}
