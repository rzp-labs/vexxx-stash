package analytics

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

func TestGenerationStderrOverlongRecordClassificationPreservesBlockContext(t *testing.T) {
	for _, fixture := range []struct {
		name, data string
		metadata   int
	}{
		{"metadata colon beyond prefix", "  Metadata:\n    " + strings.Repeat("A", 4096) + ": value\n    custom_tag: PRIVATECUSTOMVALUE\n[driver] terminal refusal\n", 3},
		{"private key begin beyond prefix", strings.Repeat("x", 4096) + "-----BEGIN PRIVATE KEY-----\nPRIVATECUSTOMVALUE\n-----END PRIVATE KEY-----\n[driver] terminal refusal\n", 0},
		{"private key end beyond prefix", "-----BEGIN PRIVATE KEY-----\nPRIVATECUSTOMVALUE\n" + strings.Repeat("x", 4096) + "-----END PRIVATE KEY-----\n[driver] terminal refusal\n", 0},
		{"metadata header beyond prefix", strings.Repeat(" ", 4096) + "Metadata:\n" + strings.Repeat(" ", 4097) + "custom_tag: PRIVATECUSTOMVALUE\n[driver] terminal refusal\n", 2},
		{"long private key label", "-----BEGIN " + strings.Repeat("A ", 4096) + "PRIVATE KEY-----\nPRIVATECUSTOMVALUE\n-----END PRIVATE KEY-----\n[driver] terminal refusal\n", 0},
		{"Unicode whitespace in metadata", "  Metadata:\n" + strings.Repeat("\u2003", 4096) + "\n    custom_tag: PRIVATECUSTOMVALUE\n[driver] terminal refusal\n", 2},
	} {
		for _, chunk := range []int{1, 37, 32768} {
			t.Run(fmt.Sprintf("%s/%d", fixture.name, chunk), func(t *testing.T) {
				var capture ffmpeg.GenerationStderr
				for offset := 0; offset < len(fixture.data); {
					end := min(offset+chunk, len(fixture.data))
					capture.Write([]byte(fixture.data[offset:end]))
					offset = end
				}
				stderr, counts := capture.Capture()
				event := GenerationException(&ffmpeg.GenerationCommandError{Err: &exec.ExitError{Stderr: stderr}, Stderr: counts}, GenerationFailureContext{})
				encoded, _ := json.Marshal(event.APIfy())
				if strings.Contains(string(encoded), "PRIVATECUSTOMVALUE") {
					t.Errorf("private value leaked; source=%d retained=%d omitted=%d", len(fixture.data), counts.Retained, counts.Omitted)
				}
				if !strings.Contains(string(encoded), "[driver] terminal refusal") {
					t.Error("terminal driver diagnostic lost")
				}
				if counts.Total != int64(len(fixture.data)) || counts.Omitted != counts.Total-int64(counts.Retained) || counts.Retained > 8192 {
					t.Error("byte counts or storage incorrect", counts)
				}
				if event.Properties["ffmpeg_metadata_omitted_records"] != fixture.metadata {
					t.Error("metadata omission count incorrect", event.Properties["ffmpeg_metadata_omitted_records"], fixture.metadata)
				}
			})
		}
	}
}

func TestGenerationStderrOverlongRecordsStillEndContextForDriverDiagnostics(t *testing.T) {
	for _, ending := range []string{
		strings.Repeat("x", 4096),
		strings.Repeat(" ", 4096) + "[driver] diagnostic: overflow",
	} {
		for _, chunk := range []int{1, 37, 32768} {
			data := "  Metadata:\n" + ending + "\n    driver detail: retained diagnostic\n[driver] terminal refusal\n"
			var capture ffmpeg.GenerationStderr
			for offset := 0; offset < len(data); {
				end := min(offset+chunk, len(data))
				capture.Write([]byte(data[offset:end]))
				offset = end
			}
			stderr, counts := capture.Capture()
			event := GenerationException(&ffmpeg.GenerationCommandError{Err: &exec.ExitError{Stderr: stderr}, Stderr: counts}, GenerationFailureContext{})
			output := event.Properties["ffmpeg_output"].(string)
			if !strings.Contains(output, "driver detail: retained diagnostic") || !strings.Contains(output, "[driver] terminal refusal") || event.Properties["ffmpeg_metadata_omitted_records"] != 1 {
				t.Fatal("overflow record incorrectly prolonged metadata context", chunk, output)
			}
		}
	}
}
