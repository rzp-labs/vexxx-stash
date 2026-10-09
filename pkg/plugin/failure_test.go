package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/plugin/common"
	"github.com/stashapp/stash/pkg/plugin/hook"
	"github.com/stashapp/stash/pkg/session"
)

// The helper is a local synthetic plugin; it installs nothing and makes no network request.
func TestPluginHelperProcess(t *testing.T) {
	if os.Getenv("VEX84_PLUGIN_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "rpcFailure" {
		server := rpc.NewServer()
		if err := server.RegisterName("RPCRunner", &pluginHelperRPC{}); err != nil {
			os.Exit(93)
		}
		server.ServeCodec(jsonrpc.NewServerCodec(pluginHelperStdio{os.Stdin, os.Stdout}))
		os.Exit(0)
	}
	var input common.PluginInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		os.Exit(90)
	}
	if marker := os.Getenv("VEX84_HELPER_MARKER"); marker != "" {
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(91)
		}
		_, _ = fmt.Fprintln(file, mode)
		_ = file.Close()
	}
	switch mode {
	case "failed":
		fmt.Fprintln(os.Stderr, "Traceback (most recent call last):")
		fmt.Fprintln(os.Stderr, `  File "/private/synthetic/plugin.py", line 19, in <module>`)
		fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'stashapi'")
		fmt.Fprintf(os.Stderr, "echo=%v cookie=%s\n", input.Args["private"], input.ServerConnection.SessionCookie.Value)
		os.Exit(7)
	case "large":
		fmt.Fprintln(os.Stderr, "Authorization: Bearer "+strings.Repeat("synthetic-secret", 10000))
		fmt.Fprintln(os.Stderr, "FileNotFoundError: synthetic site-packages missing")
		os.Exit(8)
	case "pem":
		fmt.Fprint(os.Stderr, syntheticPEMDiagnostic())
		os.Exit(7)
	case "privateJSON":
		value := input.Args["privateJSON"].(map[string]any)["scene"]
		fmt.Fprintf(os.Stderr, "ModuleNotFoundError: unfamiliar-wheel-sentinel %v\n", value)
		os.Exit(7)
	case "reported":
		fmt.Fprintln(os.Stdout, `{"error":"reported sentinel password=synthetic-secret"}`)
	case "both":
		fmt.Fprintln(os.Stdout, `{"error":"reported sentinel"}`)
		fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'stashapi'")
		os.Exit(9)
	case "waiting":
		for {
			time.Sleep(time.Second)
		}
	default:
		fmt.Fprintln(os.Stderr, "a non-fatal warning")
		fmt.Fprintln(os.Stdout, `{"output":"ok"}`)
	}
	os.Exit(0)
}

type pluginHelperStdio struct {
	io.ReadCloser
	io.Writer
}
type pluginHelperRPC struct{}

func (*pluginHelperRPC) Run(common.PluginInput, *common.PluginOutput) error {
	return errors.New("RPC unfamiliar cause password=synthetic-secret")
}
func (*pluginHelperRPC) Stop(any, *any) error { return nil }

type pluginTestConfig struct{ root string }

func (c pluginTestConfig) GetHost() string              { return "localhost" }
func (c pluginTestConfig) GetPort() int                 { return 9999 }
func (c pluginTestConfig) GetConfigPathAbs() string     { return c.root }
func (c pluginTestConfig) HasTLSConfig() bool           { return false }
func (c pluginTestConfig) GetPluginsPath() string       { return c.root }
func (c pluginTestConfig) GetDisabledPlugins() []string { return nil }
func (c pluginTestConfig) GetPythonPath() string        { return "" }
func (c pluginTestConfig) GetUsername() string          { return "synthetic" }
func (c pluginTestConfig) GetAPIKey() string            { return "" }
func (c pluginTestConfig) GetSessionStoreKey() []byte {
	return []byte("synthetic-session-key-32-bytes!!")
}
func (c pluginTestConfig) GetMaxSessionAge() int                   { return 60 }
func (c pluginTestConfig) ValidateCredentials(string, string) bool { return false }

