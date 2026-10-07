package ffmpeg

import (
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"syscall"

	"github.com/stashapp/stash/pkg/generationbudget"
)

// GenerationWorkload describes resource costs of an already capability-checked
// plan. It does not decide codec support; RunIntelGenerationWork's actual source
// decode/filter/output probes remain the shared gate for manual and Auto work.
func (p IntelGenerationPlan) GenerationWorkload(operation string) generationbudget.Workload {
	identity := p.RuntimeFingerprint
	source := p.InputSource
	key := fmt.Sprintf("%s|%s|%s|%s|%s/%dbit|%dx%d|%s|%s", identity, p.Config.Backend, source.Codec, source.Profile, source.PixelFormat, source.BitDepth, source.Width, source.Height, p.Filter, operation)
	// Thirty-two source-sized surfaces is a conservative decode/reference and
	// filter-pool estimate, not a driver limit. High-bit-depth packed 4:2:0 uses
	// twice the storage. Driver allocations and output pools can differ; runtime
	// observations/pressure adjust the trial instead of calling this safe capacity.
	bytesPerPixel := int64(3)
	if source.BitDepth > 8 {
		bytesPerPixel = 6
	}
	cost := surfaceCost(source.Width, source.Height, bytesPerPixel*16)
	if strings.Contains(p.Filter, "libplacebo") {
		extra := surfaceCost(source.Width, source.Height, 16)
		if cost > math.MaxInt64-extra {
			cost = math.MaxInt64
		} else {
			cost += extra
		}
	}
	return generationbudget.Workload{Key: key, MemoryPerSlot: cost, GPUPerSlot: cost, RuntimeUnidentified: identity == ""}
}

// GenerationPressure only recognizes explicit resource failures. Generic VAAPI
// status 23/24, missing frames, malformed media and unsupported filters/encoders
// do not establish pressure and must not trigger a reduced-concurrency retry.
func GenerationPressure(err error) error {
	if err == nil || generationbudget.IsPressure(err) {
		return err
	}
	if errors.Is(err, syscall.ENOMEM) {
		return &generationbudget.PressureError{Err: err}
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	message := strings.ToLower(string(exitErr.Stderr))
	for _, signal := range []string{"cannot allocate memory", "out of memory", "failed to allocate surface", "failed to allocate a surface", "failed to allocate video memory", "va_status_error_allocation_failed"} {
		if strings.Contains(message, signal) {
			return &generationbudget.PressureError{Err: err}
		}
	}
	return err
}

func surfaceCost(width, height int, bytesPerPixel int64) int64 {
	if width <= 0 || height <= 0 || int64(width) > math.MaxInt64/int64(height)/bytesPerPixel {
		return math.MaxInt64
	}
	return int64(width) * int64(height) * bytesPerPixel
}
