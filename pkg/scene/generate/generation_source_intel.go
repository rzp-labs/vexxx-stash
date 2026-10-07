package generate

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

// IntelSourceMetadata resolves the native GPU-selected video before callers
// plan sampling or segment targets. CPU header and GPU frame admission remain
// separate leaf operations, using the same source-lock command ownership as
// rendering and scene deletion.
func (g Generator) IntelSourceMetadata(ctx context.Context, input string, config ffmpeg.IntelGenerationConfig) (ffmpeg.IntelSource, error) {
	if g.LockManager == nil {
		return ffmpeg.IntelSource{}, fmt.Errorf("source lock manager unavailable for GPU metadata")
	}
	g = g.WithIntelGenerationBudget()
	done := make(chan struct{})
	lockCtx := g.LockManager.ReadLockWithCompletion(ctx, input, done)
	defer lockCtx.Cancel()
	defer close(done)
	source, err := g.intelSourceMetadata(lockCtx, lockCtx, input, config, false)
	if ctx.Err() != nil {
		return source, ctx.Err()
	}
	if err != nil {
		return source, ffmpeg.WithIntelGenerationDiagnostic(err,
			ffmpeg.IntelGenerationDiagnostic{Selected: config.Backend, Actual: "none", Stage: "metadata"}, source)
	}
	return source, err
}
