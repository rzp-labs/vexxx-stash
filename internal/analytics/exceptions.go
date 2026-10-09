package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/diagnostics"
)

// PanicException retains unfamiliar causes and the SDK's actual panic stack.
func PanicException(value any) posthog.Exception { return panicException(value, nil) }
func panicException(value any, private []string) posthog.Exception {
	title, message := "ApplicationPanic", ""
	switch v := value.(type) {
	case string:
		message = v
	case error:
		message = v.Error()
	case bool, int, int32, int64, uint, uint64, float32, float64:
		message = fmt.Sprint(v)
	default:
		clean := safeProperties(posthog.NewProperties().Set("panic_context", value))["panic_context"]
		data, err := json.Marshal(clean)
		if err == nil {
			message = string(data)
		} else {
			message = "panic value could not be serialized"
		}
	}
	if err, ok := value.(error); ok {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			private = append(private, pathErr.Path)
		}
	}
	if _, ok := value.(runtime.Error); ok {
		title = "RuntimePanic"
	}
	message = diagnostics.Summary(message, private, diagnostics.MaxTextBytes).Value
	if message == "" {
		message = "application panic (empty value)"
	}
	exception := posthog.NewDefaultException(time.Now(), "server", title, message)
	exception.Properties = ReleaseProperties().Set("$process_person_profile", false).Set("$exception_level", "fatal")
	handled, synthetic := false, false
	exception.ExceptionList[0].Mechanism = &posthog.ExceptionMechanism{Handled: &handled, Synthetic: &synthetic}
	for i := range exception.ExceptionList {
		if stack := exception.ExceptionList[i].Stacktrace; stack != nil {
			for j := range stack.Frames {
				stack.Frames[j].Filename = diagnosticSourcePath(stack.Frames[j].Filename)
			}
		}
	}
	for i := range exception.DebugImages {
		exception.DebugImages[i].CodeFile = diagnosticBinaryPath(exception.DebugImages[i].CodeFile)
	}
	return exception
}

func diagnosticBinaryPath(filename string) string {
	// Preserve the SDK's standard release image identity. These fixed public
	// executable locations contain no private installation/build directory.
	if filename == "/usr/bin/stash" || filename == "/usr/local/bin/stash" {
		return filename
	}
	return "stash"
}

func diagnosticSourcePath(filename string) string {
	filename = filepath.ToSlash(filename)
	for _, root := range []string{"internal/", "pkg/", "cmd/"} {
		if strings.HasPrefix(filename, root) {
			return filename
		}
		if index := strings.Index(filename, "/"+root); index >= 0 {
			return filename[index+1:]
		}
	}
	// Standard library/dependency source filenames remain useful without a
	// developer's home directory, checkout name or module cache prefix.
	return filepath.Base(filename)
}
