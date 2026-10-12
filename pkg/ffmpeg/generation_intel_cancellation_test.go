package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/diagnostics"
)

// A completed child can fail independently before its caller ends. The runner
// has already established execution causality; the wrapper must not replace it
// with a later context observation or start a fallback after cancellation.
func TestIntelWorkRetainsIndependentExitAfterCallerEnds(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell unavailable for synthetic child")
	}
	for _, stage := range []string{"filter", "encode", "generation"} {
		for _, strict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/strict=%t", stage, strict), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var records []diagnostics.PreviewReplayRecord
				ctx = diagnostics.WithPreviewSource(ctx, 1, 2, "", func(record diagnostics.PreviewReplayRecord) error {
					records = append(records, record)
					return nil
				})
				plan := IntelGenerationPlan{Config: IntelGenerationConfig{Backend: "vaapi"}, Source: intelTestSource(), Probes: []IntelProbeStep{{Stage: stage}}}
				command := diagnostics.PreviewCommand{Stage: stage}
				plan.PreviewReplay = &command
				if stage == "generation" {
					plan.Probes = nil
				}
				var original *GenerationCommandError
				fail := func(context.Context) error {
					cmd := exec.Command(shell, "-c", "printf 'synthetic encoder failure' >&2; exit 23")
					_, err := cmd.Output()
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 23 {
						t.Fatalf("fixture did not exit independently: %v", err)
					}
					original = &GenerationCommandError{Err: err, Admitted: 1, Started: true}
					// End the caller only after the child has been reaped.
					cancel()
					return diagnostics.WithPreviewFailure(ctx, original, command)
				}
				fallbackCalls, hardwareCalls, probeCalls := 0, 0, 0
				var software func(context.Context) error
				if !strict {
					software = func(context.Context) error { fallbackCalls++; return nil }
				}
				d, err := runIntelGenerationWork(ctx, plan, func(ctx context.Context) error { hardwareCalls++; return fail(ctx) }, software,
					func(ctx context.Context, _ Args) error { probeCalls++; return fail(ctx) }, func(string) error { return nil })
				var retained *GenerationCommandError
				var replay *diagnostics.PreviewReplayError
				if !errors.As(err, &retained) || retained != original || !errors.As(err, &replay) || len(records) != 1 || records[0].Replay.Reference != replay.Replay.Reference {
					t.Fatalf("lost command/replay evidence: diagnostic=%+v error=%v records=%d", d, err, len(records))
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || diagnostics.Split(err).Count != 1 {
					t.Fatalf("later caller state attributed to independent child: %v", err)
				}
				if d.Actual != "none" || d.Stage != stage || fallbackCalls != 0 || hardwareCalls+probeCalls != 1 {
					t.Fatalf("unexpected retry/diagnostic: %+v software=%d hardware=%d probes=%d", d, fallbackCalls, hardwareCalls, probeCalls)
				}
				if !strings.Contains(intelFailureReason(err), "synthetic encoder failure") {
					t.Fatal("lost bounded child stderr", err)
				}
			})
		}
	}
}

func TestIntelWorkRetainsFailureWhenCallerDeadlineExpires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cause := errors.New("independent command failure before pipe drain")
	plan := IntelGenerationPlan{Config: IntelGenerationConfig{Backend: "vaapi"}, Probes: []IntelProbeStep{{Stage: "encode"}}}
	d, err := runIntelGenerationWork(ctx, plan, func(context.Context) error { t.Fatal("render after failed probe"); return nil }, nil,
		func(context.Context, Args) error { <-ctx.Done(); return cause }, func(string) error { return nil })
	if !errors.Is(err, cause) || errors.Is(err, context.DeadlineExceeded) || d.Stage != "encode" || d.Actual != "none" {
		t.Fatalf("later deadline replaced independent failure: %+v %v", d, err)
	}
}

func TestIntelWorkRetainsJoinedFailureDuringCancellation(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("independent segment failure")
	joined := errors.Join(cause, context.Canceled)
	plan := IntelGenerationPlan{Config: IntelGenerationConfig{Backend: "vaapi"}}
	d, err := runIntelGenerationWork(ctx, plan, func(context.Context) error { return joined },
		func(context.Context) error { t.Fatal("fallback during cancellation"); return nil }, nil, func(string) error { return nil })
	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || diagnostics.Split(err).Count != 1 || d.Actual != "none" {
		t.Fatalf("joined failure lost or cancellation expanded: %+v %v", d, err)
	}
}

func TestIntelWorkRetainsExecutionCancellationEvidence(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, stage := range []string{"filter", "generation"} {
			t.Run(fmt.Sprintf("%s/%s", stage, cause), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				command := &GenerationCommandError{Err: WithGenerationExecutionCause(&exec.ExitError{Stderr: []byte("synthetic interrupted child")}, cause), Admitted: 1, Started: true}
				plan := IntelGenerationPlan{Config: IntelGenerationConfig{Backend: "vaapi"}, Probes: []IntelProbeStep{{Stage: stage}}}
				if stage == "generation" {
					plan.Probes = nil
				}
				fail := func(context.Context) error { cancel(); return command }
				d, err := runIntelGenerationWork(ctx, plan, fail, nil, func(ctx context.Context, _ Args) error { return fail(ctx) }, func(string) error { return nil })
				var retained *GenerationCommandError
				if !errors.Is(err, cause) || !errors.As(err, &retained) || retained != command || d.Actual != "none" || d.Stage != stage {
					t.Fatalf("lost execution cause/command evidence: %+v %v", d, err)
				}
				if cause == context.Canceled && diagnostics.Split(err).Count != 0 {
					t.Fatal("execution cancellation should stay omitted", err)
				}
			})
		}
	}
}
