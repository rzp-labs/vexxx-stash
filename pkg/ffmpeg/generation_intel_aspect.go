package ffmpeg

import (
	"fmt"
	"math/big"
	"strings"
)

// intelSampleAspectRatio keeps absence distinct from a positive pixel ratio.
// Missing/zero SAR uses FFmpeg's canonical unspecified-pixel behavior; malformed
// or negative ratios cannot authorize an invented display interpretation.
func intelSampleAspectRatio(value string) (*big.Rat, error) {
	switch value {
	case "", "N/A", "0:1", "0/1":
		return nil, nil
	}
	ratio, ok := new(big.Rat).SetString(strings.ReplaceAll(value, ":", "/"))
	if !ok || ratio.Sign() <= 0 {
		return nil, fmt.Errorf("invalid input sample aspect ratio %q", value)
	}
	return ratio, nil
}

func (s IntelSource) ValidateSampleAspectRatio() error {
	_, err := intelSampleAspectRatio(s.SampleAspectRatio)
	return err
}

// IntelOrientedSampleAspectRatio preserves the pixel ratio after canonical
// autorotation. Axis-swapping transforms invert SAR; reflections do not.
func IntelOrientedSampleAspectRatio(source IntelSource) string {
	sar, err := intelSampleAspectRatio(source.SampleAspectRatio)
	if err != nil || sar == nil {
		return source.SampleAspectRatio
	}
	angle, err := intelRotationDegrees(source)
	if err == nil && (angle == 90 || angle == 270) {
		sar.Inv(sar)
	}
	return sar.Num().String() + ":" + sar.Denom().String()
}

// HasSquareOrUnspecifiedSampleAspectRatio remains available to legacy callers
// which need that narrower predicate. Preview planning supports non-square SAR.
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