func pluginHelperConfig(t *testing.T, mode string) Config {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Config{id: "SyntheticPlugin", Name: "Synthetic Plugin", Interface: InterfaceEnumRaw,
		path: filepath.Join(t.TempDir(), "synthetic.yml"), Exec: []string{path, "-test.run=^TestPluginHelperProcess$", "--", mode}}
}

func TestPluginRawFailureRetainsCauseAndRedacts(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	for _, mode := range []string{"failed", "large", "pem", "reported", "both", "success"} {
		t.Run(mode, func(t *testing.T) {
			cfg := pluginHelperConfig(t, mode)
			calls := 0
			task := &rawPluginTask{pluginTask: pluginTask{plugin: &cfg, serverConfig: pluginTestConfig{}, ctx: context.Background(), kind: "task",
				input: common.PluginInput{Args: common.ArgsMap{"private": "Private Alice"}, ServerConnection: common.StashServerConnection{SessionCookie: &http.Cookie{Value: "opaque-cookie-fixture"}}},
				onError: func(_ context.Context, err error) {
					calls++
					if err == nil {
						t.Error("nil error")
					}
				}}}
			if err := task.Start(); err != nil {
				t.Fatal(err)
			}
			task.Wait()
			result := task.GetResult()
			if mode == "success" {
				if result.Err() != nil || calls != 0 {
					t.Fatal("successful stderr became a failure")
				}
				return
			}
			if calls != 1 || result.Err() == nil {
				t.Fatalf("calls=%d result=%#v", calls, result)
			}
			var failure *ExecutionError
			if !errors.As(result.Err(), &failure) {
				t.Fatal("missing typed plugin error")
			}
			if task.GetResult().Err() != result.Err() {
				t.Fatal("error identity changed")
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"Private Alice", "opaque-cookie-fixture", "synthetic-secret", "/private/synthetic", `"Failure"`} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("private value leaked: %s", secret)
				}
			}
			if len(failure.Output) > maxPluginDiagnosticBytes {
				t.Fatal("unbounded diagnostic")
			}
			var exit *exec.ExitError
			if mode != "reported" && (!errors.As(failure, &exit) || exit.ExitCode() <= 0) {
				t.Fatal("process exit identity lost")
			}
			if mode == "large" && (!strings.Contains(failure.Output, "FileNotFoundError") || failure.OutputOmittedBytes == 0) {
				t.Fatal("final cause/truncation lost")
			}
			if mode == "failed" && !strings.Contains(failure.Output, "ModuleNotFoundError") {
				t.Fatal("Python cause lost")
			}
			if mode == "both" && !strings.Contains(failure.Summary(), "reported sentinel") {
				t.Fatal("plugin reported error lost")
			}
			if mode == "pem" && (strings.Contains(failure.Output, "PEMFIXTURE") || !strings.Contains(failure.Output, "ModuleNotFoundError")) {
				t.Fatal("PEM fragment exported or terminal error lost")
			}
		})
	}
}

func syntheticPEMDiagnostic() string {
	// Nonsecret fixture: enough complete body records to evict the opening delimiter.
	return "-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("PEMFIXTUREABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\n", 700) +
		"-----END RSA PRIVATE KEY-----\nModuleNotFoundError: No module named 'stashapi'\n"
}

