package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/plugin"
	"github.com/stashapp/stash/pkg/plugin/common"
	"github.com/stashapp/stash/pkg/session"
)

func TestPluginJobHelperProcess(t *testing.T) {
	if os.Getenv("VEX84_JOB_HELPER") != "1" {
		return
	}
	var input common.PluginInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		os.Exit(91)
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "waiting" {
		// Tell the parent the process is running before cancellation.
		if err := os.WriteFile(input.Args["marker"].(string), []byte("ready"), 0600); err != nil {
			os.Exit(92)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	if mode == "failed" {
		fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'stashapi'")
		os.Exit(7)
	}
	fmt.Fprintln(os.Stdout, `{"output":"ok"}`)
	os.Exit(0)
}

type pluginJobConfig struct{ root string }

func (c pluginJobConfig) GetHost() string              { return "localhost" }
func (c pluginJobConfig) GetPort() int                 { return 9999 }
func (c pluginJobConfig) GetConfigPathAbs() string     { return c.root }
func (c pluginJobConfig) HasTLSConfig() bool           { return false }
func (c pluginJobConfig) GetPluginsPath() string       { return c.root }
func (c pluginJobConfig) GetDisabledPlugins() []string { return nil }
func (c pluginJobConfig) GetPythonPath() string        { return "" }
func (c pluginJobConfig) GetUsername() string          { return "synthetic" }
func (c pluginJobConfig) GetAPIKey() string            { return "" }
func (c pluginJobConfig) GetSessionStoreKey() []byte {
	return []byte("synthetic-session-key-32-bytes!!")
}
func (c pluginJobConfig) GetMaxSessionAge() int                   { return 60 }
func (c pluginJobConfig) ValidateCredentials(string, string) bool { return false }

func TestPluginJobStatusFailureSuccessAndCancellation(t *testing.T) {
	t.Setenv("VEX84_JOB_HELPER", "1")
	for _, mode := range []string{"failed", "success", "waiting"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			manifest := fmt.Sprintf("name: SyntheticPlugin\ninterface: raw\nexec: [%q, '-test.run=^TestPluginJobHelperProcess$', '--', %q]\n", path, mode)
			if err := os.WriteFile(filepath.Join(root, "SyntheticPlugin.yml"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := pluginJobConfig{root}
			cache := plugin.NewCache(cfg)
			cache.RegisterSessionStore(session.NewStore(cfg))
			cache.ReloadPlugins()
			var failures atomic.Int32
			cache.OnError = func(context.Context, error) { failures.Add(1) }
			jobs := job.NewManager()
			defer jobs.StopAndWait(time.Second)
			manager := &Manager{JobManager: jobs, PluginCache: cache}
			marker := filepath.Join(root, "ready")
			id := manager.RunPluginTask(context.Background(), "SyntheticPlugin", nil, nil, plugin.OperationInput{"marker": marker})
			deadline := time.Now().Add(5 * time.Second)
			if mode == "waiting" {
				for time.Now().Before(deadline) {
					if _, err := os.Stat(marker); err == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("synthetic plugin did not start")
				}
				jobs.CancelJob(id)
			}
			for time.Now().Before(deadline) {
				result := jobs.GetJob(id)
				if result != nil && result.EndTime != nil {
					want := job.StatusFinished
					if mode == "failed" {
						want = job.StatusFailed
					}
					if mode == "waiting" {
						want = job.StatusCancelled
					}
					if result.Status != want {
						t.Fatalf("status=%s want=%s", result.Status, want)
					}
					if mode == "failed" && (result.Error == nil || !strings.Contains(*result.Error, "ModuleNotFoundError")) {
						t.Fatal("job cause lost")
					}
					if mode != "failed" && result.Error != nil {
						t.Fatal("success/cancellation acquired error")
					}
					if mode == "failed" && failures.Load() != 1 {
						t.Fatal("plugin observer missing/duplicated")
					}
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("plugin job did not complete")
		})
	}
}

func TestPluginJobStartupFailureRetainsContext(t *testing.T) {
	for _, kind := range []string{"js", "raw", "rpc"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			manifest := fmt.Sprintf("name: SyntheticStartupPlugin\ninterface: %s\nexec: ['missing-synthetic-script']\n", kind)
			if err := os.WriteFile(filepath.Join(root, "SyntheticStartupPlugin.yml"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := pluginJobConfig{root}
			cache := plugin.NewCache(cfg)
			cache.RegisterSessionStore(session.NewStore(cfg))
			cache.ReloadPlugins()
			observed := make(chan error, 2)
			cache.OnError = func(_ context.Context, err error) { observed <- err }
			jobs := job.NewManager()
			defer jobs.StopAndWait(time.Second)
			jobErrors := make(chan error, 1)
			jobs.OnError = func(_ context.Context, err error, _ string) { jobErrors <- err }
			manager := &Manager{JobManager: jobs, PluginCache: cache}
			id := manager.RunPluginTask(context.Background(), "SyntheticStartupPlugin", nil, nil, nil)
			var jobErr error
			select {
			case jobErr = <-jobErrors:
			case <-time.After(3 * time.Second):
				t.Fatal("startup job did not fail")
			}
			var failure *plugin.ExecutionError
			if !errors.As(jobErr, &failure) || failure.PluginID != "SyntheticStartupPlugin" || failure.Operation != "task" || failure.Unwrap() == nil {
				t.Fatal("startup error lost typed plugin context", jobErr)
			}
			select {
			case pluginErr := <-observed:
				if pluginErr != failure {
					t.Fatal("startup observer and job lost shared error identity")
				}
			default:
				t.Fatal("startup observer did not run")
			}
			if len(observed) != 0 {
				t.Fatal("duplicate startup observation")
			}
			deadline := time.Now().Add(time.Second)
			result := jobs.GetJob(id)
			for result != nil && result.EndTime == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
				result = jobs.GetJob(id)
			}
			if result == nil || result.Status != job.StatusFailed || result.Error == nil {
				t.Fatal("startup job failure status lost", result)
			}
		})
	}
}

func TestPluginJobStartupTelemetryOnceAndRejectedEnqueueRetry(t *testing.T) {
	for _, rejectFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectFirst), func(t *testing.T) {
			var mu sync.Mutex
			var events []map[string]any
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Batch []map[string]any }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				events = append(events, body.Batch...)
				mu.Unlock()
				_, _ = io.WriteString(w, `{"status":1}`)
			}))
			defer receiver.Close()
			t.Setenv("POSTHOG_PROJECT_TOKEN", "synthetic-startup-project")
			t.Setenv("POSTHOG_HOST", receiver.URL)
			if err := analytics.Initialize(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = analytics.Close()
				t.Setenv("POSTHOG_PROJECT_TOKEN", "")
				t.Setenv("POSTHOG_HOST", "")
				_ = analytics.Initialize()
			})
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "SyntheticStartupPlugin.yml"), []byte("name: SyntheticStartupPlugin\ninterface: js\nexec: ['missing-synthetic-script']\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := pluginJobConfig{root}
			cache := plugin.NewCache(cfg)
			cache.RegisterSessionStore(session.NewStore(cfg))
			cache.ReloadPlugins()
			cache.OnError = func(ctx context.Context, err error) {
				if rejectFirst {
					_ = analytics.Close()
				}
				analytics.CapturePluginFailure(ctx, err)
				if diagnostics.Reported(ctx, err) == rejectFirst {
					t.Error("startup reservation did not reflect enqueue acceptance")
				}
				if rejectFirst {
					if err := analytics.Initialize(); err != nil {
						t.Error(err)
					}
				}
			}
			jobs := job.NewManager()
			defer jobs.StopAndWait(time.Second)
			observed := make(chan struct{}, 1)
			jobs.OnError = func(ctx context.Context, err error, kind string) {
				analytics.CaptureJobFailure(ctx, err, job.Correlation(ctx), kind)
				observed <- struct{}{}
			}
			manager := &Manager{JobManager: jobs, PluginCache: cache}
			manager.RunPluginTask(context.Background(), "SyntheticStartupPlugin", nil, nil, nil)
			select {
			case <-observed:
			case <-time.After(3 * time.Second):
				t.Fatal("startup job did not report")
			}
			if err := analytics.Close(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(events) != 1 {
				t.Fatalf("startup capture count=%d", len(events))
			}
			props := events[0]["properties"].(map[string]any)
			if props["plugin_id"] != "SyntheticStartupPlugin" || props["plugin_operation"] != "task" || props["failure_origin"] != "plugin" {
				t.Fatal("startup telemetry lost plugin context", props)
			}
		})
	}
}
