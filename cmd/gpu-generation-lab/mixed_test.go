package main

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/scene/generate"
)

func TestOwnMediaStatScope(t *testing.T) {
	for _, tc := range []struct {
		stat string
		want bool
	}{
		{"10 (ffmpeg) S 42 0", true},
		{"10 (ffprobe) S 42 0", true},
		{"10 (ffmpeg) S 43 0", false},
		{"10 (unrelated) S 42 0", false},
		{"10 (ffmpeg with spaces) S 42 0", false},
		{"10 (ffmpeg) S", false},
		{"10 ffmpeg S 42", false},
	} {
		if got := isOwnMediaStat(tc.stat, 42); got != tc.want {
			t.Errorf("%q = %v; want%v", tc.stat, got, tc.want)
		}
	}
}

func TestMixedFixtureBounds(t *testing.T) {
	valid := ffmpeg.IntelSource{Duration: "10.000000", Width: 640, Height: 360, FrameRate: "30/1", AverageFrameRate: "30/1", PixelFormat: "yuv420p"}
	if err := mixedFixtureContract(valid, true); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ffmpeg.IntelSource){
		func(s *ffmpeg.IntelSource) { s.Duration = "NaN" },
		func(s *ffmpeg.IntelSource) { s.Duration = "+Inf" },
		func(s *ffmpeg.IntelSource) { s.Duration = "100" },
		func(s *ffmpeg.IntelSource) { s.Width = 1920; s.Height = 1080 },
		func(s *ffmpeg.IntelSource) { s.Rotation = 90 },
		func(s *ffmpeg.IntelSource) { s.PixelFormat = "yuv420p10le" },
		func(s *ffmpeg.IntelSource) { s.AverageFrameRate = "29/1" },
	} {
		s := valid
		mutate(&s)
		if err := mixedFixtureContract(s, true); err == nil {
			t.Errorf("unbounded/unsupported hash fixture accepted: %+v", s)
		}
	}
}

func TestMixedPreCancelledDoesNotProbeOrAcquire(t *testing.T) {
	budget, _ := generationbudget.New(generationbudget.Settings{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := generate.Generator{Budget: budget} // Nil probe would panic if called.
	if _, err := runMixed(ctx, &g, labPaths{t.TempDir()}, "fixture", "hash-fixture", t.TempDir()); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	release, err := budget.Acquire(context.Background(), generationbudget.CPU)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestMixedVTTValidatesRealGeneratorAndRejectsDrift(t *testing.T) {
	dir := t.TempDir()
	budget, _ := generationbudget.New(generationbudget.Settings{})
	g := generate.Generator{Budget: budget, ScenePaths: labScenePaths{dir}, LockManager: fsutil.NewReadLockManager()}
	images := make([]image.Image, 81)
	for i := range images {
		images[i] = image.NewNRGBA(image.Rect(0, 0, 160, 90))
	}
	sprite := filepath.Join(dir, "sprite.jpg")
	if err := g.SaveSprite(context.Background(), images, sprite); err != nil {
		t.Fatal(err)
	}
	vtt := filepath.Join(dir, "sprite.vtt")
	if err := g.SpriteVTT(context.Background(), vtt, sprite, 2.0/81, 1); err != nil {
		t.Fatal(err)
	}
	if err := validateMixedVTT(vtt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(vtt)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "#xywh=0,0,160,90", "#xywh=160,0,160,90", 1))
	if err := os.WriteFile(vtt, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateMixedVTT(vtt); err == nil {
		t.Fatal("coordinate drift accepted")
	}
}
