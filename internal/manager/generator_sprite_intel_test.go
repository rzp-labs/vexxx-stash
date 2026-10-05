package manager

import (
	"math"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

func TestGPUSpriteMetadataUsesRationalRateAndDecodedCount(t *testing.T) {
	info := &generatorInfo{ChunkCount: 81, VideoFile: ffmpeg.VideoFile{FrameCount: 6, VideoStreamDuration: 0.6, VideoStream: &ffmpeg.FFProbeStream{RFrameRate: "10/1", NbFrames: "999"}}}
	if err := configureGPUSpriteInfo(info); err != nil {
		t.Fatal(err)
	}
	if info.NumberOfFrames != 6 || info.FrameRate != 10 || info.NthFrame != 0 {
		t.Fatalf("metadata %+v", info)
	}
	info = &generatorInfo{ChunkCount: 81, VideoFile: ffmpeg.VideoFile{FrameRate: math.NaN(), VideoStreamDuration: 6, VideoStream: &ffmpeg.FFProbeStream{RFrameRate: "0/0", NbFrames: "N/A"}}}
	if err := configureGPUSpriteInfo(info); err != nil {
		t.Fatal("time-based sheets require only duration", err)
	}
	if info.FrameRate != 0 {
		t.Fatal(info.FrameRate)
	}
}
