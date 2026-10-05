package ffmpeg

import (
	"math/big"
	"strings"
)

// HasSquareOrUnspecifiedSampleAspectRatio permits absent SAR only for callers
// retaining canonical CPU scaling. It does not rewrite decoded frame geometry:
// hwdownload/hwupload preserve frame properties and scale keeps the incoming SAR.
// A declared DAR inconsistent with square pixels still requires software fallback.
func (s IntelSource) HasSquareOrUnspecifiedSampleAspectRatio() bool {
	switch s.SampleAspectRatio {
	case "1:1", "1/1", "1":
		return true
	case "", "N/A", "0:1", "0/1":
	default:
		return false
	}
	switch s.DisplayAspectRatio {
	case "", "N/A", "0:1", "0/1":
		return true
	}
	dar, ok := new(big.Rat).SetString(strings.ReplaceAll(s.DisplayAspectRatio, ":", "/"))
	return ok && s.Width > 0 && s.Height > 0 && dar.Cmp(big.NewRat(int64(s.Width), int64(s.Height))) == 0
}
