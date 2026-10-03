package transcoder

import (
	"fmt"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

// IntelSpriteScreenshot preserves ScreenshotTime's independent input seek.
// Scaling happens before downloading the reduced frame; BMP conversion and
// montage composition remain on the CPU. The plan must have download enabled.
func IntelSpriteScreenshot(input string, seconds float64, plan ffmpeg.IntelGenerationPlan) ffmpeg.Args {
	args := ffmpeg.Args{"-v", "error", "-y", "-nostdin"}
	args = append(args, plan.InputArgs...)
	args = args.Seek(seconds).Input(input)
	args = append(args, "-map", fmt.Sprintf("0:%d", plan.Source.StreamIndex), "-an")
	args = args.VideoFrames(1)
	args = append(args, "-vf", plan.Filter+",format=bgr24")
	args = args.AppendArgs(ScreenshotOutputTypeBMP)
	return args.Output("-")
}
