package analytics

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

func TestGenerationStderrOverlappingKeyDelimitersDoNotExposeBody(t *testing.T) {
	data := "-----BEGIN PRIVATE KEY---------END PRIVATE KEY-----\nPRIVATECUSTOMVALUE\n[driver] terminal refusal\n"
	if len(data) != 97 {
		t.Fatal("review fixture changed")
	}
	for _, chunk := range []int{1, 37, 32768} {
		output := generationDelimiterEventOutput(data, chunk)
		if strings.Contains(output, "PRIVATECUSTOMVALUE") {
			t.Errorf("overlapping closing delimiter exposed key body with chunk=%d", chunk)
		}
	}
}

func TestGenerationStderrDelimiterBoundariesMatchExistingRedaction(t *testing.T) {
	// The independent complete-text oracle consumes matches exclusively, as
	// diagnostics.Sanitize does. Vary overlap, record overflow and pipe writes.
	redact := regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
	cases := 0
	for hyphens := 0; hyphens <= 14; hyphens++ {
		for _, padding := range []int{0, 4090, 4096} {
			for _, closeBlock := range []bool{false, true} {
				data := strings.Repeat("x", padding) + "-----BEGIN PRIVATE KEY" + strings.Repeat("-", hyphens) + "END PRIVATE KEY-----\nCONTEXTBODYMARKER\n"
				if closeBlock {
					data += "-----END PRIVATE KEY-----\n"
				}
				data += "[driver] terminal refusal\n"
				oracle := redact.ReplaceAllString(data, "[credential redacted]")
				for _, chunk := range []int{1, 2, 5, 9, 37, 32768} {
					output := generationDelimiterEventOutput(data, chunk)
					for _, marker := range []string{"CONTEXTBODYMARKER", "[driver] terminal refusal"} {
						if strings.Contains(output, marker) != strings.Contains(oracle, marker) {
							t.Fatalf("context differs from existing redaction: hyphens=%d padding=%d close=%t chunk=%d marker=%s", hyphens, padding, closeBlock, chunk, marker)
						}
					}
					cases++
				}
			}
		}
	}
	t.Logf("%d deterministic event-level delimiter/overflow/chunk cases", cases)
}

func generationDelimiterEventOutput(data string, chunk int) string {
	var capture ffmpeg.GenerationStderr
	for offset := 0; offset < len(data); {
		end := min(offset+chunk, len(data))
		capture.Write([]byte(data[offset:end]))
		offset = end
	}
	stderr, counts := capture.Capture()
	event := GenerationException(&ffmpeg.GenerationCommandError{Err: &exec.ExitError{Stderr: stderr}, Stderr: counts}, GenerationFailureContext{})
	return event.Properties["ffmpeg_output"].(string)
}