func TestPluginStderrTailPEMTruncationPrivacy(t *testing.T) {
	for _, chunk := range []int{1, 17, 4096} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			var tail stderrTail
			input := syntheticPEMDiagnostic()
			for len(input) > 0 {
				n := min(len(input), chunk)
				_, _ = io.WriteString(&tail, input[:n])
				input = input[n:]
			}
			cfg := pluginHelperConfig(t, "success")
			task := pluginTask{plugin: &cfg, ctx: context.Background()}
			task.complete(nil, errors.New("exit status 7"), &tail)
			var failure *ExecutionError
			if !errors.As(task.GetResult().Err(), &failure) {
				t.Fatal("missing failure")
			}
			if strings.Contains(failure.Output, "PEMFIXTURE") || strings.Contains(failure.Output, "PRIVATE KEY-----") {
				t.Fatal("truncated PEM credential body exported")
			}
			if !strings.Contains(failure.Output, "ModuleNotFoundError") || failure.OutputOmittedBytes == 0 || len(tail.data) > maxPluginDiagnosticBytes {
				t.Fatal("terminal diagnostic or bounded omission accounting lost")
			}
		})
	}
}

func TestPluginPostHooksCaptureFailureAndContinue(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	marker := filepath.Join(t.TempDir(), "invocations")
	t.Setenv("VEX84_HELPER_MARKER", marker)
	first, next := pluginHelperConfig(t, "failed"), pluginHelperConfig(t, "success")
	first.Hooks = []*HookConfig{{TriggeredBy: []hook.TriggerEnum{hook.SceneUpdatePost}}}
	next.Hooks = []*HookConfig{{TriggeredBy: []hook.TriggerEnum{hook.SceneUpdatePost}}}
	next.id = "FollowingPlugin"
	cfg := pluginTestConfig{root: t.TempDir()}
	calls := 0
	cache := Cache{config: cfg, plugins: []Config{first, next}, sessionStore: session.NewStore(cfg), OnError: func(_ context.Context, err error) {
		calls++
		var failure *ExecutionError
		if !errors.As(err, &failure) || failure.Operation != "hook" || failure.Hook != hook.SceneUpdatePost.String() {
			t.Error("missing hook context")
		}
	}}
	if err := cache.executePostHooks(context.Background(), hook.SceneUpdatePost, common.HookContext{Input: map[string]any{"private": "Private Alice"}}); err != nil {
		t.Fatal("hook continuation changed", err)
	}
	if calls != 1 {
		t.Fatalf("failure capture count %d", calls)
	}
	invocations, err := os.ReadFile(marker)
	if err != nil || string(invocations) != "failed\nsuccess\n" {
		t.Fatalf("following hook did not run: %s %v", invocations, err)
	}
	// A second failing hook must also be observed, not deduplicated by plugin ID.
	cache.plugins[1] = first
	if err := cache.executePostHooks(context.Background(), hook.SceneUpdatePost, common.HookContext{}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("distinct invocation failures lost: %d", calls)
	}
}

func TestPluginPrivateInspectionFailsClosed(t *testing.T) {
	cfg := pluginHelperConfig(t, "success")
	for _, input := range []any{strings.Repeat("x", 128*1024+1), make([]string, 5000)} {
		task := pluginTask{plugin: &cfg, ctx: context.Background(), input: common.PluginInput{Args: common.ArgsMap{"input": input}}}
		message := "Private Alice echoed in plugin error"
		var output stderrTail
		_, _ = io.WriteString(&output, "Private Alice echoed in stderr\n")
		task.complete(&common.PluginOutput{Error: &message}, nil, &output)
		if strings.Contains(task.GetResult().Err().Error(), "Private Alice") {
			t.Fatal("incomplete private inspection leaked text")
		}
	}
}

type privatePluginArgument struct{ scene string }

func (v privatePluginArgument) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"scene": v.scene})
}

func (v privatePluginArgument) Scene() string { return v.scene }

