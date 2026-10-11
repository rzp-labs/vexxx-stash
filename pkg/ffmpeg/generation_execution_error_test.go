package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestGenerationExecutionCauseRequiresSuccessfulCancelAction(t *testing.T) {
	for _, actionErr := range []error{nil, os.ErrProcessDone, errors.New("cancel failed")} {
		t.Run(fmt.Sprint(actionErr), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			cmd := &exec.Cmd{Cancel: func() error { calls++; return actionErr }}
			wrap := TrackGenerationExecutionCause(cmd, ctx)
			original := errors.New("process failure")
			cancel()
			if got := wrap(original); got != original {
				t.Fatal("context state alone changed the error", got)
			}
			if got := cmd.Cancel(); got != actionErr || calls != 1 {
				t.Fatal("original cancellation action changed", got, calls)
			}
			got := wrap(original)
			if errors.Is(got, context.Canceled) != (actionErr == nil) || !errors.Is(got, original) {
				t.Fatal("cancellation action misattributed", got)
			}
			if wrap(nil) != nil {
				t.Fatal("successful process result became a failure")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	original := errors.New("process failure")
	if got := TrackGenerationExecutionCause(&exec.Cmd{}, ctx)(original); got != original {
		t.Fatal("missing cancellation action changed error", got)
	}
}
