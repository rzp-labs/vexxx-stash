package generate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func intelMetadataTestRecord() map[string]any {
	return map[string]any{"width": 8192, "height": 4096, "pix_fmt": "p010le", "sample_aspect_ratio": "1/1",
		"color_range": "tv", "color_space": "bt2020nc", "color_primaries": "bt2020", "color_transfer": "smpte2084", "frame_rate": "60000/1001"}
}

func intelMetadataTestOutput(record map[string]any) []byte {
	data, _ := json.Marshal(record)
	return append([]byte(intelMetadataPrefix), data...)
}

func TestIntelGPUFrameMetadataRecoversActualHDRAndPreservesHeaderIdentity(t *testing.T) {
	matrix := [9]int32{0, -65536, 0, 65536, 0, 0, 0, 0, 1073741824}
	header := ffmpeg.IntelSource{Codec: "hevc", Profile: "Main 10", PixelFormat: "unknown", StreamIndex: 2,
		DisplayMatrix: &matrix, Rotation: 90, Duration: "3465.078283", StartTime: "5.25", AverageFrameRate: "60000/1001"}
	source, err := mergeIntelFrameMetadata(header, intelMetadataTestOutput(intelMetadataTestRecord()))
	if err != nil {
		t.Fatal(err)
	}
	if source.PixelFormat != "yuv420p10le" || source.Width != 8192 || source.Height != 4096 || source.ColorTransfer != "smpte2084" || source.ColorSpace != "bt2020nc" || source.SampleAspectRatio != "1:1" {
		t.Fatal("actual decoded properties lost", source)
	}
	if source.Codec != header.Codec || source.Profile != header.Profile || source.StreamIndex != 2 || source.DisplayMatrix != &matrix || source.Rotation != 90 || source.Duration != header.Duration || source.StartTime != header.StartTime || source.AverageFrameRate != header.AverageFrameRate {
		t.Fatal("container/header identity changed", source)
	}
	if err := source.ValidatePreview(); err != nil {
		t.Fatal("actual supported HDR must be validated after GPU recovery", err)
	}
}

func TestIntelGPUFrameMetadataNeverInventsMissingPixelProperties(t *testing.T) {
	for _, field := range []string{"width", "height", "pix_fmt", "sample_aspect_ratio", "color_range", "color_space", "color_primaries", "color_transfer", "frame_rate"} {
		t.Run(field, func(t *testing.T) {
			record := intelMetadataTestRecord()
			delete(record, field)
			if _, err := mergeIntelFrameMetadata(ffmpeg.IntelSource{}, intelMetadataTestOutput(record)); err == nil {
				t.Fatal("missing actual field was accepted", field)
			}
		})
	}
	for _, mutation := range []struct {
		field string
		value any
	}{
		{"width", 0}, {"height", -1}, {"pix_fmt", "vaapi"}, {"pix_fmt", "rgba"}, {"sample_aspect_ratio", "1/0"},
		{"sample_aspect_ratio", "-1/1"}, {"color_range", "limited"}, {"color_space", ""}, {"color_transfer", "reserved"},
		{"color_primaries", nil}, {"color_space", "bogus"}, {"color_primaries", "bogus"}, {"color_transfer", "bogus"}, {"frame_rate", "NaN"}, {"frame_rate", "-30/1"},
	} {
		record := intelMetadataTestRecord()
		record[mutation.field] = mutation.value
		if _, err := mergeIntelFrameMetadata(ffmpeg.IntelSource{}, intelMetadataTestOutput(record)); err == nil {
			t.Fatalf("invalid %s=%v accepted", mutation.field, mutation.value)
		}
	}
	for _, output := range [][]byte{[]byte("{}"), []byte(intelMetadataPrefix + "{"), append(append(intelMetadataTestOutput(intelMetadataTestRecord()), '\n'), intelMetadataTestOutput(intelMetadataTestRecord())...)} {
		if _, err := mergeIntelFrameMetadata(ffmpeg.IntelSource{}, output); err == nil {
			t.Fatal("missing/malformed/duplicate first-frame metadata accepted")
		}
	}
}