func TestPluginUnexportedInputPrivacyAtExecutionBoundary(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	const private = "SyntheticPrivateSceneIdentity9182"
	for _, kind := range []string{"raw", "js"} {
		t.Run(kind, func(t *testing.T) {
			cfg := pluginHelperConfig(t, "privateJSON")
			if kind == "js" {
				cfg.Interface, cfg.Exec = InterfaceEnumJS, []string{"fixture.js"}
				script := `throw new Error("ModuleNotFoundError: unfamiliar-wheel-sentinel " + input.Args.privateJSON.scene());`
				if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.path), "fixture.js"), []byte(script), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			pt := pluginTask{plugin: &cfg, serverConfig: pluginTestConfig{}, ctx: context.Background(), kind: "task",
				input:   common.PluginInput{Args: common.ArgsMap{"privateJSON": privatePluginArgument{scene: private}}},
				onError: func(context.Context, error) { calls++ }}
			task := pt.createTask()
			if err := task.Start(); err != nil {
				t.Fatal(err)
			}
			task.Wait()
			var failure *ExecutionError
			if !errors.As(task.GetResult().Err(), &failure) || calls != 1 {
				t.Fatal("missing execution failure")
			}
			text := failure.Error()
			if strings.Contains(text, private) {
				t.Fatal("unexported input echoed through supported serialization/JS method")
			}
			for _, retained := range []string{"ModuleNotFoundError", "unfamiliar-wheel-sentinel"} {
				if !strings.Contains(text, retained) {
					t.Fatal("useful diagnostic lost", retained, text)
				}
			}
		})
	}
}

func TestPluginStderrTailBoundedRecords(t *testing.T) {
	var tail stderrTail
	_, _ = io.WriteString(&tail, "password="+strings.Repeat("fixture", 10000))
	_, _ = io.WriteString(&tail, "secret-tail\nModuleNotFoundError: stashapi\n")
	if strings.Contains(string(tail.data), "secret-tail") || !strings.Contains(string(tail.data), "ModuleNotFoundError") || tail.omitted == 0 {
		t.Fatal("torn credential or lost final record")
	}
	var multiline stderrTail
	for i := 0; i < 2000; i++ {
		_, _ = fmt.Fprintf(&multiline, "diagnostic line %d\n", i)
	}
	if len(multiline.data) > maxPluginDiagnosticBytes || !strings.HasSuffix(string(multiline.data), "diagnostic line 1999\n") {
		t.Fatal("tail budget lost")
	}
}

func TestPluginStoppedRawRetainsCancellationIdentity(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	cfg := pluginHelperConfig(t, "waiting")
	ctx, cancel := context.WithCancel(diagnostics.WithState(context.Background()))
	task := &rawPluginTask{pluginTask: pluginTask{plugin: &cfg, serverConfig: pluginTestConfig{}, ctx: ctx}}
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := task.Stop(); err != nil {
		t.Fatal(err)
	}
	task.Wait()
	var exit *exec.ExitError
	if !errors.As(task.GetResult().Err(), &exit) || exit.ExitCode() != -1 {
		t.Fatal("killed process identity lost")
	}
}

func TestPluginRawStopAfterCompletion(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	cfg := pluginHelperConfig(t, "success")
	task := &rawPluginTask{pluginTask: pluginTask{plugin: &cfg, serverConfig: pluginTestConfig{}, ctx: context.Background()}}
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	task.Wait()
	if task.GetResult().Err() != nil {
		t.Fatal(task.GetResult().Err())
	}
	for i := 0; i < 64; i++ {
		if err := task.Stop(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("Stop after reaping must use completed os.Process identity: %v", err)
		}
	}
}

func TestPluginRawConcurrentStopAndWait(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	cfg := pluginHelperConfig(t, "waiting")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := &rawPluginTask{pluginTask: pluginTask{plugin: &cfg, serverConfig: pluginTestConfig{}, ctx: ctx}}
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { task.Wait(); close(done) }()
	cancel()
	var stops sync.WaitGroup
	results := make(chan error, 32)
	for i := 0; i < cap(results); i++ {
		stops.Add(1)
		go func() {
			defer stops.Done()
			results <- task.Stop()
		}()
	}
	stops.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatal("unexpected concurrent Stop result", err)
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Stop left task/readers waiting")
	}
	var exit *exec.ExitError
	if !errors.As(task.GetResult().Err(), &exit) || exit.ExitCode() != -1 {
		t.Fatal("concurrent Stop lost cancellation exit identity")
	}
	if err := task.Stop(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatal("completed concurrent Stop did not retain ProcessDone identity", err)
	}
}

