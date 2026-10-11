package generate

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestGenerationRunnersDrainLargeStderrAndReleasePermits(t *testing.T) {
	for _, output := range []bool{false, true} {
		g := metadataTestGenerator(t, "printf 'initial unsupported decode profile\\n' >&2\nhead -c 12582912 /dev/zero >&2\nprintf '\\nVAAPI encode failed: 24; Input/output error\\n' >&2\nexit 23\n", generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lock := g.LockManager.ReadLock(ctx, "synthetic")
		var err error
		if output {
			_, err = g.generateOutputWithContext(lock, lock, []string{"-hwaccel", "vaapi"})
		} else {
			err = g.generateWithContext(lock, lock, []string{"-hwaccel", "vaapi"})
		}
		var exit *exec.ExitError
		var command *ffmpeg.GenerationCommandError
		if !errors.As(err, &exit) || exit.ExitCode() != 23 || !errors.As(err, &command) {
			t.Fatal("command cause lost", err)
		}
		if len(exit.Stderr) > 8300 || command.Stderr.Total < 12<<20 || command.Stderr.Retained > 8192 || command.Stderr.Omitted != command.Stderr.Total-int64(command.Stderr.Retained) {
			t.Fatal("unbounded or inaccurate capture", len(exit.Stderr), command.Stderr)
		}
		for _, text := range []string{"initial unsupported decode profile", "VAAPI encode failed: 24; Input/output error", "stderr bytes omitted"} {
			if !strings.Contains(string(exit.Stderr), text) {
				t.Fatal("lost diagnostic", text)
			}
		}
		release, acquireErr := g.Budget.Acquire(ctx, generationbudget.GPU)
		if acquireErr != nil {
			t.Fatal("permit leaked", acquireErr)
		}
		release()
		lock.Cancel()
		cancel()
	}
}
