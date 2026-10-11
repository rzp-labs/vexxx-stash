package generate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestGenerationRunnersKeepIndependentExitDuringContextDrain(t *testing.T) {
	for _, output := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("output=%t/deadline=%t", output, deadline), func(t *testing.T) {
				dir := t.TempDir()
				ready, release := filepath.Join(dir, "exited"), filepath.Join(dir, "release")
				// The inherited pipe stays open until the test releases it. The marker
				// appears only after the command itself has exited and been reaped.
				script := fmt.Sprintf("parent=$$\n( while kill -0 \"$parent\" 2>/dev/null; do sleep 0.01; done; touch %q; while [ ! -e %q ]; do sleep 0.01; done ) &\nexit 23\n", ready, release)
				g := metadataTestGenerator(t, script, generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
				lock := g.LockManager.ReadLock(context.Background(), "synthetic")
				defer lock.Cancel()
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				}
				defer cancel()
				defer os.WriteFile(release, nil, 0600)
				done := make(chan error, 1)
				go func() {
					if output {
						_, err := g.generateOutputWithContext(ctx, lock, []string{"-hwaccel", "vaapi"})
						done <- err
					} else {
						done <- g.generateWithContext(ctx, lock, []string{"-hwaccel", "vaapi"})
					}
				}()
				until := time.Now().Add(2 * time.Second)
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					if time.Now().After(until) {
						t.Fatal("command did not exit before the drain marker")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if ctx.Err() != nil {
					t.Fatal("context ended before independent exit was established")
				}
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				if err := os.WriteFile(release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 23 {
						t.Fatal("independent exit was lost", err)
					}
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("later context state was attributed to independent exit", err)
					}
					if got := diagnostics.Split(err); got.Count != 1 {
						t.Fatal("independent failure was omitted", got)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("runner did not finish after pipe release")
				}
			})
		}
	}
}
