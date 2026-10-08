package pkg

import (
	"errors"
	"strings"

	"github.com/stashapp/stash/pkg/python"
)

// InstallError retains the failing phase and Go stack. A dependency failure can
// leave package files installed; callers must refresh state even on failure.
type InstallError struct {
	Stage string
	Err   error
	Stack []uintptr
}

func (e *InstallError) Error() string {
	var summaries, outputs []string
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		if command, ok := err.(*python.CommandError); ok {
			summaries = append(summaries, command.Summary())
			if command.Output != "" {
				outputs = append(outputs, command.Output)
			}
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
			return
		}
		var command *python.CommandError
		if errors.As(err, &command) {
			visit(errors.Unwrap(err))
			return
		}
		summaries = append(summaries, python.SanitizeDiagnostic(err.Error(), nil))
	}
	visit(e.Err)
	text := strings.Join(summaries, "\n")
	// Never tail-truncate the module/cause list with the verbose output. The
	// output section has its own redaction and byte bound; Unwrap is unchanged.
	if output := python.SanitizeDiagnostic(strings.Join(outputs, "\n"), nil); output != "" {
		text += "\nOutput: " + output
	}
	return text
}
func (e *InstallError) Unwrap() error { return e.Err }
