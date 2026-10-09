package diagnostics

import (
	"github.com/posthog/posthog-go"
	"time"
)

// Stack saves the SDK's actual native frames and image identity at the failure.
// It does not export anything or infer an origin from a later observer stack.
type Stack struct {
	Trace  *posthog.ExceptionStacktrace
	Images []posthog.DebugImage
}

func CaptureStack() Stack {
	event := posthog.NewDefaultException(time.Now(), "server", "Failure", "Failure")
	return Stack{Trace: event.ExceptionList[0].Stacktrace, Images: event.DebugImages}
}

// Copy gives each report ownership of mutable SDK slices before path rewriting.
func (s Stack) Copy() Stack {
	result := Stack{Images: append([]posthog.DebugImage(nil), s.Images...)}
	if s.Trace != nil {
		trace := *s.Trace
		trace.Frames = append([]posthog.StackFrame(nil), s.Trace.Frames...)
		result.Trace = &trace
	}
	return result
}
