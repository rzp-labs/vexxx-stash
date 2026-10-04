package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestIntelSpriteSourceIgnoresAudioCountOnly(t *testing.T) {
	video8 := `{"codec_type":"video","codec_name":"h264","profile":"High","pix_fmt":"yuv420p","width":3840,"height":2160,"index":2,"sample_aspect_ratio":"1:1","r_frame_rate":"25/1","avg_frame_rate":"25/1","duration":"1290.400000"}`
	video10 := `{"codec_type":"video","codec_name":"hevc","profile":"Main 10","pix_fmt":"yuv420p10le","width":8192,"height":4096,"index":2,"color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709"}`
	audio := `{"codec_type":"audio","codec_name":"aac","index":0},{"codec_type":"audio","codec_name":"aac","index":1},{"codec_type":"audio","codec_name":"aac","index":3},{"codec_type":"audio","codec_name":"aac","index":4},{"codec_type":"audio","codec_name":"aac","index":5},{"codec_type":"audio","codec_name":"aac","index":6}`
	for _, c := range []struct {
		name, video, backend string
		wantError            bool
	}{
		{"8bit VAAPI", video8, "vaapi", false},
		{"8bit QSV", video8, "qsv", false},
		{"10bit VAAPI", video10, "vaapi", false},
		{"10bit QSV remains rejected", video10, "qsv", true},
		{"multiple video remains rejected", video8 + "," + video8, "vaapi", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixture := `{"streams":[` + audio + "," + c.video + `]}`
			path := filepath.Join(t.TempDir(), "ffprobe")
			if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'JSON'\n"+fixture+"\nJSON\n"), 0700); err != nil {
				t.Fatal(err)
			}
			p := &FFProbe{path: path}
			s, err := p.IntelSpriteSource(context.Background(), "silent-sprite.mp4", c.backend)
			if (err != nil) != c.wantError || (!c.wantError && s.StreamIndex != 2) {
				t.Fatalf("sprite source=%+v err=%v", s, err)
			}
			// Check the audio guard specifically, before codec/pixel validation,
			// so a 10-bit rejection cannot disguise a marker audio regression.
			if _, err := p.IntelSource(context.Background(), "marker.mp4"); err == nil || !strings.Contains(err.Error(), "multiple audio streams") {
				t.Fatalf("marker automatic-audio selection guard changed: %v", err)
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

func TestIntelSpriteSourceKeepsMarkerAndQSVGates(t *testing.T) {
	fixture := `{"streams":[{"codec_type":"video","codec_name":"hevc","profile":"Main 10","pix_fmt":"yuv420p10le","width":8192,"height":4096,"index":0,"color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","sample_aspect_ratio":"1:1","avg_frame_rate":"998386873/16659228","r_frame_rate":"60000/1001","duration":"2669.015617"},{"codec_type":"audio","codec_name":"aac"}]}`
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'JSON'\n"+fixture+"\nJSON\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := &FFProbe{path: path}
	s, err := p.IntelSpriteSource(context.Background(), "actual.mp4", "vaapi")
	if err != nil || s.Profile != "Main 10" || s.Width != 8192 || s.Duration != "2669.015617" {
		t.Fatalf("source=%+v err=%v", s, err)
	}
	if _, err := p.IntelSource(context.Background(), "actual.mp4"); err == nil {
		t.Fatal("marker source broadened")
	}
	if _, err := p.IntelSpriteSource(context.Background(), "actual.mp4", "qsv"); err == nil {
		t.Fatal("QSV source broadened")
	}
}
