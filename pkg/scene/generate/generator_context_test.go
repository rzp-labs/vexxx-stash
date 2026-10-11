package generate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestGenerationRunnersRetainExecutionContextAndProcessCause(t *testing.T) {
	for _, output := range []bool{false, true} {
		for _, mode := range []string{"deadline", "cancel", "external kill"} {
			t.Run(fmt.Sprintf("%t/%s", output, mode), func(t *testing.T) {
				script := "exec sleep 3\n"
				if mode == "external kill" {
					script = "kill -KILL $$\n"
				}
				g := metadataTestGenerator(t, script, generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				lock := g.LockManager.ReadLock(ctx, "synthetic")
				defer lock.Cancel()
				var execCtx context.Context = lock
				if mode == "deadline" {
					execCtx = ffmpeg.WithIntelProbeTimeout(lock, 40*time.Millisecond)
				}
				if mode == "cancel" {
					timer := time.AfterFunc(40*time.Millisecond, cancel)
					defer timer.Stop()
				}
				var err error
				if output {
					_, err = g.generateOutputWithContext(execCtx, lock, []string{"-hwaccel", "vaapi"})
				} else {
					err = g.generateWithContext(execCtx, lock, []string{"-hwaccel", "vaapi"})
				}
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !strings.Contains(err.Error(), "signal: killed") {
					t.Fatal("original process error lost", err)
				}
				if errors.Is(err, context.DeadlineExceeded) != (mode == "deadline") || errors.Is(err, context.Canceled) != (mode == "cancel") {
					t.Fatal("context cause misattributed", mode, err)
				}
				if mode != "cancel" && ctx.Err() != nil {
					t.Fatal("parent unexpectedly ended")
				}
				wantFailures := 1
				if mode == "cancel" {
					wantFailures = 0
				}
				if got := diagnostics.Split(err); got.Count != wantFailures {
					t.Fatal("one command became duplicate failures", got)
				}
				fresh, freshCancel := context.WithTimeout(context.Background(), time.Second)
				defer freshCancel()
				release, acquireErr := g.Budget.Acquire(fresh, generationbudget.GPU)
				if acquireErr != nil {
					t.Fatal("permit leaked", acquireErr)
				}
				release()
			})
		}
	}
}
