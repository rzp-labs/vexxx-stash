package manager

import (
	"context"
	"fmt"
	"math"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/logger"
)

// intelSpriteSheet bypasses all CPU tile export, montage and JPEG encoding when
// a GPU backend is selected. Unsupported projection/seek paths fail explicitly.
func (g *SpriteGenerator) intelSpriteSheet(ctx context.Context, req spriteRequest) (bool, error) {
	if g.g.IntelSprites == nil || !g.g.IntelSprites.Enabled() {
		return false, nil
	}
	if req.count <= 0 {
		return true, fmt.Errorf("sprite tile count must be positive")
	}
	if req.vrMode != "" {
		reason := "VR projection has no GPU sprite implementation"
		d := ffmpeg.IntelGenerationDiagnostic{Selected: g.g.IntelSprites.Backend, Actual: "none", Stage: "eligibility", Reason: reason}
		logger.Infof("[generator] sprite selected=%s actual=%s stage=%s reason=%s", d.Selected, d.Actual, d.Stage, d.Reason)
		if g.g.IntelDiagnostic != nil {
			g.g.IntelDiagnostic(d)
		}
		return true, fmt.Errorf("GPU sprite unsupported: %s", reason)
	}
	if req.slowSeek {
		frames := make([]int, req.count)
		step := float64(req.frameCount-1) / float64(req.count)
		for i := range frames {
			frame := math.Round(float64(i) * step)
			if frame >= math.MaxInt || frame <= math.MinInt {
				return true, fmt.Errorf("invalid GPU sprite frame number conversion")
			}
			frames[i] = int(frame)
		}
		d, err := g.g.IntelSpriteSheetFrames(ctx, req.path, frames, g.Columns, g.Rows, g.ImageOutputPath)
		logger.Infof("[generator] sprite selected=%s actual=%s stage=%s reason=%s", d.Selected, d.Actual, d.Stage, d.Reason)
		return true, err
	}
	duration := req.streamDuration
	if req.duration > 0 {
		duration = req.duration
	}
	step := duration / float64(req.count)
	times := make([]float64, req.count)
	for i := range times {
		times[i] = req.startOffset + float64(i)*step
	}
	d, err := g.g.IntelSpriteSheet(ctx, req.path, times, g.Columns, g.Rows, g.ImageOutputPath)
	logger.Infof("[generator] sprite selected=%s actual=%s stage=%s reason=%s", d.Selected, d.Actual, d.Stage, d.Reason)
	return true, err
}
