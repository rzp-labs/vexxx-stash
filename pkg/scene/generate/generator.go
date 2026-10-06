// Package generate provides functions to generate media assets from scenes.
package generate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

const (
	mp4Pattern  = "*.mp4"
	webpPattern = "*.webp"
	jpgPattern  = "*.jpg"
	txtPattern  = "*.txt"
	vttPattern  = "*.vtt"
)

type Paths interface {
	TempFile(pattern string) (*os.File, error)
}

type MarkerPaths interface {
	Paths

	GetVideoPreviewPath(checksum string, seconds int) string
	GetWebpPreviewPath(checksum string, seconds int) string
	GetScreenshotPath(checksum string, seconds int) string
}

type ScenePaths interface {
	Paths

	GetVideoPreviewPath(checksum string) string
	GetWebpPreviewPath(checksum string) string

	GetSpriteImageFilePath(checksum string) string
	GetSpriteVttFilePath(checksum string) string

	GetTranscodePath(checksum string) string
}

type FFMpegConfig interface {
	GetTranscodeInputArgs() []string
	GetTranscodeOutputArgs() []string
}

type Generator struct {
	Encoder             *ffmpeg.FFMpeg
	Probe               *ffmpeg.FFProbe
	IntelPreviews       *ffmpeg.IntelGenerationConfig
	previewIntelPlan    *ffmpeg.IntelGenerationPlan
	capacityWorkload    *generationbudget.Workload
	capacityObservation *capacityObservation
	IntelMarker         *ffmpeg.IntelGenerationConfig
	IntelSprites        *ffmpeg.IntelGenerationConfig
	IntelDiagnostic     func(ffmpeg.IntelGenerationDiagnostic)
	// Tests can substitute capability/device work; nil retains runtime validation.
	intelSpriteWork func(context.Context, ffmpeg.IntelGenerationPlan, func(context.Context) error, func(context.Context) error, ffmpeg.IntelGenerationRunner) (ffmpeg.IntelGenerationDiagnostic, error)
	// Budget optionally overrides the application's shared generation budget.
	Budget       *generationbudget.Budget
	FFMpegConfig FFMpegConfig
	LockManager  *fsutil.ReadLockManager
	MarkerPaths  MarkerPaths
	ScenePaths   ScenePaths
	Overwrite    bool
}

type generationBudgetConfig interface {
	GetGenerationBudget() *generationbudget.Budget
}

func (g Generator) generationBudget() *generationbudget.Budget {
	if g.Budget != nil {
		return g.Budget
	}
	if config, ok := g.FFMpegConfig.(generationBudgetConfig); ok {
		return config.GetGenerationBudget()
	}
	return nil
}

// WithIntelGenerationBudget scopes mandatory Intel admission to the selected
// workload and its fallback. The original generator keeps ordinary CPU settings.
// Explicit overrides and the application's optional shared budget take priority.
func (g Generator) WithIntelGenerationBudget() Generator {
	if g.generationBudget() != nil {
		return g
	}
	if config, ok := g.FFMpegConfig.(interface {
		GetIntelGenerationBudget() *generationbudget.Budget
	}); ok {
		g.Budget = config.GetIntelGenerationBudget()
	}
	return g
}

// acquireGeneration reserves only a subprocess stage, never a parent scene
// task. Hardware attempts release their permits before a software fallback.
func (g Generator) acquireGeneration(ctx context.Context, args []string) ([]string, func(), error) {
	return g.acquireGenerationN(ctx, args, 1)
}

func (g Generator) acquireGenerationN(ctx context.Context, args []string, slots int) ([]string, func(), error) {
	budget := g.generationBudget()
	release, err := budget.AcquireN(ctx, generationbudget.ClassifyFFMpeg(args), slots)
	if err != nil {
		return nil, nil, fmt.Errorf("waiting for generation budget: %w", err)
	}
	return budget.FFMpegArgs(args), release, nil
}

type generateFn func(lockCtx *fsutil.LockContext, tmpFn string) error

func (g Generator) tempFile(p Paths, pattern string) (*os.File, error) {
	tmpFile, err := p.TempFile(pattern) // tmp output in case the process ends abruptly
	if err != nil {
		return nil, fmt.Errorf("creating temporary file: %w", err)
	}
	_ = tmpFile.Close()
	return tmpFile, err
}

// generateFile performs a generate operation by generating a temporary file using p and pattern, then
// moving it to output on success.
func (g Generator) generateFile(lockCtx *fsutil.LockContext, p Paths, pattern string, output string, generateFn generateFn) error {
	tmpFile, err := g.tempFile(p, pattern) // tmp output in case the process ends abruptly
	if err != nil {
		return err
	}

	tmpFn := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpFn)
	}()

	if err := generateFn(lockCtx, tmpFn); err != nil {
		return err
	}

	// check if generated empty file
	stat, err := os.Stat(tmpFn)
	if err != nil {
		return fmt.Errorf("error getting file stat: %w", err)
	}

	if stat.Size() == 0 {
		return fmt.Errorf("ffmpeg command produced no output")
	}

	if err := fsutil.SafeMove(tmpFn, output); err != nil {
		return fmt.Errorf("moving %s to %s failed: %w", tmpFn, output, err)
	}

	return nil
}

