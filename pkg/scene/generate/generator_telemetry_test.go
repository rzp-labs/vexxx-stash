package generate

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestCommandErrorTracksSuccessfulStartAndReleasesAdmission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command fixtures")
	}
	for _, tc := range []struct {
		name, path      string
		output, started bool
	}{
		{"missing command", filepath.Join(t.TempDir(), "missing-ffmpeg"), false, false},
		{"failed command", "/usr/bin/false", false, true},
		{"missing output command", filepath.Join(t.TempDir(), "missing-ffmpeg"), true, false},
		{"failed output command", "/usr/bin/false", true, true},
		{"empty output after successful command", "/usr/bin/true", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 1})
			g := Generator{Budget: budget, Encoder: ffmpeg.NewEncoder(tc.path)}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			lock := fsutil.NewReadLockManager().ReadLock(ctx, "synthetic.mp4")
			defer lock.Cancel()
			var err error
			if tc.output {
				_, err = g.generateOutput(lock, []string{"-i", "synthetic.mp4", "output.mp4"})
			} else {
				err = g.generate(lock, []string{"-i", "synthetic.mp4", "output.mp4"})
			}
			var command *ffmpeg.GenerationCommandError
			if !errors.As(err, &command) || command.Started != tc.started || command.Admitted != 1 {
				t.Fatalf("command execution facts incorrect: %+v, %v", command, err)
			}
			release, err := budget.Acquire(ctx, generationbudget.CPU)
			if err != nil {
				t.Fatal("execution failure leaked admission", err)
			}
			release()
		})
	}
}
