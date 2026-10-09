package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/posthog/posthog-go"

	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/plugin"
)

func TestPluginFailureOfflineSDKOnceAndPrivacy(t *testing.T) {
	_, received := offlineSDK(t, 0)
	ctx := diagnostics.WithState(context.Background())
	for i := 0; i < 2; i++ {
		failure := &plugin.ExecutionError{PluginID: "SyntheticPythonTools", Operation: "hook", Hook: "Scene.Update.Post", Err: errors.New("exit status 1"),
			Output: "ModuleNotFoundError: No module named 'stashapi'\nAuthorization: Bearer synthetic-secret\nFileNotFoundError: '/private/synthetic/venv/site-packages'", OutputOmittedBytes: 42}
		CapturePluginFailure(ctx, failure)
		CaptureJobFailure(ctx, fmt.Errorf("job wrapper: %w", failure), "", "synthetic")
		if id := CaptureAPIFailure(ctx, failure, "runPlugin", "mutation", ""); id != "" {
			t.Fatal("duplicate API capture")
		}
	}
	events := received()
	if len(events) != 2 {
		t.Fatalf("distinct failures should each capture exactly once: %d", len(events))
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"synthetic-secret", "/private/synthetic"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private diagnostic leaked: %s", private)
		}
	}
	for _, expected := range []string{"ModuleNotFoundError", "stashapi", "PluginExecutionError", "SyntheticPythonTools", "Scene.Update.Post", "app_revision", "capture_boundary"} {
		if !strings.Contains(string(data), expected) {
			t.Errorf("lost %s", expected)
		}
	}
	if events[0]["uuid"] == events[1]["uuid"] || events[0]["uuid"] == nil {
		t.Fatal("missing distinct accepted UUIDs")
	}
}

type rejectPluginOnce struct {
	posthog.Client
	rejected bool
}

func (c *rejectPluginOnce) Enqueue(message posthog.Message) error {
	if !c.rejected {
		c.rejected = true
		return errors.New("synthetic enqueue failure")
	}
	return c.Client.Enqueue(message)
}

func TestPluginRejectedEnqueueRetriesAtJobBoundaryWithCause(t *testing.T) {
	_, received := offlineSDK(t, 0)
	client = &rejectPluginOnce{Client: client}
	ctx := diagnostics.WithState(context.Background())
	failure := &plugin.ExecutionError{PluginID: "SyntheticPlugin", Operation: "task", Err: errors.New("exit status 1"), Output: "ModuleNotFoundError: No module named 'stashapi'"}
	CapturePluginFailure(ctx, failure)
	if diagnostics.Reported(ctx, failure) {
		t.Fatal("rejected enqueue marked as reported")
	}
	CaptureJobFailure(ctx, failure, "", "synthetic")
	CaptureJobFailure(ctx, failure, "", "synthetic")
	events := received()
	if len(events) != 1 {
		t.Fatalf("fallback capture count=%d", len(events))
	}
	data, _ := json.Marshal(events)
	if !strings.Contains(string(data), "ModuleNotFoundError") || !strings.Contains(string(data), "PluginExecutionError") {
		t.Fatal("fallback lost plugin diagnostics")
	}
}

func TestPluginCancellationNoTelemetry(t *testing.T) {
	_, received := offlineSDK(t, 0)
	ctx, cancel := context.WithCancel(diagnostics.WithState(context.Background()))
	cancel()
	CapturePluginFailure(ctx, &plugin.ExecutionError{Err: context.Canceled})
	cmd := exec.Command("/bin/sh", "-c", "kill -TERM $$")
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatal("fixture not signal terminated", err)
	}
	CapturePluginFailure(ctx, &plugin.ExecutionError{Err: err, PluginID: "SyntheticPlugin"})
	if events := received(); len(events) != 0 {
		t.Fatal("cancellation emitted exception")
	}
}

func TestPluginRealFailureAfterCancellationStillCaptured(t *testing.T) {
	_, received := offlineSDK(t, 0)
	ctx, cancel := context.WithCancel(diagnostics.WithState(context.Background()))
	cancel()
	err := exec.Command("/bin/sh", "-c", "exit 7").Run()
	CapturePluginFailure(ctx, &plugin.ExecutionError{Err: err, PluginID: "SyntheticPlugin", Operation: "task"})
	events := received()
	if len(events) != 1 {
		t.Fatal("real exit failure was suppressed")
	}
	if events[0]["properties"].(map[string]any)["plugin_exit_code"] != float64(7) {
		t.Fatal("exit code lost")
	}
}