func TestIntelGPUFrameMetadataKnownAbsenceAndRateFallback(t *testing.T) {
	for _, headerSAR := range []string{"", "1:1", "4:3"} {
		record := intelMetadataTestRecord()
		record["pix_fmt"], record["sample_aspect_ratio"], record["frame_rate"] = "nv12", "0/1", "0/0"
		record["color_range"], record["color_space"], record["color_primaries"], record["color_transfer"] = "unknown", "unknown", "unknown", "unknown"
		header := ffmpeg.IntelSource{SampleAspectRatio: headerSAR, DisplayAspectRatio: "99:1", FrameRate: "30000/1001", ColorTransfer: "smpte2084"}
		source, err := mergeIntelFrameMetadata(header, intelMetadataTestOutput(record))
		if err != nil {
			t.Fatal(err)
		}
		wantSAR := headerSAR
		if wantSAR == "" {
			wantSAR = "0:1"
		}
		if source.SampleAspectRatio != wantSAR || source.FrameRate != header.FrameRate || source.PixelFormat != "yuv420p" || source.ColorTransfer != "unknown" {
			t.Fatal("actual absence/header rational handling changed", source)
		}
		wantDAR := ""
		if headerSAR == "1:1" {
			wantDAR = "2:1"
		} else if headerSAR == "4:3" {
			wantDAR = "8:3"
		}
		if source.DisplayAspectRatio != wantDAR {
			t.Fatal("obsolete header DAR retained", source.DisplayAspectRatio)
		}
	}
	record := intelMetadataTestRecord()
	record["frame_rate"] = "0/0"
	if _, err := mergeIntelFrameMetadata(ffmpeg.IntelSource{}, intelMetadataTestOutput(record)); err == nil {
		t.Fatal("invented a frame rate without any valid actual/header rate")
	}
}

func TestIntelGPUFrameMetadataPreservesCanonicalContainerRate(t *testing.T) {
	for _, c := range []struct{ header, average, actual, want string }{
		{"30/1", "29/1", "24/1", "30/1"},
		{"0/0", "29/1", "24/1", "24/1"},
		{"0/0", "29/1", "0/0", "29/1"},
	} {
		record := intelMetadataTestRecord()
		record["frame_rate"], record["sample_aspect_ratio"] = c.actual, "4/3"
		source, err := mergeIntelFrameMetadata(ffmpeg.IntelSource{FrameRate: c.header, AverageFrameRate: c.average, DisplayAspectRatio: "99:1"}, intelMetadataTestOutput(record))
		if err != nil || source.FrameRate != c.want || source.DisplayAspectRatio != "8:3" {
			t.Fatalf("container/actual rate or geometry changed: %+v %v", source, err)
		}
	}
}

func metadataTestGenerator(t *testing.T, gpuScript string, settings generationbudget.Settings) Generator {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess fixtures")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe")
	// Emulate missing SPS-dependent fields: only container identity is known.
	header := `{"streams":[{"codec_type":"video","codec_name":"hevc","profile":"Main 10","index":2,"duration":"10","r_frame_rate":"60000/1001"}],"format":{"start_time":"0"}}`
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffprobe version 8.1';exit 0;fi\ncase \" $* \" in *no_pixel_probe*) ;; *) exit 44;; esac\ncat <<'HEADER'\n"+header+"\nHEADER\n"), 0700); err != nil {
		t.Fatal(err)
	}
	encoder := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(encoder, []byte("#!/bin/sh\nif [ \"$1\" = '-version' ];then echo 'ffmpeg version 8.1';exit 0;fi\n"+gpuScript), 0700); err != nil {
		t.Fatal(err)
	}
	budget, err := generationbudget.New(settings)
	if err != nil {
		t.Fatal(err)
	}
	return Generator{Probe: ffmpeg.NewFFProbe(probe), Encoder: ffmpeg.NewEncoder(encoder), Budget: budget, LockManager: fsutil.NewReadLockManager()}
}

