package python

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/stashapp/stash/pkg/diagnostics"
)

const MaxDiagnosticBytes = 4096

// CommandError keeps structured process diagnostics without serializing command
// arguments. Err remains wrapped for errors.As/Is (including exec.ExitError).
// Output is redacted and bounded before logging or job-status formatting.
type CommandError struct {
	Stage              string
	Module             string
	Err                error
	Output             string
	Stack              []uintptr
	NativeStack        diagnostics.Stack
	OutputOmittedBytes int
}

func (e *CommandError) Error() string {
	text := e.Summary()
	if output := SanitizeDiagnostic(e.Output, nil); output != "" {
		text += "\nOutput: " + output
	}
	return text
}

// Summary keeps module identity and process cause independent of verbose output.
func (e *CommandError) Summary() string {
	text := fmt.Sprintf("%s failed: %s", e.Stage, diagnostics.Summary(e.Err.Error(), diagnostics.ErrorPrivate(e.Err), MaxDiagnosticBytes).Value)
	if module := SanitizeDiagnostic(e.Module, nil); module != "" {
		text = "installing module " + module + ": " + text
	}
	return text
}

func (e *CommandError) DiagnosticOutput() string { return e.Output }

func (e *CommandError) Unwrap() error { return e.Err }

func newCommandError(stage string, err error, output []byte, private []string) *CommandError {
	stack := make([]uintptr, 32)
	n := runtime.Callers(2, stack)
	var locations []string
	for _, value := range private {
		if strings.ContainsAny(value, "/\\") {
			locations = append(locations, value)
		}
	}
	safe := diagnostics.Sanitize(string(output), locations, MaxDiagnosticBytes)
	return &CommandError{Stage: stage, Err: err, Output: safe.Value, Stack: stack[:n], NativeStack: diagnostics.CaptureStack(), OutputOmittedBytes: safe.OmittedBytes}
}

// SanitizeDiagnostic retains the job-status output limit while using the shared policy.
func SanitizeDiagnostic(message string, private []string) string {
	// Plain requirement names remain public diagnostic context.
	var locations []string
	for _, value := range private {
		if strings.ContainsAny(value, "/\\") {
			locations = append(locations, value)
		}
	}
	return diagnostics.Sanitize(message, locations, MaxDiagnosticBytes).Value
}
