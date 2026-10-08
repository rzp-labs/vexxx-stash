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

func TestRequirementsFailurePreservesOperationWrappers(t *testing.T) {
	cause := errors.New("exit status 23")
	command := &python.CommandError{Stage: "requirements_analyze", Err: cause, Output: "unknown-requirements-sentinel"}
	err := &InstallError{Stage: "python_dependencies", Err: fmt.Errorf("installing Python dependencies: %w", fmt.Errorf("installing requirements: %w", command))}
	want := "installing Python dependencies: installing requirements: requirements_analyze failed: exit status 23\nOutput: unknown-requirements-sentinel"
	if err.Error() != want {
		t.Fatal("requirements failure discarded or duplicated operation context")
	}
	var found *python.CommandError
	if !errors.As(err, &found) || found != command || !errors.Is(err, cause) {
		t.Fatal("preserving operation context changed error identity")
	}
}

func TestWrappedVerboseJoinPreservesContextAndEveryModule(t *testing.T) {
	var failures []error
	var causes []error
	for i := 0; i < 20; i++ {
		cause := fmt.Errorf("cause-%02d", i)
		causes = append(causes, cause)
		failures = append(failures, &python.CommandError{Stage: "module_install", Module: fmt.Sprintf("module-%02d", i), Err: cause, Output: strings.Repeat("x", python.MaxDiagnosticBytes) + "\nunknown-wheel-sentinel"})
	}
	err := &InstallError{Stage: "python_dependencies", Err: fmt.Errorf("installing Python dependencies: %w; batch attempted", fmt.Errorf("scanning modules: %w; scan complete", errors.Join(failures...)))}
	summary, output, found := strings.Cut(err.Error(), "\nOutput: ")
	if !found || len(output) > python.MaxDiagnosticBytes || !strings.Contains(output, "unknown-wheel-sentinel") || strings.Count(err.Error(), "\nOutput: ") != 1 {
		t.Fatal("wrapped join lost the independent useful output bound")
	}
	for _, context := range []string{"installing Python dependencies:", "scanning modules:", "; scan complete", "; batch attempted"} {
		if strings.Count(summary, context) != 1 {
			t.Fatal("wrapped join discarded or duplicated prefix/suffix context")
		}
	}
	for i, cause := range causes {
		if strings.Count(summary, fmt.Sprintf("module-%02d", i)) != 1 || strings.Count(summary, fmt.Sprintf("cause-%02d", i)) != 1 || !errors.Is(err, cause) {
			t.Fatal("wrapper context displaced a module/cause or altered error identity")
		}
	}
	var command *python.CommandError
	if !errors.As(err, &command) || command != failures[0] {
		t.Fatal("wrapped join changed typed command identity")
	}
}

func TestWrapperPrefixAndSuffixAreIndependentlySanitized(t *testing.T) {
	cause := errors.New("exit status 23")
	command := &python.CommandError{Stage: "requirements_install", Err: cause, Output: "unknown-output-sentinel\ntoken=output-secret"}
	err := &InstallError{Stage: "python_dependencies", Err: fmt.Errorf("unknown-prefix token=prefix-secret\nAuthorization: Bearer prefix-auth\nrequirements: %w\nunknown-suffix password=suffix-secret\nCookie: theme=dark; sid=suffix-cookie\nretry context", command)}
	summary, output, found := strings.Cut(err.Error(), "\nOutput: ")
	if !found || len(output) > python.MaxDiagnosticBytes || strings.Count(err.Error(), "\nOutput: ") != 1 {
		t.Fatal("wrapper formatting duplicated or unbounded command output")
	}
	for _, context := range []string{"unknown-prefix", "requirements:", "requirements_install failed: exit status 23", "unknown-suffix", "retry context"} {
		if strings.Count(summary, context) != 1 {
			t.Fatal("prefix/suffix redaction discarded or duplicated useful context")
		}
	}
	for _, private := range []string{"prefix-secret", "prefix-auth", "suffix-secret", "suffix-cookie", "output-secret"} {
		if strings.Contains(err.Error(), private) {
			t.Fatal("wrapper prefix/suffix bypassed targeted redaction")
		}
	}
	if !strings.Contains(output, "unknown-output-sentinel") || !errors.Is(err, cause) {
		t.Fatal("sanitization lost useful output or wrapped cause")
	}
}