func TestIntelMetadataStagesReleaseLeafPermitsAndUseOnlyWrappedHardwareFrame(t *testing.T) {
	output := string(intelMetadataTestOutput(intelMetadataTestRecord()))
	script := "case \" $* \" in *'-hwaccel vaapi'*'-hwaccel_strict 1'*'-hwaccel_metadata 1'*'-map 0:2 -frames:v 1 -an -c:v wrapped_avframe -f null -'*) ;; *) exit 45;; esac\nprintf '%s\\n' '" + output + "'\n"
	g := metadataTestGenerator(t, script, generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock := g.LockManager.ReadLock(ctx, "synthetic")
	defer lock.Cancel()
	source, err := g.intelSourceMetadata(ctx, lock, "synthetic", ffmpeg.IntelGenerationConfig{Backend: "vaapi", Device: "/dev/dri/renderD128"}, false)
	if err != nil || source.Width != 8192 {
		t.Fatalf("header/GPU nested admission or pixel copy: %+v %v", source, err)
	}
	release, err := g.Budget.Acquire(ctx, generationbudget.GPU)
	if err != nil {
		t.Fatal("metadata leaked permit", err)
	}
	release()
}

func TestIntelMetadataGPUQueueCancellationAndFailureReleasePermits(t *testing.T) {
	g := metadataTestGenerator(t, "echo driverfailure >&2\nexit 23\n", generationbudget.Settings{MaxProcesses: 2, MaxGPUProcesses: 1})
	held, err := g.Budget.Acquire(context.Background(), generationbudget.GPU)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	lock := g.LockManager.ReadLock(ctx, "synthetic")
	_, err = g.intelSourceMetadata(ctx, lock, "synthetic", ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queued GPU metadata did not cancel", err)
	}
	lock.Cancel()
	cancel()
	held()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock = g.LockManager.ReadLock(ctx, "synthetic")
	defer lock.Cancel()
	_, err = g.intelSourceMetadata(ctx, lock, "synthetic", ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, false)
	if err == nil || !strings.Contains(err.Error(), "GPU first-frame metadata") {
		t.Fatal("metadata driverfailure silently accepted", err)
	}
	release, err := g.Budget.Acquire(ctx, generationbudget.GPU)
	if err != nil {
		t.Fatal("failure leaked GPU admission", err)
	}
	release()
}

func TestIntelMetadataMissingEncoderFailsExplicitly(t *testing.T) {
	g := metadataTestGenerator(t, "exit 0\n", generationbudget.Settings{})
	g.Encoder = nil
	lock := g.LockManager.ReadLock(context.Background(), "synthetic")
	defer lock.Cancel()
	_, err := g.intelSourceMetadata(lock, lock, "synthetic", ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, false)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg unavailable for GPU frame metadata") {
		t.Fatal("missing encoder did not fail explicitly", err)
	}
}

