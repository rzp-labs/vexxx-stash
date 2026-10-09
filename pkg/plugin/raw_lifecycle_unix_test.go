//go:build linux || darwin

package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPluginRawStopDoesNotSignalReusedNumericID(t *testing.T) {
	t.Setenv("VEX84_PLUGIN_HELPER", "1")
	cfg := pluginHelperConfig(t, "success")
	task := &rawPluginTask{pluginTask: pluginTask{plugin: &cfg, serverConfig: pluginTestConfig{}, ctx: context.Background()}}
	if err := task.Start(); err != nil {
		t.Fatal(err)
	}
	task.Wait()

	// Simulate reuse with an explicitly owned, still-live sentinel group.
	// No actual OS PID reuse or unrelated process is involved.
	sentinel := exec.Command("/bin/sleep", "5")
	sentinel.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- sentinel.Wait() }()
	t.Cleanup(func() {
		_ = sentinel.Process.Kill()
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("owned sentinel did not stop/reap during cleanup")
		}
	})
	task.cmd.Process.Pid = sentinel.Process.Pid
	if err := task.Stop(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatal("Stop signaled a numeric group after the task's os.Process was reaped", err)
	}
	select {
	case err := <-exited:
		// Put it back for cleanup, which also verifies that the process was reaped.
		exited <- err
		t.Fatal("completed task Stop terminated the owned reuse sentinel", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPluginRawInheritedStderrDoesNotHoldCompletion(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			cfg := Config{id: "InheritedStderrFixture", Name: "Inherited stderr fixture", Interface: InterfaceEnumRaw,
				Exec: []string{"/bin/sh", "-c", `sleep 3 >&2 & printf '%s\n' "$!" > "$1"; printf 'terminal fixture diagnostic\n' >&2; printf '{"output":"ok"}'; exit "$2"`, "fixture", pidFile, strconv.Itoa(exitCode)}}
			calls := 0
			task := &rawPluginTask{pluginTask: pluginTask{plugin: &cfg, serverConfig: pluginTestConfig{}, ctx: context.Background(),
				onError: func(context.Context, error) { calls++ }}}
			started := time.Now()
			if err := task.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { task.Wait(); close(done) }()
			// Always clean up the short fixture, including when reproducing the old hang.
			defer func() {
				// The descendant has a finite lifetime; await natural exit without
				// sending a signal to a PID we do not own through os.Process.
				if remaining := time.Until(started.Add(3500 * time.Millisecond)); remaining > 0 {
					time.Sleep(remaining)
				}
				data, _ := os.ReadFile(pidFile)
				status, err := exec.Command("ps", "-o", "stat=", "-p", strings.TrimSpace(string(data))).Output()
				if err == nil && strings.TrimSpace(string(status)) != "" && !strings.HasPrefix(strings.TrimSpace(string(status)), "Z") {
					t.Error("finite background fixture did not exit")
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("fixture task/goroutine did not finish after cleanup")
				}
			}()
			select {
			case <-done:
			case <-time.After(1500 * time.Millisecond):
				t.Fatal("primary process exited but inherited stderr held task completion")
			}
			if time.Since(started) > 1500*time.Millisecond {
				t.Fatal("bounded completion exceeded deadline")
			}
			result := task.GetResult()
			if result.Output != "ok" {
				t.Fatal("primary stdout lost", result)
			}
			if exitCode == 0 {
				if result.Err() != nil || calls != 0 {
					t.Fatal("inherited stderr became a false plugin failure")
				}
			} else {
				var failure *ExecutionError
				var exit *exec.ExitError
				if calls != 1 || !errors.As(result.Err(), &failure) || !errors.As(failure, &exit) || exit.ExitCode() != exitCode || !strings.Contains(failure.Output, "terminal fixture diagnostic") {
					t.Fatal("exit identity, final stderr or single capture lost", result)
				}
			}
		})
	}
}
