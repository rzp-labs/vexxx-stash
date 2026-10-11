package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stashapp/stash/pkg/diagnostics"
	"strings"
	"testing"
)

func TestPreviewReplayPropertiesSurvivePrivacySanitizerAndJobWrappers(t *testing.T) {
	start, duration := 7.125, 0.75
	ctx := diagnostics.WithPreviewRequest(diagnostics.WithPreviewSource(context.Background(), 12345, 67890, "", nil), diagnostics.PreviewRequest{Segments: 2, LimitStart: &start})
	err := diagnostics.WithPreviewFailure(ctx, errors.New("input /private/source.mp4 failed"), diagnostics.PreviewCommand{Stage: "render", SeekSeconds: &start, DurationSeconds: &duration})
	for _, kind := range []string{"generation", "job"} {
		var data []byte
		if kind == "generation" {
			event := beforeSend(GenerationException(fmt.Errorf("task: %w", err), GenerationFailureContext{PrivateValues: []string{"/private/source.mp4"}}))
			data, _ = json.Marshal(event)
		} else {
			event := FailureException(fmt.Errorf("job: %w", err), "job", nil, []string{"/private/source.mp4"})
			data, _ = json.Marshal(beforeSend(event))
		}
		for _, private := range []string{"12345", "67890", "/private/source.mp4", "[private content redacted]"} {
			if strings.Contains(string(data), private) {
				t.Fatal("replay values redacted or private identifiers leaked", kind, private, string(data))
			}
		}
		if !strings.Contains(string(data), `"seek_seconds":7.125`) || !strings.Contains(string(data), `"requested_parameters"`) || !strings.Contains(string(data), `"retention":"unavailable"`) {
			t.Fatal("typed replay facts lost", kind, string(data))
		}
	}
}

func TestPreviewReplayJoinedTelemetryKeepsEachLeafReference(t *testing.T) {
	ctx := diagnostics.WithPreviewSource(context.Background(), 1, 2, "", nil)
	a := diagnostics.WithPreviewFailure(ctx, errors.New("first"), diagnostics.PreviewCommand{Stage: "decode"})
	b := errors.New("unrelated sibling")
	joined := fmt.Errorf("job: %w", errors.Join(a, b, context.Canceled))
	entries := diagnostics.Split(joined).Entries
	for i, entry := range entries {
		event := generationException(entry.Err, joined, entry.Cause, GenerationFailureContext{})
		_, present := event.Properties["preview_replay"]
		if present != (i == 0) {
			t.Fatal("borrowed sibling replay context", i, event.Properties)
		}
	}
}