func TestPluginRPCTransportFailureIsCaptured(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	cfg := pluginHelperConfig(t, "rpcFailure")
	cfg.Interface = InterfaceEnumRPC
	calls := 0
	task := &rpcPluginTask{pluginTask: pluginTask{plugin: &cfg, ctx: context.Background(), kind: "task", onError: func(context.Context, error) { calls++ }}}
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	task.Wait()
	if calls != 1 || task.GetResult().Err() == nil {
		t.Fatal("RPC call error lost")
	}
	text := task.GetResult().Err().Error()
	if !strings.Contains(text, "RPC unfamiliar cause") || strings.Contains(text, "synthetic-secret") {
		t.Fatal("RPC cause lost or unsafe")
	}
}

func TestPluginJSFailureSuccessAndStop(t *testing.T) {
	for _, scenario := range []struct {
		name, script string
		failed       bool
	}{
		{"throw", `throw new Error("unfamiliar JS cause " + input.Args.private);`, true},
		{"reported", `({Error: "reported JS cause " + input.Args.private});`, true},
		{"success", `({Output: "ok"});`, false},
		{"stopped", `while (true) {}`, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cfg := pluginHelperConfig(t, "success")
			cfg.Interface, cfg.Exec = InterfaceEnumJS, []string{"fixture.js"}
			if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.path), "fixture.js"), []byte(scenario.script), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			task := &jsPluginTask{pluginTask: pluginTask{plugin: &cfg, ctx: context.Background(), kind: "task", input: common.PluginInput{Args: common.ArgsMap{"private": "Private Alice"}}, onError: func(context.Context, error) { calls++ }}}
			if err := task.Start(); err != nil {
				t.Fatal(err)
			}
			if scenario.name == "stopped" {
				if err := task.Stop(); err != nil {
					t.Fatal(err)
				}
			}
			task.Wait()
			err := task.GetResult().Err()
			if scenario.failed {
				if calls != 1 || err == nil {
					t.Fatal("JS error not captured")
				}
				if strings.Contains(err.Error(), "Private Alice") {
					t.Fatal("JS echoed input leaked")
				}
			} else if calls != 0 || err != nil {
				t.Fatal("JS success/stop emitted error")
			}
		})
	}
}

func TestPluginHookStartFailurePreservesAbort(t *testing.T) {
	first := pluginHelperConfig(t, "success")
	first.Exec = []string{filepath.Join(t.TempDir(), "missing-plugin-executable")}
	first.Hooks = []*HookConfig{{TriggeredBy: []hook.TriggerEnum{hook.SceneUpdatePost}}}
	cfg := pluginTestConfig{root: t.TempDir()}
	calls := 0
	cache := Cache{config: cfg, plugins: []Config{first}, sessionStore: session.NewStore(cfg), OnError: func(context.Context, error) { calls++ }}
	err := cache.executePostHooks(context.Background(), hook.SceneUpdatePost, common.HookContext{})
	var failure *ExecutionError
	if calls != 1 || !errors.As(err, &failure) {
		t.Fatal("hook start failure lost or swallowed", err)
	}
}

func TestPluginObserverPanicDoesNotHideFailure(t *testing.T) {
	cfg := pluginHelperConfig(t, "success")
	task := pluginTask{plugin: &cfg, ctx: context.Background(), onError: func(context.Context, error) { panic("synthetic observer panic") }}
	task.complete(nil, errors.New("real failure"), nil)
	if task.GetResult().Err() == nil {
		t.Fatal("observer panic hid plugin failure")
	}
}
