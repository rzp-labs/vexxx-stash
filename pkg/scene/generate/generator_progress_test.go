package generate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestGenerationRunnersRetainCauseAfterCarriageReturnProgress(t *testing.T) {
	const progress = "frame=   10 fps=0.0 q=0.0 size=       0kB time=00:00:00.40 bitrate=   0.0kbits/s speed=0.01x\r"
	const cause = "Error while opening encoder\n"
	for _, output := range []bool{false, true} {
		t.Run(fmt.Sprint(output), func(t *testing.T) {
			script := fmt.Sprintf("printf '%%s' '%s' >&2\nexit 251\n", strings.Repeat(progress, 100)+cause)
			g := metadataTestGenerator(t, script, generationbudget.Settings{MaxProcesses: 1, MaxGPUProcesses: 1})
			lock := g.LockManager.ReadLock(context.Background(), "synthetic")
			defer lock.Cancel()
			var err error
			if output {
				_, err = g.generateOutputWithContext(lock, lock, []string{"-hwaccel", "vaapi"})
			} else {
				err = g.generateWithContext(lock, lock, []string{"-hwaccel", "vaapi"})
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 251 || !strings.Contains(string(exit.Stderr), cause) {
				t.Fatal("process error lost terminal encoder cause", err)
			}
			if text := ffmpeg.GenerationStderrText(exit.Stderr, 2048); !strings.Contains(text, strings.TrimSpace(cause)) {
				t.Fatal("bounded exit reason lost terminal cause", text)
			}
		})
	}
}
