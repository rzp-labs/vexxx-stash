package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/ffmpeg"
)

func TestJoinedGenerationBranchDiagnosticsOnSDKWire(t *testing.T) {
	_, received := offlineSDK(t, 0)
	firstCause, secondCause, softwareCause := errors.New("first-driver-cause"), errors.New("second-driver-cause"), errors.New("software-encoder-cause")
	first := ffmpeg.WithIntelGenerationDiagnostic(firstCause, ffmpeg.IntelGenerationDiagnostic{Selected: "vaapi", Actual: "none", Stage: "decode", Reason: "first-probe-reason"}, ffmpeg.IntelSource{Codec: "h264", Width: 64, Height: 36})
	second := ffmpeg.WithIntelGenerationDiagnostic(secondCause, ffmpeg.IntelGenerationDiagnostic{Selected: "qsv", Actual: "software", Stage: "encode", Reason: "second-fallback-reason"}, ffmpeg.IntelSource{Codec: "hevc", Width: 128, Height: 72})
	software := &ffmpeg.GenerationCommandError{Err: softwareCause, Started: true, Admitted: 1}
	root := fmt.Errorf("generating outputs: %w: output completion", errors.Join(first, second, software))
	ctx := diagnostics.WithState(context.Background())
	info := GenerationFailureContext{SelectedBackend: "software", JobCorrelation: uuid.NewString()}
	CaptureGenerationFailure(ctx, root, info)
	CaptureJobFailure(ctx, root, info.JobCorrelation, "generation")
	events := received()
	if len(events) != 3 {
		t.Fatalf("expected three distinct branch events without job duplicate, got %d", len(events))
	}
	wants := []map[string]any{
		{"generation_selected_backend": "vaapi", "generation_actual_backend": "none", "generation_stage": "decode", "generation_reason": "first-probe-reason", "source_width": float64(64), "source_height": float64(36), "source_codec": "h264"},
		{"generation_selected_backend": "qsv", "generation_actual_backend": "software", "generation_stage": "encode", "generation_reason": "second-fallback-reason", "source_width": float64(128), "source_height": float64(72), "source_codec": "hevc"},
		{"generation_selected_backend": "software", "generation_actual_backend": "software", "generation_stage": "encode"},
	}
	for i, event := range events {
		props := event["properties"].(map[string]any)
		for key, want := range wants[i] {
			if props[key] != want {
				t.Errorf("branch %d %s=%v, want %v", i, key, props[key], want)
			}
		}
		if i == 2 {
			if _, exists := props["generation_reason"]; exists {
				t.Error("software branch inherited another branch's Intel reason")
			}
		}
		cause := props["operation_cause"].(string)
		encoded, _ := json.Marshal(props["$exception_list"])
		for _, text := range []string{"generating outputs:", ": output completion"} {
			if strings.Count(cause, text) != 1 || !strings.Contains(string(encoded), text) {
				t.Errorf("branch %d lost/duplicated common wrapper %q", i, text)
			}
		}
	}
	if !errors.Is(root, secondCause) {
		t.Fatal("original error chain changed")
	}
}

func TestJoinedGenerationCommonIntelWrapper(t *testing.T) {
	_, received := offlineSDK(t, 0)
	branch := ffmpeg.WithIntelGenerationDiagnostic(errors.New("branch-cause"), ffmpeg.IntelGenerationDiagnostic{Selected: "qsv", Actual: "qsv", Stage: "encode", Reason: "branch-reason"}, ffmpeg.IntelSource{Width: 128, Height: 72})
	root := ffmpeg.WithIntelGenerationDiagnostic(fmt.Errorf("common prefix: %w: common suffix", errors.Join(errors.New("common-cause"), branch)), ffmpeg.IntelGenerationDiagnostic{Selected: "vaapi", Actual: "none", Stage: "decode", Reason: "common-reason"}, ffmpeg.IntelSource{Width: 64, Height: 36})
	CaptureGenerationFailure(diagnostics.WithState(context.Background()), root, GenerationFailureContext{})
	events := received()
	if len(events) != 2 {
		t.Fatal("joined entries lost")
	}
	for i, want := range []string{"common-reason", "branch-reason"} {
		props := events[i]["properties"].(map[string]any)
		if props["generation_reason"] != want {
			t.Errorf("branch %d reason=%v, want %s", i, props["generation_reason"], want)
		}
	}
}
