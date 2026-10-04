package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func markerFixture(t *testing.T, rate string, duration float64) (string, string, string) {
	t.Helper()
	encoder, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	path := filepath.Join(t.TempDir(), "marker.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-f", "lavfi", "-i", "color=c=red:s=640x360:r=" + rate, "-t", fmt.Sprint(duration), "-an", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", path}
	if output, err := exec.CommandContext(ctx, encoder, args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, output)
	}
	return path, probe, encoder
}

func TestMarkerValidationRealFrameRatesAndFractionalTiming(t *testing.T) {
	for _, tc := range []struct {
		rate     string
		duration float64
	}{{"24", 2}, {"30", 1.23}, {"30000/1001", 1.23}} {
		t.Run(tc.rate, func(t *testing.T) {
			path, probe, encoder := markerFixture(t, tc.rate, tc.duration)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := validateMarkerWithTools(ctx, path, tc.duration, probe, encoder)
			if err != nil {
				t.Fatal(err)
			}
			if result["status"] != "passed" || result["playback"] != "decoded all frames without reported errors" {
				t.Fatalf("unexpected validation: %+v", result)
			}
		})
	}
}

func TestMarkerValidationRequiresDecoderSuccessAndCancellation(t *testing.T) {
	path, probe, _ := markerFixture(t, "30", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result, err := validateMarkerWithTools(ctx, path, 1, probe, filepath.Join(t.TempDir(), "missing-decoder")); err == nil || result["status"] == "passed" {
		t.Fatalf("unavailable decoder approved: %+v, %v", result, err)
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if result, err := validateMarkerWithTools(ctx, path, 1, probe, "unused"); err == nil || result["status"] == "passed" {
		t.Fatalf("cancelled validation approved: %+v, %v", result, err)
	}
	for _, tc := range []struct {
		name, script string
		timeout      time.Duration
	}{
		{"error", "exit 1", time.Second},
		{"incomplete", "printf 'frame=1\\nprogress=end\\n'", time.Second},
		{"cancel", "exec /bin/sleep 10", 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder := filepath.Join(t.TempDir(), "decoder")
			if err := os.WriteFile(decoder, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			if result, err := validateMarkerWithTools(ctx, path, 1, probe, decoder); err == nil || result["status"] == "passed" {
				t.Fatalf("decoder failure approved: %+v, %v", result, err)
			}
		})
	}
}

func TestMarkerExpectedDurationUsesFixtureEnd(t *testing.T) {
	path, probe, _ := markerFixture(t, "24", 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if duration, err := markerExpectedDuration(ctx, probe, path, 0.5, 2); err != nil || duration != 1.5 {
		t.Fatalf("duration=%v, error=%v", duration, err)
	}
	if _, err := markerExpectedDuration(ctx, probe, path, 2, 1); err == nil {
		t.Fatal("EOF start accepted")
	}
}

func TestMarkerMissingDurationIgnoresLongerAudio(t *testing.T) {
	_, probe, encoder := markerFixture(t, "24", 1)
	fixture := filepath.Join(t.TempDir(), "longer-audio.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-f", "lavfi", "-i", "color=c=red:s=640x360:r=24:d=2", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono:d=3", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "pcm_s16le", fixture}
	if output, err := exec.CommandContext(ctx, encoder, args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, output)
	}
	duration, err := markerExpectedDuration(ctx, probe, fixture, 1, 2)
	if err != nil || math.Abs(duration-1) > 0.01 {
		t.Fatalf("video remainder=%v; want1s, error=%v", duration, err)
	}
}

func TestMarkerPacketEvidenceBoundAndCancellation(t *testing.T) {
	var output markerPacketBuffer
	if _, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", 1<<20))); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&output, strings.NewReader("overflow")); err == nil {
		t.Fatal("packet evidence bound bypassed through io.Copy")
	}
	if output.data.Len() != 1<<20 {
		t.Fatalf("retained%d bytes", output.data.Len())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := markerVideoPacketDuration(ctx, "unused", "synthetic", "0"); err == nil {
		t.Fatal("cancelled packet probe accepted")
	}
	if _, err := markerVideoPacketDuration(context.Background(), "unused", "synthetic", "NaN"); err == nil {
		t.Fatal("nonfinite timestamp origin accepted")
	}
}

func TestLabRejectsNonFiniteFlagsBeforeCreatingOutput(t *testing.T) {
	for _, tc := range []struct{ option, value string }{{"-start", "NaN"}, {"-start", "+Inf"}, {"-start", "-Inf"}, {"-duration", "NaN"}, {"-duration", "+Inf"}} {
		t.Run(tc.option+tc.value, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "must-not-exist")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLabInvalidFlagHelper$")
			cmd.Env = append(os.Environ(), "LAB_INVALID_OPTION="+tc.option, "LAB_INVALID_VALUE="+tc.value, "LAB_INVALID_OUTPUT="+out)
			if err := cmd.Run(); err == nil {
				t.Fatal("invalid flag accepted")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("invalid flags created output directory: %v", err)
			}
		})
	}
}

func TestLabInvalidFlagHelper(t *testing.T) {
	if os.Getenv("LAB_INVALID_OUTPUT") == "" {
		return
	}
	os.Args = []string{"lab", "-fixture=unused-synthetic", "-workload=webp", "-output-dir=" + os.Getenv("LAB_INVALID_OUTPUT"), os.Getenv("LAB_INVALID_OPTION") + "=" + os.Getenv("LAB_INVALID_VALUE")}
	flag.CommandLine = flag.NewFlagSet("lab", flag.ExitOnError)
	main()
}
