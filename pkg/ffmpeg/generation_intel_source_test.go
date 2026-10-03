package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIntelSourceMetadata(t *testing.T) {
	fixture := `{"streams":[{"codec_type":"video","codec_name":"hevc","pix_fmt":"yuv420p","width":1920,"height":1080,"index":0,"color_transfer":"bt709","sample_aspect_ratio":"1:1","avg_frame_rate":"24/1","r_frame_rate":"24/1"}]}`
	for _, c := range []struct {
		name, json string
		fail       bool
	}{
		{"sdr", fixture, false},
		{"10bit", `{"streams":[{"codec_type":"video","codec_name":"hevc","pix_fmt":"yuv420p10le","width":1920,"height":1080}]}`, true},
		{"rotation", `{"streams":[{"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":1920,"height":1080,"side_data_list":[{"rotation":90}]}]}`, true},
		{"multiple_audio", `{"streams":[{"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":1920,"height":1080},{"codec_type":"audio","channels":1},{"codec_type":"audio","channels":2}]}`, true},
		{"multiple", `{"streams":[{"codec_type":"video"},{"codec_type":"video"}]}`, true},
		{"malformed", `{`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ffprobe")
			if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'JSON'\n"+c.json+"\nJSON\n"), 0700); err != nil {
				t.Fatal(err)
			}
			p := &FFProbe{path: path}
			s, err := p.IntelSource(context.Background(), "fixture")
			if (err != nil) != c.fail {
				t.Fatalf("source=%+v err=%v", s, err)
			}
			if !c.fail && (s.SampleAspectRatio != "1:1" || s.Codec != "hevc" || s.ColorTransfer != "bt709") {
				t.Fatal(s)
			}
		})
	}
}

func TestValidateVideoOutputRequiresPackets(t *testing.T) {
	for _, c := range []struct {
		name, json string
		valid      bool
	}{
		{"header_only", `{"streams":[]}`, false},
		{"zero_packets", `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"0"}]}`, false},
		{"unknown_packets", `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"N/A"}]}`, false},
		{"audio_only", `{"streams":[{"codec_type":"audio","codec_name":"aac","nb_read_packets":"10"}]}`, false},
		{"valid_packets", `{"streams":[{"codec_type":"video","codec_name":"h264","nb_read_packets":"1"}]}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ffprobe")
			if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'JSON'\n"+c.json+"\nJSON\n"), 0700); err != nil {
				t.Fatal(err)
			}
			probe := &FFProbe{path: path}
			err := probe.ValidateVideoOutput(context.Background(), "header.mp4")
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v err=%v", c.valid, err)
			}
		})
	}
}

func TestValidateVideoOutputCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := &FFProbe{path: "must-not-execute"}
	if err := probe.ValidateVideoOutput(ctx, "header.mp4"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
