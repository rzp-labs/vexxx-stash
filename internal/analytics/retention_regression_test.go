package analytics

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/pkg"
	"github.com/stashapp/stash/pkg/python"
)

// These tests use only shipped APIs, so they also run unchanged on v0.2.2.
func TestRegressionUnknownPanicRetainsUsefulCause(t *testing.T) {
	event := PanicException("unfamiliar driver-sentinel password='fixture-secret'")
	if !strings.Contains(event.ExceptionList[0].Value, "unfamiliar driver-sentinel") || strings.Contains(event.ExceptionList[0].Value, "fixture-secret") {
		t.Fatal("panic discarded cause or retained credential")
	}
}
func TestRegressionPackageRequirementsWrapperRetainedInTelemetry(t *testing.T) {
	var failures []error
	for i := 0; i < 12; i++ {
		failures = append(failures, fmt.Errorf("installing Python dependencies: installing requirements: %w", &python.CommandError{Stage: "module_install", Module: fmt.Sprintf("widget%d", i), Err: errors.New("wheel-cause"), Output: strings.Repeat("x", 8000) + "\nfinal pip cause"}))
	}
	event := PackageInstallException(&pkg.InstallError{Stage: "python_dependencies", Err: errors.Join(failures...)}, PackageFailureContext{})
	details := event.Properties["package_failures"].([]map[string]any)
	if len(details) != 12 {
		t.Fatal("earlier/all module details omitted without sufficient budget")
	}
	for i, detail := range details {
		cause := detail["error_cause"].(string)
		if !strings.Contains(cause, "installing requirements") || !strings.Contains(cause, fmt.Sprintf("widget%d", i)) {
			t.Fatal("requirements wrapper/module lost")
		}
	}
	if !strings.Contains(event.ExceptionList[0].Value, "widget0") {
		t.Fatal("verbose output displaced headline")
	}
}
func TestRegressionGenerationWrapperAndVersionRetained(t *testing.T) {
	exit := &exec.ExitError{Stderr: []byte("ffmpeg version diagnostic-version-sentinel\nunknown driver-sentinel")}
	err := fmt.Errorf("sprite extraction: %w", &ffmpeg.GenerationCommandError{Err: fmt.Errorf("decoder setup: %w", exit)})
	event := GenerationException(err, GenerationFailureContext{})
	message := event.ExceptionList[0].Value
	for _, part := range []string{"sprite extraction", "decoder setup", "diagnostic-version-sentinel", "unknown driver-sentinel"} {
		if !strings.Contains(message, part) {
			t.Fatalf("lost %s", part)
		}
	}
}
