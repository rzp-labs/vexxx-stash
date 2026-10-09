package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
