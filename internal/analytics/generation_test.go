package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestGenerationExceptionPreservesFailureWithoutCommandOrPrivateMetadata(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 23")
	exit := cmd.Run().(*exec.ExitError)
	exit.Stderr = []byte("  Metadata:\n    title           : private media title\n    artist          : private performer\n[hevc @ 0xabcdef] Failed to end picture: 23 (internal decoding error).\nError while decoding stream #0:0: Input/output error\nsource='/mnt/user/Private Media With Spaces.mp4' token='super-secret'\nAuthorization: Bearer second-secret\nhttps://user:pass@private.invalid/path?key=third-secret\n")
	command := &ffmpeg.GenerationCommandError{Err: fmt.Errorf("error running ffmpeg command <-i /mnt/user/Private Media With Spaces.mp4 -vf PRIVATE_FILTER>: %w", exit), Admitted: 16, Limits: generationbudget.Settings{MaxProcesses: 24, MaxGPUProcesses: 24, Threads: 1}}
	err := ffmpeg.WithIntelGenerationDiagnostic(command, ffmpeg.IntelGenerationDiagnostic{Selected: "vaapi", Actual: "none", Stage: "generation"}, ffmpeg.IntelSource{Codec: "hevc", Width: 8192, Height: 4096})
	event := GenerationException(err, GenerationFailureContext{JobCorrelation: uuid.NewString(), Workload: "sprite", Configured: generationbudget.Settings{MaxProcesses: 24, MaxGPUProcesses: 24}, Effective: command.Limits})
	message := event.ExceptionList[0].Value
	for _, text := range []string{"Failed to end picture: 23", "internal decoding error", "Input/output error"} {
		if !strings.Contains(message, text) {
			t.Errorf("technical diagnostic missing %q: %s", text, message)
		}
	}
	encoded, _ := json.Marshal(event.APIfy())
	for _, text := range []string{"Private Media", "private media title", "private performer", "super-secret", "second-secret", "third-secret", "private.invalid", "PRIVATE_FILTER", "command <"} {
		if strings.Contains(string(encoded), text) {
			t.Errorf("private or command value leaked %q: %s", text, encoded)
		}
	}
	for key, want := range map[string]any{"generation_admitted_slots": 16, "ffmpeg_exit_code": 23, "va_status": int64(23), "generation_stage": "decode", "source_width": 8192, "$app_namespace": "vexxx-server"} {
		if event.Properties[key] != want {
			t.Errorf("%s=%v want %v", key, event.Properties[key], want)
		}
	}
	if event.ExceptionList[0].Stacktrace != nil {
		t.Fatal("external failure has a fabricated application stack")
	}
}

func TestGenerationDiagnosticSeparatesHWAccelReturnFromVAStatus(t *testing.T) {
	message := "Failed setup for format vaapi: hwaccel initialisation returned error 23\nError while decoding stream #0:0: Input/output error"
	event := GenerationException(errors.New(message), GenerationFailureContext{})
	if event.Properties["hwaccel_error_code"] != 23 {
		t.Fatal("missing hardware initialization return code", event.Properties)
	}
	if _, present := event.Properties["va_status"]; present {
		t.Fatal("FFmpeg return code mislabeled VAStatus")
	}
}

func TestPictureStatusKeepsDecoderAndEncoderStagesDistinct(t *testing.T) {
	for message, stage := range map[string]string{
		"[hevc @ 0xabc] Failed to end picture: 23 (internal decoding error).":                     "decode",
		"[mjpeg_vaapi @ 0xabc] Failed to end picture encode issue: 24 (internal encoding error).": "encode",
		"[h264_vaapi @ 0xabc] Failed to end picture encode issue: 24 (internal encoding error).":  "encode",
		"Failed to end picture: 1 (unknown error).":                                               "generation",
	} {
		event := GenerationException(errors.New(message), GenerationFailureContext{})
		if event.Properties["generation_stage"] != stage {
			t.Errorf("stage for %q=%v, want %s", message, event.Properties["generation_stage"], stage)
		}
	}
}

