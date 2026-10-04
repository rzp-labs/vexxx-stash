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
	if plan.Config.Backend == "qsv" {
		// A B580 single-frame seek returned pixels three frames earlier than
		// their PTS at the default decoder depth. Use depth one for sprites.
		args = append(args, "-async_depth", "1")
	}
	args = args.Seek(seconds).Input(input)
	args = append(args, "-map", fmt.Sprintf("0:%d", plan.Source.StreamIndex), "-an")
	args = args.VideoFrames(1)
	args = append(args, "-vf", plan.Filter+",format=bgr24")
	args = args.AppendArgs(ScreenshotOutputTypeBMP)
	return args.Output("-")
}
