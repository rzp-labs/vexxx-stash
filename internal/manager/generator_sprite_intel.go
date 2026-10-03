package manager

import (
	"context"
	"fmt"
	"image"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/logger"
)

// intelSpriteTiles returns handled=true only when Intel sprites were explicitly
// selected. Declined/failed hardware uses one canonical software fallback;
// unrelated Native Generation settings cannot override that rollback path.
func (g *SpriteGenerator) intelSpriteTiles(ctx context.Context, req spriteRequest) ([]image.Image, bool, error) {
	if g.g.IntelSprites == nil || !g.g.IntelSprites.Enabled() {
		return nil, false, nil
	}
	if req.count <= 0 {
		return nil, true, fmt.Errorf("sprite tile count must be positive")
	}
	if req.slowSeek || req.vrMode != "" {
		reason := "frame-based seeking"
		if req.vrMode != "" {
			reason = "VR projection"
		}
		logger.Infof("[generator] sprite selected=%s actual=software stage=eligibility reason=%s", g.g.IntelSprites.Backend, reason)
		if g.g.IntelDiagnostic != nil {
			g.g.IntelDiagnostic(ffmpeg.IntelGenerationDiagnostic{Selected: g.g.IntelSprites.Backend, Actual: "software", Stage: "eligibility", Reason: reason})
		}
		images, err := (ffmpegSprites{gen: g.g}).tiles(ctx, req)
		return images, true, err
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
	images, d, err := g.g.IntelSpriteTiles(ctx, req.path, times)
	logger.Infof("[generator] sprite selected=%s actual=%s stage=%s reason=%s", d.Selected, d.Actual, d.Stage, d.Reason)
	return images, true, err
}