func TestIntelMetadataActiveSourceDeletionDrainsCommandAndReusesPermit(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	g := metadataTestGenerator(t, "printf active > '"+started+"'\nexec sleep 30\n", generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lock := g.LockManager.ReadLock(ctx, "synthetic")
	defer lock.Cancel()
	done := make(chan error, 1)
	go func() {
		_, err := g.intelSourceMetadata(lock, lock, "synthetic", ffmpeg.IntelGenerationConfig{Backend: "vaapi"}, false)
		done <- err
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatal("metadata exited before actual GPU phase", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	g.LockManager.Cancel("synthetic")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "GPU first-frame metadata") {
			t.Fatal("active metadata cancellation succeeded", err)
		}
	case <-ctx.Done():
		t.Fatal("source deletion did not drain GPU metadata", ctx.Err())
	}
	release, err := g.Budget.Acquire(ctx, generationbudget.GPU)
	if err != nil {
		t.Fatal("cancelled metadata leaked admission", err)
	}
	release()
}

func TestIntelMetadataOutputProbeDeadlineStartsAfterAdmission(t *testing.T) {
	output := string(intelMetadataTestOutput(intelMetadataTestRecord()))
	g := metadataTestGenerator(t, "printf '%s\\n' '"+output+"'\n", generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	release, err := g.Budget.Acquire(ctx, generationbudget.CPU)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	lock := g.LockManager.ReadLock(ctx, "synthetic")
	defer lock.Cancel()
	done := make(chan error, 1)
	go func() {
		_, err := g.generateOutputWithContext(ffmpeg.WithIntelProbeTimeout(lock, 20*time.Millisecond), lock, []string{"-hwaccel", "vaapi", "-c:v", "wrapped_avframe", "-f", "null", "-"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatal("metadata finished before admission", err)
	case <-time.After(60 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("queue wait consumed metadata execution deadline", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestIntelMetadataRefusalDiagnosticPreservesBoundedStderrAndNoAssets(t *testing.T) {
	const refusal = "Failed to initialise VAAPI VLD context: unsupported decode profile"
	for _, long := range []bool{false, true} {
		name := "short"
		stderr := "  " + refusal + "\n"
		if long {
			name = "bounded"
			stderr += strings.Repeat("x", 8192) + "BEYOND_DIAGNOSTIC_LIMIT"
		}
		t.Run(name, func(t *testing.T) {
			script := "cat >&2 <<'REFUSAL'\n" + stderr + "\nREFUSAL\nexit 23\n"
			g := metadataTestGenerator(t, script, generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
			g.IntelPreviews = &ffmpeg.IntelGenerationConfig{Backend: "vaapi"}
			paths := previewTestPaths{markerTestPaths{t.TempDir()}}
			g.ScenePaths = paths
			var diagnostics []ffmpeg.IntelGenerationDiagnostic
			g.IntelDiagnostic = func(d ffmpeg.IntelGenerationDiagnostic) { diagnostics = append(diagnostics, d) }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			lock := g.LockManager.ReadLock(ctx, "synthetic")
			defer lock.Cancel()
			err := g.scenePreviewVideo("synthetic", 10, PreviewOptions{Segments: 1, SegmentDuration: 0.75}, "", false, false)(lock, paths.GetVideoPreviewPath(""))
			var exitErr *exec.ExitError
			if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
				t.Fatal("metadata error lost original exit error", err)
			}
			if len(diagnostics) != 1 || diagnostics[0].Stage != "metadata" || diagnostics[0].Actual != "none" || !strings.Contains(diagnostics[0].Reason, refusal) {
				t.Fatal("FFmpeg refusal missing from strict metadata diagnostic", diagnostics)
			}
			reason := diagnostics[0].Reason
			if long {
				if !strings.Contains(reason, strings.TrimSpace(string(exitErr.Stderr[:4096]))) || !strings.HasSuffix(reason, " [truncated]") || strings.Contains(reason, "BEYOND_DIAGNOSTIC_LIMIT") {
					t.Fatal("stderr diagnostic did not retain a bounded first 4096 bytes")
				}
				commandErr := errors.Unwrap(errors.Unwrap(err))
				if len(reason) > len("GPU first-frame metadata: ")+len(commandErr.Error())+len(": ")+4096+len(" [truncated]") {
					t.Fatal("metadata diagnostic exceeded stderr bound", len(reason))
				}
			} else if !strings.HasSuffix(reason, refusal) || strings.Contains(reason, "[truncated]") {
				t.Fatal("short refusal was not trimmed faithfully", reason)
			}
			if entries, err := os.ReadDir(paths.dir); err != nil || len(entries) != 0 {
				t.Fatal("failed metadata created assets", entries, err)
			}
			release, err := g.Budget.Acquire(ctx, generationbudget.GPU)
			if err != nil {
				t.Fatal("refused metadata leaked permit", err)
			}
			release()
		})
	}
}
