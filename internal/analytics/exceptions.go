package analytics

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/internal/build"
)

var runtimePanicMessage = regexp.MustCompile(`^runtime error: (?:invalid memory address or nil pointer dereference|integer divide by zero|index out of range \[-?\d+\](?: with length \d+)?|slice bounds out of range \[[0-9: -]+\](?: with (?:length|capacity) \d+)?|makeslice: len out of range|makeslice: cap out of range|assignment to entry in nil map|hash of unhashable type [A-Za-z0-9_.*\[\]]+|comparing uncomparable type [A-Za-z0-9_.*\[\]]+)$`)

// PanicException retains runtime diagnostics and native symbolication addresses,
// while never exporting arbitrary panic text, media paths or local build roots.
func PanicException(value any) posthog.Exception {
	title, message := "ApplicationPanic", "Unrecognized panic text [redacted]"
	if err, ok := value.(runtime.Error); ok {
		title = "RuntimePanic"
		if text := err.Error(); runtimePanicMessage.MatchString(text) {
			message = text
		}
	} else if err, ok := value.(*fs.PathError); ok && err != nil {
		// Keep the failed operation and standard OS cause, never Path or an
		// arbitrary wrapped error string from a filesystem/plugin implementation.
		op := "filesystem operation"
		for _, allowed := range []string{"open", "read", "write", "stat", "lstat", "mkdir", "remove", "rename", "chmod", "chown", "readdir", "close"} {
			if err.Op == allowed {
				op = allowed
				break
			}
		}
		cause := "filesystem error"
		if errors.Is(err.Err, fs.ErrPermission) {
			cause = "permission denied"
		} else if errors.Is(err.Err, fs.ErrNotExist) {
			cause = "file does not exist"
		} else if errors.Is(err.Err, fs.ErrExist) {
			cause = "file already exists"
		} else if errors.Is(err.Err, fs.ErrInvalid) {
			cause = "invalid argument"
		} else if errors.Is(err.Err, fs.ErrClosed) {
			cause = "file already closed"
		}
		message = fmt.Sprintf("%s: %s [path redacted]", op, cause)
	}
	exception := posthog.NewDefaultException(time.Now(), "server", title, message)
	version, revision, _ := build.Version()
	exception.Properties = posthog.NewProperties().Set("app_version", version).Set("app_revision", revision).Set("$process_person_profile", false).Set("$exception_level", "fatal")
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
		if index := strings.LastIndex(filename, "/"+root); index >= 0 {
			return filename[index+1:]
		}
		if strings.HasPrefix(filename, root) {
			return filename
		}
	}
	// Standard library/dependency source filenames remain useful without a
	// developer's home directory, checkout name or module cache prefix.
	return filepath.Base(filename)
}