func TestTechnicalRedactionBoundsAfterRemovingWholeSensitiveValues(t *testing.T) {
	private := "/mnt/user/Private Name With Spaces.mkv"
	message := strings.Repeat("é", 4090) + " source=" + private + " password=secret-value\nError submitting packet to decoder: Cannot allocate memory"
	safe := sanitizeTechnicalMessage(message, []string{private})
	if len(safe) > 4096 || !utf8.ValidString(safe) {
		t.Fatal("diagnostic bound or UTF-8 violated", len(safe))
	}
	if !strings.Contains(safe, "Cannot allocate memory") || strings.Contains(safe, "Private") || strings.Contains(safe, "secret-value") {
		t.Fatal("tail cause lost or redaction incomplete", safe)
	}
	for _, input := range []string{`open C:\Users\Private\movie.mkv: permission denied`, `open '\\nas\Private Share\movie.mp4': permission denied`, `failed https://admin:secret@nas/private.mp4?token=abc`, `api_key="key with spaces" VAAPI resource allocation failed`} {
		safe := sanitizeTechnicalMessage(input, nil)
		if strings.Contains(safe, "Private") || strings.Contains(safe, "secret") || strings.Contains(safe, "abc") || strings.Contains(safe, "key with spaces") {
			t.Errorf("redaction failed: %s", safe)
		}
	}
	if safe := sanitizeTechnicalMessage("Input/output error; NV12/P010 conversion failed on /dev/dri/renderD128", nil); safe != "Input/output error; NV12/P010 conversion failed on /dev/dri/renderD128" {
		t.Fatal("technical slash text redacted", safe)
	}
}

func TestGenerationTimeoutDiagnosticRemainsFailure(t *testing.T) {
	// A capability probe can time out while its parent job is still alive. The
	// caller context, rather than this wrapped internal deadline, defines cancel.
	event := GenerationException(ffmpeg.WithIntelGenerationDiagnostic(context.DeadlineExceeded, ffmpeg.IntelGenerationDiagnostic{Selected: "vaapi", Actual: "none", Stage: "decode"}, ffmpeg.IntelSource{}), GenerationFailureContext{})
	if event.Properties["generation_stage"] != "decode" || event.ExceptionList[0].Value != "context deadline exceeded" {
		t.Fatal("probe timeout lost", event)
	}
}

func TestAuxiliaryPathsWithSpacesAreRedactedAsWholeRecords(t *testing.T) {
	for _, path := range []string{
		`/mnt/user/Jane Doe/subtitle data.ass`,
		`C:\Users\Jane Doe\subtitle data.ass`,
		`\\nas\Private Share\Jane Doe\subtitle data.ass`,
		`/mnt/user/Jane Doe/Private Folder (draft)/subtitle data.ass`,
	} {
		for _, suffix := range []string{"", ": Permission denied", ": No such file or directory"} {
			message := "[Parsed_subtitles_0 @ 0xabc] Unable to open " + path + suffix + "\n[hevc @ 0xabc] Failed to end picture: 23 (internal decoding error).\nInput/output error; NV12/P010 conversion failed on /dev/dri/renderD128"
			event := GenerationException(errors.New(message), GenerationFailureContext{})
			encoded, _ := json.Marshal(event.APIfy())
			for _, private := range []string{"Jane", "Doe", "subtitle", "data.ass", "Private Share", "Private Folder", "draft"} {
				// The FFmpeg filter name is technical; check the message after its prefix.
				if strings.Contains(strings.SplitN(event.ExceptionList[0].Value, "Unable to open ", 2)[1], private) {
					t.Errorf("path fragment %q leaked: %s", private, encoded)
				}
			}
			for _, technical := range []string{"[Parsed_subtitles_0 @ 0xabc] Unable to open [path redacted]" + suffix, "Failed to end picture: 23", "Input/output error; NV12/P010 conversion failed on /dev/dri/renderD128"} {
				if !strings.Contains(event.ExceptionList[0].Value, technical) {
					t.Errorf("technical cause lost %q: %s", technical, encoded)
				}
			}
		}
	}
	// A path may itself end in words that resemble an error. Redaction still
	// removes all of its private components and exports only a fixed OS phrase.
	if safe := sanitizeTechnicalMessage("open /mnt/Jane Doe/record: Permission denied", nil); safe != "open [path redacted]: Permission denied" {
		t.Fatal(safe)
	}
}

func TestSoftwareActualBackendRequiresSuccessfulProcessStart(t *testing.T) {
	for _, tc := range []struct {
		name, selected, actual string
		started                bool
		intel                  bool
	}{
		{"start failure", "software", "none", false, false},
		{"executed software", "software", "software", true, false},
		{"selected GPU", "vaapi", "none", true, false},
		{"unspecified backend", "", "none", true, false},
		{"Intel diagnostic authoritative", "software", "none", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error = &ffmpeg.GenerationCommandError{Err: errors.New("synthetic command failed"), Admitted: 1, Started: tc.started}
			if tc.intel {
				err = ffmpeg.WithIntelGenerationDiagnostic(err, ffmpeg.IntelGenerationDiagnostic{Selected: "software", Actual: "none"}, ffmpeg.IntelSource{})
			}
			event := GenerationException(err, GenerationFailureContext{SelectedBackend: tc.selected})
			if actual := event.Properties["generation_actual_backend"]; actual != tc.actual {
				t.Fatalf("actual backend %v, want %s", actual, tc.actual)
			}
		})
	}
}
