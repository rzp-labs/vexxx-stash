package ffmpeg

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"sync/atomic"
)

// GenerationExecutionError retains one process failure and the execution
// context cause. It is not a join of two task failures. External kills with a
// live context retain only their original process error.
type GenerationExecutionError struct {
	Err, Cause error
}

func (e *GenerationExecutionError) Error() string        { return e.Err.Error() + ": " + e.Cause.Error() }
func (e *GenerationExecutionError) Unwrap() error        { return e.Err }
func (e *GenerationExecutionError) Is(target error) bool { return errors.Is(e.Cause, target) }

func WithGenerationExecutionCause(err, cause error) error {
	if err == nil || cause == nil {
		return err
	}
	return &GenerationExecutionError{Err: err, Cause: cause}
}

// TrackGenerationExecutionCause observes the existing CommandContext cancel
// action without changing it. Call before Start and apply the returned wrapper
// after Wait: a context ending during pipe drain did not interrupt the child.
func TrackGenerationExecutionCause(cmd *exec.Cmd, ctx context.Context) func(error) error {
	var interrupted atomic.Bool
	if cancel := cmd.Cancel; cancel != nil {
		cmd.Cancel = func() error {
			err := cancel()
			if err == nil {
				interrupted.Store(true)
			}
			return err
		}
	}
	return func(err error) error {
		if !interrupted.Load() {
			return err
		}
		// On Unix a normal exit remains independent even if Kill raced with
		// reaping the exited child. Windows reports terminated processes using
		// ordinary exit codes, so retain the successful cancel action there.
		var exit *exec.ExitError
		if runtime.GOOS != "windows" && errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return err
		}
		return WithGenerationExecutionCause(err, ctx.Err())
	}
}
