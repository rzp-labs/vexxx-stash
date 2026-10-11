package ffmpeg

import (
	"crypto/sha256"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/stashapp/stash/pkg/diagnostics"
)

// PreviewReplayCommand records the same typed start/geometry inputs used by
// the preview builder. It does not reconstruct values from a private argv.
func PreviewReplayCommand(plan IntelGenerationPlan, start float64, width int, projection string) diagnostics.PreviewCommand {
	if !(start > 0) {
		start = 0
	} // no -ss means the beginning, not a negative seek
	w, h := IntelDisplayDimensions(plan.Source)
	var height *int
	if w > 0 && h > 0 {
		value := max(2, int(math.Round(float64(h)*float64(width)/float64(w)/2))*2)
		height = &value
	}
	return diagnostics.PreviewCommand{SeekSeconds: &start, PlanID: "intel-scene-preview-v1", PlanFingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(plan.Filter))), FilterConfigured: true,
		TargetWidth: &width, TargetHeight: height, Projection: projection, RuntimeFingerprint: plan.RuntimeFingerprint,
		InputPixels: PreviewSourcePixels(plan.InputSource), ProcessedPixels: PreviewSourcePixels(plan.Source)}
}

func PreviewSourcePixels(source IntelSource) *diagnostics.PreviewPixels {
	if source.Width <= 0 || source.Height <= 0 {
		return nil
	}
	p := &diagnostics.PreviewPixels{Width: source.Width, Height: source.Height, Rotation: source.Rotation, StreamIndex: source.StreamIndex, BitDepth: source.BitDepth, DisplayMatrix: source.DisplayMatrix,
		IsRGB: source.IsRGB, PixelFormat: source.PixelFormat,
		ColorSpace: source.ColorSpace, ColorPrimaries: source.ColorPrimaries, ColorTransfer: source.ColorTransfer, ColorRange: source.ColorRange}
	if sar, ok := new(big.Rat).SetString(strings.ReplaceAll(source.SampleAspectRatio, ":", "/")); ok && sar.Num().IsInt64() && sar.Denom().IsInt64() {
		n, d := sar.Num().Int64(), sar.Denom().Int64()
		p.SARNum, p.SARDen = &n, &d
	}
	return p
}
