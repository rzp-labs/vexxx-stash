package generate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os/exec"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/generationbudget"
)

const intelMetadataPrefix = "VEXXX_GPU_METADATA="

// Header inspection must not discover stream properties by software decoding.
// Decode one hardware frame separately, after releasing CPU admission, and use
// its actual properties before workload-specific source eligibility is checked.
func (g Generator) intelSourceMetadata(ctx context.Context, lockCtx *fsutil.LockContext, input string, config ffmpeg.IntelGenerationConfig, requireAudio bool) (ffmpeg.IntelSource, error) {
	release, err := g.generationBudget().Acquire(ctx, generationbudget.CPU)
	if err != nil {
		return ffmpeg.IntelSource{}, err
	}
	source, err := g.Probe.IntelSourceMetadata(ctx, input, requireAudio)
	release()
	if err != nil {
		return source, err
	}
	if config.Backend != "vaapi" && config.Backend != "qsv" {
		return source, fmt.Errorf("GPU metadata requires an explicit Intel hardware backend")
	}
	if g.Encoder == nil {
		return source, fmt.Errorf("ffmpeg unavailable for GPU frame metadata")
	}
	args := ffmpeg.Args{"-v", "error", "-nostdin", "-abort_on", "empty_output"}
	args = append(args, ffmpeg.IntelInputArgs(config, source)...)
	args = append(args, "-noautorotate", "-display_rotation:v", "0", "-hwaccel_metadata", "1", "-i", input, "-frames:v", "1", "-an", "-c:v", "wrapped_avframe", "-f", "null", "-")
	output, err := g.generateOutputWithContext(ffmpeg.WithIntelProbeTimeout(ctx, 10*time.Second), lockCtx, args)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			stderr := exitErr.Stderr
			truncated := ""
			if len(stderr) > 4096 {
				stderr = stderr[:4096]
				truncated = " [truncated]"
			}
			return source, fmt.Errorf("GPU first-frame metadata: %w: %s%s", err, strings.TrimSpace(string(stderr)), truncated)
		}
		return source, fmt.Errorf("GPU first-frame metadata: %w", err)
	}
	source, err = mergeIntelFrameMetadata(source, output)
	if err == nil {
		source.RuntimeFingerprint = ffmpeg.IntelRuntimeFingerprint(g.Encoder.Path(), config.Device)
	}
	return source, err
}

