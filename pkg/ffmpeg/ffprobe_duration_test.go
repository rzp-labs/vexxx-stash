package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseVideoDurationUsesFinitePositiveMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, stream, container string
		want                    float64
	}{
		{"stream preferred", "10.125", "100", 10.125},
		{"valid stream without container", "10.125", "N/A", 10.125},
		{"missing stream", "", "12.345", 12.35},
		{"unavailable stream", "N/A", "12.345", 12.35},
		{"zero stream", "0", "12.345", 12.35},
		{"negative stream", "-1", "12.345", 12.35},
		{"nan stream", "NaN", "12.345", 12.35},
		{"infinite stream", "+Inf", "12.345", 12.35},
		{"negative infinite stream", "-Inf", "12.345", 12.35},
		{"missing both", "N/A", "N/A", 0},
		{"zero container", "NaN", "0", 0},
		{"negative container", "0", "-10", 0},
		{"nan container", "N/A", "NaN", 0},
		{"infinite container", "0", "+Inf", 0},
		{"negative infinite container", "-1", "-Inf", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := &FFProbeJSON{Streams: []FFProbeStream{{CodecType: "video", Duration: tc.stream}}}
			metadata.Format.Duration = tc.container
			video, err := parse(path, metadata)
			if err != nil {
				t.Fatal(err)
			}
			if video.VideoStreamDuration != tc.want {
				t.Fatalf("stream=%q container=%q: video duration=%v, want %v", tc.stream, tc.container, video.VideoStreamDuration, tc.want)
			}
		})
	}
}