// generateBytes performs a generate operation by generating a temporary file using p and pattern, returns the contents, then deletes it.
func (g Generator) generateBytes(lockCtx *fsutil.LockContext, p Paths, pattern string, generateFn generateFn) ([]byte, error) {
	tmpFile, err := g.tempFile(p, pattern) // tmp output in case the process ends abruptly
	if err != nil {
		return nil, err
	}

	tmpFn := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpFn)
	}()

	if err := generateFn(lockCtx, tmpFn); err != nil {
		return nil, err
	}

	defer os.Remove(tmpFn)
	return os.ReadFile(tmpFn)
}

// generate runs ffmpeg with the given args and waits for it to finish.
// Returns an error if the command fails. If the command fails, the return
// value will be of type *exec.ExitError.
func (g Generator) generate(lockCtx *fsutil.LockContext, args []string) error {
	return g.generateWithContext(lockCtx, lockCtx, args)
}

// The execution context can carry a probe timeout while command ownership stays
// on the registered source lock used by scene deletion.
func (g Generator) generateWithContext(ctx context.Context, lockCtx *fsutil.LockContext, args []string) error {
	return g.generateWithContextN(ctx, lockCtx, args, 1)
}

// Independent hardware inputs share command ownership and cancellation, while
// each decoder consumes a slot in the shared generation budget.
func (g Generator) generateWithContextN(ctx context.Context, lockCtx *fsutil.LockContext, args []string, slots int) error {
	if g.capacityWorkload != nil && g.generationBudget() != nil && generationbudget.ClassifyFFMpeg(args) == generationbudget.GPU {
		return g.generateCapacityWork(ctx, lockCtx, args, slots)
	}
	args, release, err := g.acquireGenerationN(ctx, args, slots)
	if err != nil {
		return err
	}
	defer release()
	return g.generateAdmittedWithContext(ctx, lockCtx, args, slots)
}

// The caller owns admission for this command and releases it only after the
// registered child drains. Adaptive sprite admission uses this same execution
// path after choosing its decoder count under the shared budget lock.
func (g Generator) generateAdmittedWithContext(ctx context.Context, lockCtx *fsutil.LockContext, args []string, admitted int) error {
	execCtx, cancel := ffmpeg.IntelProbeExecutionContext(ctx)
	defer cancel()
	cmd := g.Encoder.Command(execCtx, args)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	done := make(chan struct{})
	defer close(done)
	lockCtx.AttachCommandWithCompletion(cmd, done)

	if err := cmd.Start(); err != nil {
		return g.commandError(args, admitted, false, fmt.Errorf("error starting command: %w", err))
	}

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.Bytes()
			err = exitErr
		}
		return g.commandError(args, admitted, true, fmt.Errorf("error running ffmpeg command <%s>: %w", strings.Join(args, " "), err))
	}

	return nil
}

// GenerateOutput runs ffmpeg with the given args and returns it standard output.
func (g Generator) generateOutput(lockCtx *fsutil.LockContext, args []string) ([]byte, error) {
	return g.generateOutputWithContext(lockCtx, lockCtx, args)
}

// A bounded probe starts its deadline after admission while source deletion
// continues to own and drain the subprocess through the registered lock.
func (g Generator) generateOutputWithContext(ctx context.Context, lockCtx *fsutil.LockContext, args []string) ([]byte, error) {
	args, release, err := g.acquireGeneration(ctx, args)
	if err != nil {
		return nil, err
	}
	defer release()
	execCtx, cancel := ffmpeg.IntelProbeExecutionContext(ctx)
	defer cancel()
	cmd := g.Encoder.Command(execCtx, args)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	done := make(chan struct{})
	defer close(done)
	lockCtx.AttachCommandWithCompletion(cmd, done)

	if err := cmd.Start(); err != nil {
		return nil, g.commandError(args, 1, false, fmt.Errorf("error starting command: %w", err))
	}

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.Bytes()
			err = exitErr
		}
		return nil, g.commandError(args, 1, true, fmt.Errorf("error running ffmpeg command <%s>: %w", strings.Join(args, " "), err))
	}

	if stdout.Len() == 0 {
		return nil, g.commandError(args, 1, true, fmt.Errorf("ffmpeg command produced no output: <%s>", strings.Join(args, " ")))
	}

	return stdout.Bytes(), nil
}

// Admission metadata follows the returned error through existing task wrappers;
// it never changes command construction, permit ownership or retry behavior.
func (g Generator) commandError(args []string, admitted int, started bool, err error) error {
	limits := generationbudget.Settings{}
	if budget := g.generationBudget(); budget != nil {
		limits = budget.Settings()
	}
	private := []string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-i" {
			private = append(private, args[i+1])
		}
	}
	if len(args) > 0 {
		private = append(private, args[len(args)-1])
	}
	return &ffmpeg.GenerationCommandError{Err: err, Admitted: admitted, Started: started, Limits: limits, PrivateValues: private}
}