func mergeIntelFrameMetadata(source ffmpeg.IntelSource, output []byte) (ffmpeg.IntelSource, error) {
	var line string
	for _, candidate := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(candidate, intelMetadataPrefix) {
			if line != "" {
				return source, fmt.Errorf("GPU metadata returned multiple first-frame records")
			}
			line = strings.TrimPrefix(candidate, intelMetadataPrefix)
		}
	}
	if line == "" {
		return source, fmt.Errorf("GPU metadata did not return a first hardware frame record")
	}
	var frame struct {
		StreamIndex *int    `json:"stream_index"`
		BitDepth    *int    `json:"bit_depth"`
		IsRGB       *bool   `json:"is_rgb"`
		Width       *int    `json:"width"`
		Height      *int    `json:"height"`
		PixelFormat *string `json:"pix_fmt"`
		SAR         *string `json:"sample_aspect_ratio"`
		ColorRange  *string `json:"color_range"`
		ColorSpace  *string `json:"color_space"`
		Primaries   *string `json:"color_primaries"`
		Transfer    *string `json:"color_transfer"`
		FrameRate   *string `json:"frame_rate"`
	}
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		return source, fmt.Errorf("invalid GPU metadata JSON: %w", err)
	}
	if frame.StreamIndex == nil || *frame.StreamIndex < 0 {
		return source, fmt.Errorf("GPU metadata requires the selected stream index")
	}
	if len(source.MetadataCandidates) > 0 {
		found := false
		for _, candidate := range source.MetadataCandidates {
			if candidate.StreamIndex == *frame.StreamIndex {
				source, found = candidate, true
				break
			}
		}
		if !found {
			return source, fmt.Errorf("GPU metadata selected an unknown video stream %d", *frame.StreamIndex)
		}
	} else if source.StreamIndex != *frame.StreamIndex {
		return source, fmt.Errorf("GPU metadata stream index differs from source")
	}
	if source.MetadataError != nil {
		return source, source.MetadataError
	}
	source.MetadataCandidates = nil
	if frame.BitDepth == nil || *frame.BitDepth <= 0 {
		return source, fmt.Errorf("GPU metadata requires valid actual pixel bit depth")
	}
	source.BitDepth = *frame.BitDepth
	if frame.IsRGB == nil {
		return source, fmt.Errorf("GPU metadata requires the actual RGB descriptor flag")
	}
	source.IsRGB = *frame.IsRGB
	if frame.Width == nil || frame.Height == nil || *frame.Width <= 0 || *frame.Height <= 0 {
		return source, fmt.Errorf("GPU metadata requires positive actual frame dimensions")
	}
	fields := []struct {
		name  string
		value *string
	}{
		{"pix_fmt", frame.PixelFormat}, {"sample_aspect_ratio", frame.SAR}, {"color_range", frame.ColorRange},
		{"color_space", frame.ColorSpace}, {"color_primaries", frame.Primaries}, {"color_transfer", frame.Transfer}, {"frame_rate", frame.FrameRate},
	}
	for _, field := range fields {
		if field.value == nil || *field.value == "" || strings.TrimSpace(*field.value) != *field.value {
			return source, fmt.Errorf("GPU metadata is missing actual %s", field.name)
		}
	}
	pixelFormat := *frame.PixelFormat
	switch pixelFormat {
	case "nv12":
		pixelFormat = "yuv420p"
	case "p010le":
		pixelFormat = "yuv420p10le"
	default:
		// The strict CLI reports the software descriptor of an actual resident
		// hardware pool; format support belongs to the operation's GPU probes.
		if pixelFormat == "vaapi" || pixelFormat == "qsv" || pixelFormat == "vulkan" || strings.ContainsAny(pixelFormat, " ,:;\t\n") {
			return source, fmt.Errorf("GPU metadata returned invalid surface pixel format %q", pixelFormat)
		}
	}
	sar, ok := new(big.Rat).SetString(strings.ReplaceAll(*frame.SAR, ":", "/"))
	if !ok || sar.Sign() < 0 {
		return source, fmt.Errorf("GPU metadata returned invalid sample aspect ratio %q", *frame.SAR)
	}
	actualSAR := strings.ReplaceAll(*frame.SAR, "/", ":")
	if sar.Sign() == 0 {
		if headerSAR, ok := new(big.Rat).SetString(strings.ReplaceAll(source.SampleAspectRatio, ":", "/")); ok && headerSAR.Sign() > 0 {
			actualSAR = source.SampleAspectRatio
		}
	}
	if *frame.ColorRange != "tv" && *frame.ColorRange != "pc" && *frame.ColorRange != "unknown" {
		return source, fmt.Errorf("GPU metadata returned invalid color range %q", *frame.ColorRange)
	}
	// Actual unspecified color enums are retained as unknown. An absent field
	// never authorizes assuming SDR or retaining potentially incomplete headers.
	for _, field := range []struct{ name, value, enums string }{
		{"color_space", *frame.ColorSpace, "unknown gbr bt709 fcc bt470bg smpte170m smpte240m ycgco ycgco-re ycgco-ro bt2020nc bt2020c smpte2085 chroma-derived-nc chroma-derived-c ictcp ipt-c2"},
		{"color_primaries", *frame.Primaries, "unknown bt709 bt470m bt470bg smpte170m smpte240m film bt2020 smpte428 smpte431 smpte432 jedec-p22 ebu3213 vgamut"},
		{"color_transfer", *frame.Transfer, "unknown bt709 bt470m bt470bg smpte170m smpte240m linear log100 log316 iec61966-2-4 bt1361e iec61966-2-1 bt2020-10 bt2020-12 smpte2084 smpte428 arib-std-b67 vlog"},
	} {
		if !strings.Contains(" "+field.enums+" ", " "+field.value+" ") {
			return source, fmt.Errorf("GPU metadata returned invalid %s %q", field.name, field.value)
		}
	}
	frameRate, rateOK := new(big.Rat).SetString(*frame.FrameRate)
	if *frame.FrameRate != "0/0" && (!rateOK || frameRate.Sign() < 0) {
		return source, fmt.Errorf("GPU metadata returned invalid frame rate %q", *frame.FrameRate)
	}
	validRate := func(value string) bool { rate, ok := new(big.Rat).SetString(value); return ok && rate.Sign() > 0 }
	// Container r_frame_rate is the existing VTT/frame-sampling contract. A
	// decoder's nominal bitstream rate must not silently resample that contract.
	if !validRate(source.FrameRate) {
		if rateOK && frameRate.Sign() > 0 {
			source.FrameRate = *frame.FrameRate
		} else if validRate(source.AverageFrameRate) {
			source.FrameRate = source.AverageFrameRate
		}
	}
	source.Width, source.Height, source.PixelFormat = *frame.Width, *frame.Height, pixelFormat
	source.SampleAspectRatio = actualSAR
	if actualRatio, ok := new(big.Rat).SetString(strings.ReplaceAll(actualSAR, ":", "/")); ok && actualRatio.Sign() > 0 {
		dar := new(big.Rat).Mul(actualRatio, big.NewRat(int64(source.Width), int64(source.Height)))
		source.DisplayAspectRatio = dar.Num().String() + ":" + dar.Denom().String()
	} else {
		// The decoded frame explicitly reports absent SAR. Discard an obsolete
		// header DAR rather than inventing a pixel ratio or rejecting CPU-default
		// square geometry using dimensions from incomplete header inspection.
		source.DisplayAspectRatio = ""
	}
	source.ColorRange, source.ColorSpace = *frame.ColorRange, *frame.ColorSpace
	source.ColorPrimaries, source.ColorTransfer = *frame.Primaries, *frame.Transfer
	return source, nil
}
