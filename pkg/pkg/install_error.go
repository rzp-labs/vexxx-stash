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
		if child := errors.Unwrap(err); child != nil && errors.As(child, &command) {
			// Ordinary %w wrappers embed the child's Error verbatim. Keep only
			// their context here; the child formatter collects output separately.
			prefix, suffix, embedded := strings.Cut(err.Error(), child.Error())
			first := len(summaries)
			visit(child)
			if embedded && len(summaries) > first {
				summaries[first] = sanitizeInstallContext(prefix) + summaries[first]
				summaries[len(summaries)-1] += sanitizeInstallContext(suffix)
			}
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
func (e *InstallError) DiagnosticTransparent() bool { return true }
func (e *InstallError) Unwrap() error               { return e.Err }

// Preserve a wrapper's separating whitespace while sanitizing its own text.
// SanitizeDiagnostic trims whitespace; applying it to the whole error would
// instead let verbose output displace operation labels and module summaries.
func sanitizeInstallContext(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text
	}
	start := strings.Index(text, trimmed)
	return text[:start] + python.SanitizeDiagnostic(trimmed, nil) + text[start+len(trimmed):]
}
