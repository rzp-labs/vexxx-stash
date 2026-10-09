package analytics

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/pkg"
	"github.com/stashapp/stash/pkg/python"
)

type PackageFailureContext struct {
	Operation      string
	PackageID      string
	JobCorrelation string
}

// CapturePackageInstallFailure runs once per failed package at the install/update
// task boundary. Accepted identities deduplicate the aggregate job boundary.
// Fallback module failures share one event; cancellation emits no exception.
func CapturePackageInstallFailure(ctx context.Context, err error, info PackageFailureContext) {
	if client != nil && shouldCapture(ctx, err) {
		captureException(ctx, err, PackageInstallException(err, info))
	}
}

func PackageInstallException(err error, info PackageFailureContext) posthog.Exception {
	operation := info.Operation
	if operation != "install" && operation != "update" {
		operation = "install"
	}
	stage := "package_install"
	var stack []uintptr
	var native diagnostics.Stack
	var install *pkg.InstallError
	if errors.As(err, &install) {
		stage, stack = install.Stage, install.Stack
	}
	properties := ReleaseProperties().Set("$process_person_profile", false).Set("$exception_level", "error").
		Set("failure_origin", "package_install").Set("job_correlation", safeCorrelation(info.JobCorrelation)).
		Set("package_operation", operation).Set("package_id", python.SanitizeDiagnostic(info.PackageID, nil)).
		Set("package_files_installed", stage == "python_dependencies")

	failures := diagnostics.Split(err)
	var details []map[string]any
	var summaries []map[string]any
	outputBudget := 64 * 1024
	omittedBytes := 0
	for _, entry := range failures.Entries {
		failure := entry.Err
		cause := diagnostics.Summary(entry.Cause, diagnostics.ErrorPrivate(entry.Err), diagnostics.MaxTextBytes)
		failureStage := stage
		module := ""
		var command *python.CommandError
		if errors.As(failure, &command) {
			omittedBytes += command.OutputOmittedBytes
			failureStage = command.Stage
			module = diagnostics.Safe(command.Module, nil)
			if len(details) == 0 {
				stack = command.Stack
				native = command.NativeStack.Copy()
			}
		}
		diagnostic := map[string]any{"stage": diagnostics.Safe(failureStage, nil), "error_cause": cause.Value}
		omittedBytes += cause.OmittedBytes
		if module != "" {
			diagnostic["module"] = module
		}
		if entry.Output != "" {
			limit := python.MaxDiagnosticBytes
			if outputBudget < limit {
				limit = outputBudget
			}
			if limit > 16 {
				output := diagnostics.Sanitize(entry.Output, nil, limit)
				diagnostic["python_output"] = output.Value
				omittedBytes += output.OmittedBytes
				outputBudget -= len(output.Value)
			} else {
				omittedBytes += len(entry.Output)
			}
		}
		var exit *exec.ExitError
		if errors.As(failure, &exit) {
			diagnostic["exit_code"] = exit.ExitCode()
		}
		details = append(details, diagnostic)
		compact := map[string]any{"stage": failureStage, "error_cause": diagnostics.Summary(entry.Cause, diagnostics.ErrorPrivate(entry.Err), 1024).Value}
		if module != "" {
			compact["module"] = module
		}
		summaries = append(summaries, compact)
	}
	count := failures.Count
	diagnostics := details
	properties.Set("package_failures_omitted", failures.Omitted).Set("diagnostic_omitted_bytes", omittedBytes)
	properties.Set("package_failure_count", count).Set("package_failures", diagnostics).Set("package_failure_summaries", summaries)
	message := "Package installation failed"
	if len(diagnostics) > 0 {
		primary := diagnostics[0]
		properties.Set("package_stage", primary["stage"]).Set("package_error_cause", primary["error_cause"])
		message = primary["error_cause"].(string)
		if output, ok := primary["python_output"]; ok {
			properties.Set("python_output", output)
			// Output remains a separately bounded property; the headline stays useful.
		}
		if code, ok := primary["exit_code"]; ok {
			properties.Set("python_exit_code", code)
		}
		if module, ok := primary["module"]; ok {
			properties.Set("python_module", module)
		}
	}
	handled, synthetic := true, false
	event := posthog.Exception{Timestamp: time.Now(), DistinctId: "server", Properties: properties,
		ExceptionList: []posthog.ExceptionItem{{Type: "PackageInstallError", Value: message,
			Mechanism: &posthog.ExceptionMechanism{Handled: &handled, Synthetic: &synthetic}, Stacktrace: packageFailureStack(stack)}}}
	if native.Trace != nil {
		event.ExceptionList[0].Stacktrace = native.Trace
		event.DebugImages = native.Images
		event.Properties.Set("stack_origin", "process_failure")
		sanitizeStackImages(&event)
	}
	return event
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
			// Use the declaring package to preserve nested source packages.
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
