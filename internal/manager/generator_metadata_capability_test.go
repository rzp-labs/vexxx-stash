package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/generate"
)

// These subprocesses supply capability evidence; they never open a GPU or decode
// media. A codec name or header dimension alone must not determine eligibility.
func capabilityMetadataFixture(t *testing.T, codec, profile string, width, height int, outcome string) (*Manager, string, string) {
	t.Helper()
	mgr, input, _, _ := metadataFixture(t)
	mgr.Config.SetInterface(config.PreviewGenerationBackend, "vaapi")
	mgr.Config.SetInterface(config.GenerationDevice, "/dev/dri/renderD128")
	dir := filepath.Dir(input)
	header := fmt.Sprintf(`{"format":{"format_name":"matroska","start_time":"0","duration":"10"},"streams":[{"index":0,"codec_type":"video","codec_name":%q,"profile":%q,"width":%d,"height":%d,"pix_fmt":"yuv420p","avg_frame_rate":"24/1","r_frame_rate":"24/1","duration":"10"}]}`, codec, profile, width, height)
	probe := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffprobe version 8.1'; exit 0; fi\ncase \" $* \" in *'no_pixel_probe'*) ;; *) echo 'software pixel probe forbidden' >&2; exit 97;; esac\nprintf '%s\\n' '" + header + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte(probe), 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "capability-calls")
	// Record every non-version call, including any unexpected retry/render.
	encoder := "#!/bin/sh\nif [ \"$1\" = '-version' ]; then echo 'ffmpeg version 8.1'; exit 0; fi\nprintf 'call\\n' >> '" + calls + "'\ncase \" $* \" in *'-hwaccel vaapi'*'-hwaccel_output_format vaapi'*'-hwaccel_strict 1'*'-hwaccel_metadata 1'*'-frames:v 1 -an -c:v wrapped_avframe -f null -'*) ;; *) echo 'unexpected render or software fallback' >&2; exit 98;; esac\n" + outcome
	binary := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(binary, []byte(encoder), 0700); err != nil {
		t.Fatal(err)
	}
	mgr.FFMpeg = ffmpeg.NewEncoder(binary)
	return mgr, input, calls
}

type capabilityRefusalCase struct {
	name, codec, profile, evidence string
	width, height, exit            int
	killed                         bool
}

func refusalProbeOutcome(codec, evidence string, killed bool) string {
	stderr := evidence
	if stderr != "" {
		stderr += "\nExplicit vaapi hardware output required for decoder " + codec + "; refusing software decoding."
	}
	outcome := "cat >&2 <<'EVIDENCE'\n" + stderr + "\nEVIDENCE\n"
	if killed {
		return outcome + "kill -KILL $$\n"
	}
	return outcome + "exit 69\n"
}

func TestVEX101CapabilityRefusalBeforePreviewRender(t *testing.T) {
	for _, tc := range []capabilityRefusalCase{
		{"wmv3-profile", "wmv3", "Main", "Codec WMV3 profile 1 is not supported.", 1920, 1080, 69, false},
		{"vc1-profile", "vc1", "Advanced", "Codec VC1 profile 3 is not supported.", 1920, 1080, 69, false},
		{"h264-dimensions", "h264", "High", "Hardware does not support image size 5760x2880 (constraints: width 0-4096 height 0-4096).", 5760, 2880, 69, false},
		{"h264-dimensions-killed", "h264", "High", "Hardware does not support image size 5760x2880 (constraints: width 0-4096 height 0-4096).", 5760, 2880, -1, true},
		{"killed-without-capability-evidence", "h264", "High", "", 5760, 2880, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runCapabilityRefusalCase(t, tc)
		})
	}
}

func runCapabilityRefusalCase(t *testing.T, tc capabilityRefusalCase) {
	t.Helper()
	mgr, input, calls := capabilityMetadataFixture(t, tc.codec, tc.profile, tc.width, tc.height, refusalProbeOutcome(tc.codec, tc.evidence, tc.killed))
	previous := instance
	instance = mgr
	defer func() { instance = previous }()
	if err := os.MkdirAll(mgr.Paths.Generated.Tmp, 0700); err != nil {
		t.Fatal(err)
	}
	output := mgr.Paths.Scene.GetVideoPreviewPath("fixture")
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("prior valid preview"), 0600); err != nil {
		t.Fatal(err)
	}
	intel := mgr.Config.GetIntelPreviewGeneration()
	g := &generate.Generator{Encoder: mgr.FFMpeg, Probe: mgr.FFProbe, FFMpegConfig: mgr.Config, LockManager: mgr.ReadLockManager, ScenePaths: mgr.Paths.Scene, Overwrite: true, IntelPreviews: intel}
	task := GeneratePreviewTask{Scene: models.Scene{Path: input, Checksum: "fixture", HasPreview: true}, Options: generate.PreviewOptions{Segments: 2, SegmentDuration: 1}, Overwrite: true, generator: g, fileNamingAlgorithm: models.HashAlgorithmMd5}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := task.Start(ctx)
	assertCapabilityRefusalError(t, tc, err, intel.Device)
	assertRefusalTelemetry(t, err, input)
	assertRefusalLeftNoArtifacts(t, mgr, ctx, calls, output)
}

