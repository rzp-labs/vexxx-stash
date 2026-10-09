package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/session"
)

func standaloneStartupFixture(t *testing.T) (Task, string, *int) {
	t.Helper()
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	cfg := pluginHelperConfig(t, "gatedSuccess")
	server := pluginTestConfig{root: t.TempDir()}
	calls := new(int)
	cache := &Cache{config: server, plugins: []Config{cfg}, sessionStore: session.NewStore(server),
		OnError: func(context.Context, error) { *calls += 1 }}
	release := filepath.Join(t.TempDir(), "release")
	task, err := cache.CreateTask(context.Background(), cfg.id, nil, OperationInput{"release": release}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("ready"), 0600)
		task.Wait()
	})
	return task, release, calls
}

func TestPluginStandaloneRepeatedStartPreservesActiveAndCompletedResult(t *testing.T) {
	task, release, calls := standaloneStartupFixture(t)
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	if err := task.Start(); err == nil || !strings.Contains(err.Error(), "task already started") {
		t.Fatal("duplicate active Start did not preserve backend rejection", err)
	} else {
		var failure *ExecutionError
		if errors.As(err, &failure) {
			t.Error("duplicate active Start became an execution failure")
		}
	}
	if result := task.GetResult(); result != nil {
		t.Error("duplicate Start replaced pending result", result)
	}
	if *calls != 0 {
		t.Errorf("duplicate Start notified execution observer %d times", *calls)
	}
	if err := os.WriteFile(release, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	task.Wait()
	result := task.GetResult()
	if result == nil || result.Err() != nil || result.Output != "done" {
		t.Fatal("duplicate Start hid successful backend result", result)
	}
	if err := task.Start(); err == nil || !strings.Contains(err.Error(), "task already started") {
		t.Fatal("duplicate completed Start did not preserve backend rejection", err)
	}
	if task.GetResult() != result || *calls != 0 {
		t.Fatal("completed Start changed result or emitted execution failure")
	}
}

func TestPluginStandaloneSuccessfulRetryClearsStartupFailure(t *testing.T) {
	task, release, calls := standaloneStartupFixture(t)
	// CreateTask retains a copied Config shared with its backend.
	cfg := task.(*startupReportingTask).metadata.plugin
	executable := cfg.Exec
	cfg.Exec = []string{filepath.Join(t.TempDir(), "missing-synthetic-executable")}
	err := task.Start()
	var failure *ExecutionError
	if !errors.As(err, &failure) || *calls != 1 || task.GetResult().Err() != err {
		t.Fatal("genuine startup failure lost observer/retained identity", err)
	}
	cfg.Exec = executable
	if err := task.Start(); err != nil {
		t.Fatal("backend-supported startup retry failed", err)
	}
	if result := task.GetResult(); result != nil {
		t.Error("successful retry retained failed-start result", result)
	}
	if err := os.WriteFile(release, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	task.Wait()
	result := task.GetResult()
	if result == nil || result.Err() != nil || result.Output != "done" || *calls != 1 {
		t.Fatal("successful retry hid output or changed failure count", result, *calls)
	}
}

func TestPluginStandaloneRejectedJSRetryPreservesOriginalStartupFailure(t *testing.T) {
	cfg := pluginHelperConfig(t, "success")
	cfg.Interface, cfg.Exec = InterfaceEnumJS, []string{"missing-synthetic-script.js"}
	server := pluginTestConfig{root: t.TempDir()}
	calls := 0
	cache := Cache{config: server, plugins: []Config{cfg}, sessionStore: session.NewStore(server),
		OnError: func(context.Context, error) { calls++ }}
	task, err := cache.CreateTask(context.Background(), cfg.id, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	initial := task.Start()
	var failure *ExecutionError
	if !errors.As(initial, &failure) || calls != 1 {
		t.Fatal("genuine JS startup failure was not reported", initial)
	}
	result := task.GetResult()
	rejected := task.Start()
	if rejected == nil || !strings.Contains(rejected.Error(), "task already started") || errors.As(rejected, &failure) {
		t.Fatal("rejected JS retry changed baseline validation error", rejected)
	}
	if task.GetResult() != result || calls != 1 {
		t.Fatal("rejected JS retry replaced original failure or reported again")
	}
}
