package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

func TestPanicExceptionPreservesRuntimeCauseAndActualPanicFrame(t *testing.T) {
	func() {
		defer func() {
			exception := PanicException(recover())
			item := exception.ExceptionList[0]
			if item.Type != "RuntimePanic" || !strings.HasPrefix(item.Value, "runtime error: index out of range [2] with length 0") {
				t.Fatalf("lost runtime cause: %+v", item)
			}
			found := false
			for _, frame := range item.Stacktrace.Frames {
				if strings.Contains(frame.Function, "panicBoundsForDiagnostics") {
					found = frame.LineNo > 0 && frame.Filename == "internal/analytics/exceptions_test.go"
				}
				if strings.HasPrefix(frame.Filename, "/") || strings.Contains(frame.Filename, "Users/") {
					t.Errorf("local source root leaked: %s", frame.Filename)
				}
			}
			if !found || item.Mechanism.Handled == nil || *item.Mechanism.Handled {
				t.Fatal("missing original panic frame or fatal mechanism")
			}
		}()
		panicBoundsForDiagnostics()
	}()
}

//go:noinline
func panicBoundsForDiagnostics() {
	values := []int{}
	_ = values[2]
}

func TestPanicExceptionRedactsPrivateValuesAndKeepsFilesystemOperation(t *testing.T) {
	for _, value := range []any{
		`"Jane Smith private.mp4" token=secret`,
		errors.New("https://private/media.mp4?token=secret"),
		&fs.PathError{Op: "open", Path: "/mnt/user/media/private.mp4", Err: syscall.EACCES},
		&fs.PathError{Op: "unfamiliarOperation", Path: "C:\\Users\\private\\media.mp4", Err: errors.New("token=secret")},
		fmt.Errorf("opening media: %w", &fs.PathError{Op: "open", Path: "/mnt/user/Jane Doe/private.funscript", Err: syscall.EACCES}),
	} {
		exception := PanicException(value)
		encoded, err := json.Marshal(exception)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "Jane") || strings.Contains(string(encoded), "Doe") {
			t.Fatalf("private panic content escaped: %s", encoded)
		}
		if err, ok := value.(*fs.PathError); ok && err.Err == syscall.EACCES && exception.ExceptionList[0].Value != "open [location redacted]: permission denied" {
			t.Fatal("lost filesystem diagnostic")
		}
	}
}

func TestDiagnosticBinaryPathPreservesReleaseIdentity(t *testing.T) {
	for input, expected := range map[string]string{
		"/usr/bin/stash":             "/usr/bin/stash",
		"/usr/local/bin/stash":       "/usr/local/bin/stash",
		"/Users/private/build/stash": "stash",
		"/mnt/user/private/stash":    "stash",
	} {
		if actual := diagnosticBinaryPath(input); actual != expected {
			t.Errorf("%q: got %q, want %q", input, actual, expected)
		}
	}
}
