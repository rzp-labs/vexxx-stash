package ffmpeg

import "errors"

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
