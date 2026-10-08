package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/pkg"
	"github.com/stashapp/stash/pkg/python"
)

func TestPackageDiagnosticsBoundJoinedFailuresAndRemoveSecrets(t *testing.T) {
	var failures []error
	for i := 0; i < 20; i++ {
		failures = append(failures, &python.CommandError{Stage: "module_install", Err: errors.New("token=fixture-secret"), Output: strings.Repeat("x", 6000) + "\nwheel-sentinel api_key=fixture-output-secret"})
	}
	err := &pkg.InstallError{Stage: "python_dependencies", Err: errors.Join(failures...)}
	event := PackageInstallException(err, PackageFailureContext{Operation: "update", PackageID: "PythonTools"})
	details := event.Properties["package_failures"].([]map[string]any)
	if len(details) != 8 || event.Properties["package_failure_count"] != 20 {
		t.Fatal("joined event lost total count or exceeded diagnostic limit")
	}
	for _, detail := range details {
		output := detail["python_output"].(string)
		if len(output) > python.MaxDiagnosticBytes || !strings.Contains(output, "wheel-sentinel") {
			t.Fatal("bounded joined output lost useful final cause")
		}
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), "fixture-secret") || strings.Contains(string(encoded), "fixture-output-secret") {
		t.Fatal("structured diagnostics bypassed credential redaction")
	}
}

func TestPackageCaptureDisabledRemainsUsable(t *testing.T) {
	previous := client
	client = nil
	t.Cleanup(func() { client = previous })
	CapturePackageInstallFailure(context.Background(), errors.New("installation failure"), PackageFailureContext{})
	CapturePackageInstallFailure(context.Background(), nil, PackageFailureContext{})
}

func TestModuleContextIsSanitizedAtTelemetryBoundary(t *testing.T) {
	command := &python.CommandError{Stage: "module_install", Module: "password=correct horse battery staple", Err: errors.New("exit status 23")}
	event := PackageInstallException(command, PackageFailureContext{PackageID: "PythonTools"})
	encoded, _ := json.Marshal(event)
	for _, private := range []string{"correct", "horse", "battery", "staple"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("module context bypassed credential redaction")
		}
	}
	if event.Properties["python_module"] != "[credential redacted]" || !strings.Contains(event.Properties["package_error_cause"].(string), "exit status 23") {
		t.Fatal("sanitizing module identity lost the process cause")
	}
}
