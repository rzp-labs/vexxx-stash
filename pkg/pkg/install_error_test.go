package pkg

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/python"
)

func TestVerboseJoinedInstallFailureKeepsEveryModuleCause(t *testing.T) {
	oneCause, twoCause := errors.New("cause-one"), errors.New("cause-two")
	one := &python.CommandError{Stage: "module_install", Module: "badone", Err: oneCause, Output: strings.Repeat("x", 3072)}
	two := &python.CommandError{Stage: "module_install", Module: "badtwo", Err: twoCause, Output: strings.Repeat("y", 3072) + "\nERROR: unfamiliar-wheel-sentinel token=fixture-secret"}
	err := &InstallError{Stage: "python_dependencies", Err: fmt.Errorf("installing Python dependencies: %w", errors.Join(one, two))}
	text := err.Error()
	for _, diagnostic := range []string{"badone", "cause-one", "badtwo", "cause-two"} {
		if !strings.Contains(text, diagnostic) {
			t.Fatal("verbose joined failure displaced an earlier module or cause")
		}
	}
	if strings.Contains(text, "fixture-secret") || !strings.Contains(text, "unfamiliar-wheel-sentinel") {
		t.Fatal("bounded output lost useful unknown cause or bypassed redaction")
	}
	_, output, found := strings.Cut(text, "\nOutput: ")
	if !found || len(output) > python.MaxDiagnosticBytes {
		t.Fatal("verbose output must have its own byte bound")
	}
	var command *python.CommandError
	if !errors.As(err, &command) || command != one || !errors.Is(err, oneCause) || !errors.Is(err, twoCause) {
		t.Fatal("summary formatting altered wrapped error identities")
	}
}

func TestJoinedSummaryHasNoFailureCountTailLimit(t *testing.T) {
	var failures []error
	for i := 0; i < 20; i++ {
		failures = append(failures, &python.CommandError{Stage: "module_install", Module: fmt.Sprintf("module-%02d", i), Err: fmt.Errorf("cause-%02d", i), Output: strings.Repeat("x", python.MaxDiagnosticBytes)})
	}
	err := &InstallError{Stage: "python_dependencies", Err: errors.Join(failures...)}
	summary, output, found := strings.Cut(err.Error(), "\nOutput: ")
	if !found || len(output) > python.MaxDiagnosticBytes {
		t.Fatal("joined verbose output lost its independent bound")
	}
	for i := 0; i < 20; i++ {
		if strings.Count(summary, fmt.Sprintf("module-%02d", i)) != 1 || !strings.Contains(summary, fmt.Sprintf("cause-%02d", i)) {
			t.Fatal("joined summary lost a failure identity/cause")
		}
	}
}

func TestNonPythonInstallErrorRetainsOperationAndCause(t *testing.T) {
	cause := errors.New("permission denied")
	err := &InstallError{Stage: "package_extract", Err: fmt.Errorf("writing package data: %w", cause)}
	if err.Error() != "writing package data: permission denied" || !errors.Is(err, cause) {
		t.Fatal("formatting a Python batch changed an unrelated installation error")
	}
}
