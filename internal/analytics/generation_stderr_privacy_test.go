package analytics

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

func TestGenerationStderrReasonRetainsCredentialContext(t *testing.T) {
	data := []byte(strings.Repeat("probe status\n", 320) + "token: " + strings.Repeat("CONFIDENTIALVALUE", 160) + "\nVAAPI encode failed: 24; Input/output error\n")
	reason := ffmpeg.GenerationStderrText(data, 4096)
	err := ffmpeg.WithIntelGenerationDiagnostic(&exec.ExitError{}, ffmpeg.IntelGenerationDiagnostic{Selected: "vaapi", Actual: "none", Stage: "encode", Reason: reason}, ffmpeg.IntelSource{})
	event := GenerationException(err, GenerationFailureContext{})
	encoded, _ := json.Marshal(event.APIfy())
	if strings.Contains(string(encoded), "CONFIDENTIALVALUE") {
		t.Fatalf("credential suffix leaked through reason: input=%d reason=%d", len(data), len(reason))
	}
	if !strings.Contains(string(encoded), "VAAPI encode failed: 24; Input/output error") {
		t.Fatal("terminal driver cause lost")
	}
}

func TestGenerationStderrCaptureRetainsMetadataContext(t *testing.T) {
	data := []byte("  Metadata:\n" + strings.Repeat("    custom_tag: PRIVATECUSTOMVALUE\n", 400) + "[h264_vaapi] terminal driver refusal\n")
	var capture ffmpeg.GenerationStderr
	capture.Write(data)
	stderr, counts := capture.Capture()
	event := GenerationException(&ffmpeg.GenerationCommandError{Err: &exec.ExitError{Stderr: stderr}, Stderr: counts}, GenerationFailureContext{})
	encoded, _ := json.Marshal(event.APIfy())
	if strings.Contains(string(encoded), "PRIVATECUSTOMVALUE") {
		t.Fatalf("metadata context lost: input=%d retained=%d omitted=%d", len(data), counts.Retained, counts.Omitted)
	}
	if !strings.Contains(string(encoded), "terminal driver refusal") {
		t.Fatal("terminal driver cause lost")
	}
}

func TestGenerationStderrBlockContextSurvivesDiscardedHeadersAndSplitWrites(t *testing.T) {
	for _, block := range []string{
		"  Metadata:\n" + strings.Repeat("    custom_tag: PRIVATECUSTOMVALUE\n", 400),
		"-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("PRIVATECUSTOMVALUE\n", 800) + "-----END PRIVATE KEY-----\n",
		"token: " + strings.Repeat("PRIVATECUSTOMVALUE", 800) + "\n",
	} {
		for _, chunk := range []int{1, 37, 32768} {
			data := []byte(strings.Repeat("[probe] status\n", 800) + block + "[h264_vaapi] terminal driver refusal\n")
			var capture ffmpeg.GenerationStderr
			for offset := 0; offset < len(data); {
				end := min(offset+chunk, len(data))
				capture.Write(data[offset:end])
				offset = end
			}
			stderr, counts := capture.Capture()
			event := GenerationException(&ffmpeg.GenerationCommandError{Err: &exec.ExitError{Stderr: stderr}, Stderr: counts}, GenerationFailureContext{})
			encoded, _ := json.Marshal(event.APIfy())
			if strings.Contains(string(encoded), "PRIVATECUSTOMVALUE") || !strings.Contains(string(encoded), "terminal driver refusal") {
				t.Fatalf("context or terminal cause lost with chunk=%d", chunk)
			}
			if counts.Total != int64(len(data)) || counts.Omitted != counts.Total-int64(counts.Retained) || counts.Retained > 8192 {
				t.Fatalf("incorrect byte counts: %+v", counts)
			}
			if strings.Contains(block, "Metadata:") && event.Properties["ffmpeg_metadata_omitted_records"] != 401 {
				t.Fatal("metadata omission count lost", event.Properties["ffmpeg_metadata_omitted_records"])
			}
		}
	}
}