func assertCapabilityRefusalError(t *testing.T, tc capabilityRefusalCase, err error, device string) {
	t.Helper()
	var failure *ffmpeg.IntelGenerationError
	var exitErr *exec.ExitError
	if !errors.As(err, &failure) || !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.exit {
		t.Fatalf("original capability/process outcome lost: %v", err)
	}
	d := failure.Diagnostic
	if d.Selected != "vaapi" || d.Actual != "none" || d.Stage != "metadata" {
		t.Fatalf("pre-render rejection misclassified: %+v", d)
	}
	if d.Reason == "" || !strings.Contains(d.Reason, tc.evidence) || d.Device != device || d.ProbeTimeout != 10*time.Second {
		t.Errorf("actionable structured probe diagnostic missing: %+v", d)
	}
	if tc.killed && !strings.Contains(err.Error(), "signal: killed") {
		t.Error("killed process relabeled as a codec/dimension failure", err)
	}
	if tc.evidence == "" && (strings.Contains(err.Error(), "not supported") || strings.Contains(err.Error(), "4096")) {
		t.Fatal("header dimensions/SIGKILL invented capability evidence", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "out of memory") || generationbudget.IsPressure(ffmpeg.GenerationPressure(err)) {
		t.Fatal("capability refusal/SIGKILL incorrectly established resource pressure", err)
	}
	assertRefusalPreservesHeader(t, tc, failure.Source)
}

func assertRefusalPreservesHeader(t *testing.T, tc capabilityRefusalCase, source ffmpeg.IntelSource) {
	t.Helper()
	if source.Codec != tc.codec || source.Profile != tc.profile || source.Width != tc.width || source.Height != tc.height {
		t.Fatal("source header evidence lost", source)
	}
}

func assertRefusalTelemetry(t *testing.T, err error, input string) {
	t.Helper()
	// Build the telemetry payload locally; no client or network send.
	props := analytics.GenerationException(err, analytics.GenerationFailureContext{Workload: "preview", SelectedBackend: "vaapi", PrivateValues: []string{input}}).Properties
	if props["generation_stage"] != "metadata" || props["generation_actual_backend"] != "none" || props["generation_reason"] == "" || props["generation_probe_timeout_ms"] != int64(10000) {
		t.Errorf("task diagnostic lost in telemetry: stage=%v actual=%v reason=%v timeout=%v", props["generation_stage"], props["generation_actual_backend"], props["generation_reason"], props["generation_probe_timeout_ms"])
	}
}

func assertRefusalLeftNoArtifacts(t *testing.T, mgr *Manager, ctx context.Context, calls, output string) {
	t.Helper()
	data, readErr := os.ReadFile(calls)
	if readErr != nil || string(data) != "call\n" {
		t.Fatal("failed capability probe retried/rendered", string(data), readErr)
	}
	data, readErr = os.ReadFile(output)
	if readErr != nil || string(data) != "prior valid preview" {
		t.Fatal("pre-render rejection replaced prior artifact", string(data), readErr)
	}
	if entries, readErr := os.ReadDir(mgr.Paths.Generated.Tmp); readErr != nil || len(entries) != 0 {
		t.Fatal("pre-render rejection leaked temporary output", entries, readErr)
	}
	release, acquireErr := mgr.Config.GetIntelGenerationBudget().Acquire(ctx, generationbudget.GPU)
	if acquireErr != nil {
		t.Fatal("capability/process failure leaked shared admission", acquireErr)
	}
	release()
}

func TestVEX101SupportedMetadataContinuesWithoutHeaderBlacklist(t *testing.T) {
	for _, tc := range []struct {
		codec, profile string
		width, height  int
	}{
		{"h264", "High", 1920, 1080},
		{"h264", "High", 5760, 2880},
		{"wmv3", "Main", 1920, 1080},
		{"vc1", "Advanced", 1920, 1080},
	} {
		t.Run(fmt.Sprintf("%s-%dx%d", tc.codec, tc.width, tc.height), func(t *testing.T) {
			record := fmt.Sprintf(`VEXXX_GPU_METADATA={"stream_index":0,"bit_depth":8,"is_rgb":false,"width":%d,"height":%d,"pix_fmt":"nv12","sample_aspect_ratio":"1/1","color_range":"tv","color_space":"bt709","color_primaries":"bt709","color_transfer":"bt709","frame_rate":"24/1"}`, tc.width, tc.height)
			mgr, input, calls := capabilityMetadataFixture(t, tc.codec, tc.profile, tc.width, tc.height, "printf '%s\\n' '"+record+"'\n")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			file, err := mgr.generationPreviewVideoFile(ctx, input)
			if err != nil || file.VideoCodec != tc.codec || file.Width != tc.width || file.Height != tc.height {
				t.Fatalf("successful actual-frame evidence blocked planning: %+v %v", file, err)
			}
			data, err := os.ReadFile(calls)
			if err != nil || string(data) != "call\n" {
				t.Fatal("supported metadata retried or used CPU pixels", string(data), err)
			}
		})
	}
}
