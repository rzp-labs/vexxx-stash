package analytics

import (
	"context"
	"errors"
	"os/exec"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/plugin"
)

// Completed plugin failures are reported here, including hooks which intentionally continue.
// The retained error identity deduplicates a later job/API observer after accepted enqueue.
func CapturePluginFailure(ctx context.Context, err error) {
	if client == nil || !shouldCapture(ctx, err) {
		return
	}
	var failure *plugin.ExecutionError
	if !errors.As(err, &failure) {
		return
	}
	captureException(ctx, err, pluginFailureException(ctx, err, failure))
}

func pluginFailureException(ctx context.Context, err error, failure *plugin.ExecutionError) posthog.Exception {
	properties := posthog.NewProperties().Set("plugin_id", diagnostics.Safe(failure.PluginID, nil)).
		Set("plugin_operation", failure.Operation).Set("plugin_hook", failure.Hook).
		Set("job_correlation", safeCorrelation(job.Correlation(ctx))).
		Set("plugin_output", failure.Output).Set("diagnostic_omitted_bytes", failure.OutputOmittedBytes)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ProcessState != nil {
		properties.Set("plugin_exit_code", exit.ExitCode())
	}
	event := FailureException(err, "plugin", properties, diagnostics.Private(ctx))
	event.ExceptionList[0].Type = "PluginExecutionError"
	return event
}
