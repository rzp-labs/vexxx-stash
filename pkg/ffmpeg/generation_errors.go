package ffmpeg

import (
	"errors"
	"github.com/stashapp/stash/pkg/generationbudget"
)

// GenerationCommandError retains command admission facts alongside the original
// local error. Arguments and private paths are never event properties.
type GenerationCommandError struct {
	Err           error
	Admitted      int
	Limits        generationbudget.Settings
	PrivateValues []string
}

func (e *GenerationCommandError) Error() string { return e.Err.Error() }
func (e *GenerationCommandError) Unwrap() error { return e.Err }

// IntelGenerationError lets the task-level observer report the selected backend
// and failing stage without emitting the free-text local diagnostic Reason.
type IntelGenerationError struct {
	Err        error
	Diagnostic IntelGenerationDiagnostic
	Source     IntelSource
}

func (e *IntelGenerationError) Error() string { return e.Err.Error() }
func (e *IntelGenerationError) Unwrap() error { return e.Err }

func WithIntelGenerationDiagnostic(err error, d IntelGenerationDiagnostic, source IntelSource) error {
	if err == nil {
		return nil
	}
	var previous *IntelGenerationError
	if source.Width == 0 && errors.As(err, &previous) {
		source = previous.Source
	}
	return &IntelGenerationError{Err: err, Diagnostic: d, Source: source}
}
