package generate

import (
	"github.com/stashapp/stash/pkg/diagnostics"
	"strconv"
	"strings"
)

// PreviewReplayRequest copies producer options before any command runs. This
// keeps requested exclusions separate from actual clamped segment positions.
func PreviewReplayRequest(options PreviewOptions, duration *float64) diagnostics.PreviewRequest {
	r := diagnostics.PreviewRequest{Segments: options.Segments, SegmentSeconds: &options.SegmentDuration, DurationSeconds: duration,
		LimitStart: options.LimitStart, LimitEnd: options.LimitEnd, Audio: options.Audio}
	parse := func(value string) (*float64, string) {
		unit := "seconds"
		if strings.HasSuffix(value, "%") {
			value = strings.TrimSuffix(value, "%")
			unit = "percent"
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, ""
		}
		return &n, unit
	}
	r.ExcludeStart, r.ExcludeStartUnit = parse(options.ExcludeStart)
	r.ExcludeEnd, r.ExcludeEndUnit = parse(options.ExcludeEnd)
	return r
}
