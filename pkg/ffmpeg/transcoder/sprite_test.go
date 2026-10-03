package transcoder

import (
	"reflect"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

func TestSpriteCanonicalTimeSeekCommand(t *testing.T) {
	opts := ScreenshotOptions{OutputPath: "-", OutputType: ScreenshotOutputTypeBMP, Width: 160}
	got := ScreenshotTime("scene.mp4", 3.125, opts)
	want := ffmpeg.Args{"-v", "error", "-y", "-ss", "3.125", "-i", "scene.mp4", "-frames:v", "1", "-vf", "scale=160:-2", "-c:v", "bmp", "-f", "rawvideo", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sprite timestamp seek = %q; want %q", got, want)
	}
}

func TestSpriteCanonicalFrameSeekCommand(t *testing.T) {
	opts := ScreenshotOptions{OutputPath: "-", OutputType: ScreenshotOutputTypeBMP, Width: 160}
	got := ScreenshotFrame("short.mp4", 7, opts)
	want := ffmpeg.Args{"-v", "error", "-y", "-i", "short.mp4", "-frames:v", "1", "-vsync", "0", "-vf", "select=eq(n\\,7),scale=160:-2", "-c:v", "bmp", "-f", "rawvideo", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sprite frame seek = %q; want %q", got, want)
	}
}
