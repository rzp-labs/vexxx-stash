package analytics

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/pkg"
	"github.com/stashapp/stash/pkg/python"
)

type PackageFailureContext struct {
	Operation      string
	PackageID      string
	JobCorrelation string
}

// CapturePackageInstallFailure runs once per failed package at the install/update
// task boundary. The package manager and aggregate job error have no capture hook.
// Fallback module failures share one event; cancellation emits no exception.
func CapturePackageInstallFailure(ctx context.Context, err error, info PackageFailureContext) {
	if client == nil || err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	_ = client.Enqueue(PackageInstallException(err, info))
}

func PackageInstallException(err error, info PackageFailureContext) posthog.Exception {
	operation := info.Operation
	if operation != "install" && operation != "update" {
		operation = "install"
	}
	stage := "package_install"
	var stack []uintptr
	var install *pkg.InstallError
	if errors.As(err, &install) {
		stage, stack = install.Stage, install.Stack
	}
	properties := ReleaseProperties().Set("$process_person_profile", false).Set("$exception_level", "error").
		Set("failure_origin", "package_install").Set("job_correlation", safeCorrelation(info.JobCorrelation)).
		Set("package_operation", operation).Set("package_id", python.SanitizeDiagnostic(info.PackageID, nil)).
		Set("package_files_installed", stage == "python_dependencies")

	// Bound the event even when a plugin has many failing fallback imports. Keep
	// the count of omitted diagnostics; local batch execution still tries all.
	const maxFailures = 8
	var diagnostics []map[string]any
	count := 0
	seen := make(map[error]bool)
	var visit func(error)
	visit = func(failure error) {
		if joined, ok := failure.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
			return
		}
		if child := errors.Unwrap(failure); child != nil && containsJoinedError(child) {
			visit(child)
			return
		}
		if failure == nil || errors.Is(failure, context.Canceled) {
			return
		}
		if comparableError(failure) {
			if seen[failure] {
				return
			}
			seen[failure] = true
		}
		count++
		if len(diagnostics) == maxFailures {
			return
		}
		cause, output, failureStage := failure.Error(), "", stage
		var command *python.CommandError
		module := ""
		if errors.As(failure, &command) {
			cause, output, failureStage = command.Err.Error(), command.Output, command.Stage
			module = python.SanitizeDiagnostic(command.Module, nil)
			if module != "" {
				cause = fmt.Sprintf("installing module %s: %s", module, cause)
			}
			if len(diagnostics) == 0 {
				stack = command.Stack
			}
		}
		diagnostic := map[string]any{"stage": failureStage, "error_cause": python.SanitizeDiagnostic(cause, nil)}
		if module != "" {
			diagnostic["module"] = module
		}
		if output != "" {
			diagnostic["python_output"] = python.SanitizeDiagnostic(output, nil)
		}
		var exit *exec.ExitError
		if errors.As(failure, &exit) {
			diagnostic["exit_code"] = exit.ExitCode()
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	visit(err)
	properties.Set("package_failure_count", count).Set("package_failures", diagnostics)
	message := "Package installation failed"
	if len(diagnostics) > 0 {
		primary := diagnostics[0]
		properties.Set("package_stage", primary["stage"]).Set("package_error_cause", primary["error_cause"])
		message = primary["stage"].(string) + ": " + primary["error_cause"].(string)
		if output, ok := primary["python_output"]; ok {
			properties.Set("python_output", output)
			message += "\n" + output.(string)
		}
		if code, ok := primary["exit_code"]; ok {
			properties.Set("python_exit_code", code)
		}
		if module, ok := primary["module"]; ok {
			properties.Set("python_module", module)
		}
	}
	handled, synthetic := true, false
	return posthog.Exception{Timestamp: time.Now(), DistinctId: "server", Properties: properties,
		ExceptionList: []posthog.ExceptionItem{{Type: "PackageInstallError", Value: python.SanitizeDiagnostic(message, nil),
			Mechanism: &posthog.ExceptionMechanism{Handled: &handled, Synthetic: &synthetic}, Stacktrace: packageFailureStack(stack)}}}
}

// Resolve the stack saved at the failure, not a later stack in the observer.
// PostHog raw frames use bottom-up order and repository-relative filenames.
func packageFailureStack(stack []uintptr) *posthog.ExceptionStacktrace {
	if len(stack) == 0 {
		return nil
	}
	result := &posthog.ExceptionStacktrace{Type: "raw"}
	frames := runtime.CallersFrames(stack)
	for {
		frame, more := frames.Next()
		filename := diagnosticSourcePath(frame.File)
		inApp := strings.HasPrefix(frame.Function, "github.com/stashapp/stash/")
		if inApp {
			// The shared path helper chooses the last /pkg/ segment, which
			// collapses pkg/pkg/manager.go. Use the declaring package instead.
			packageFunction := strings.TrimPrefix(frame.Function, "github.com/stashapp/stash/")
			if dot := strings.IndexByte(packageFunction, '.'); dot >= 0 {
				filename = packageFunction[:dot] + "/" + filepath.Base(frame.File)
			}
		}
		result.Frames = append(result.Frames, posthog.StackFrame{Filename: filename, LineNo: frame.Line,
			Function: frame.Function, Platform: "go", InApp: inApp})
		if !more {
			break
		}
	}
	for i, j := 0, len(result.Frames)-1; i < j; i, j = i+1, j-1 {
		result.Frames[i], result.Frames[j] = result.Frames[j], result.Frames[i]
	}
	return result
}
