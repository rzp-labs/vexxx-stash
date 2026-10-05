package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stashExec "github.com/stashapp/stash/pkg/exec"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// IntelSource inspects all streams with a bounded, cancellable ffprobe process.
// Multiple video streams are rejected rather than changing FFmpeg's stream choice.
func (f *FFProbe) IntelSource(ctx context.Context, input string) (IntelSource, error) {
	result, err := f.intelSource(ctx, input, true)
	if err != nil {
		return result, err
	}
	return result, result.Validate()
}

// IntelSpriteSource keeps sprite eligibility separate from marker validation.
// Sprites explicitly map video and disable audio, so audio stream count cannot
// change their output or introduce ambiguous automatic audio selection.
func (f *FFProbe) IntelSpriteSource(ctx context.Context, input, backend string) (IntelSource, error) {
	result, err := f.intelSource(ctx, input, false)
	if err != nil {
		return result, err
	}
	return result, result.ValidateSprite(backend)
}

// IntelSourceMetadata reads container/header information without software pixel
// decoding. Eligibility follows a strict GPU frame metadata probe, which supplies
// VUI color and aspect metadata that header-only probing can leave incomplete.
func (f *FFProbe) IntelSourceMetadata(ctx context.Context, input string, requireUnambiguousAudio bool) (IntelSource, error) {
	return f.intelSource(ctx, input, requireUnambiguousAudio)
}

func (f *FFProbe) intelSource(ctx context.Context, input string, requireUnambiguousAudio bool) (IntelSource, error) {
	var result IntelSource
	if f == nil {
		return result, fmt.Errorf("ffprobe unavailable for generation eligibility")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := stashExec.CommandContext(ctx, f.path, "-v", "error", "-fflags", "+no_pixel_probe", "-show_streams", "-show_format", "-of", "json", input)
	output, err := cmd.Output()
	if err != nil {
		return result, fmt.Errorf("GPU header-only metadata probe: %w", gpuMetadataProbeError(err))
	}
	var data struct {
		Format struct {
			StartTime string `json:"start_time"`
		} `json:"format"`
		Streams []struct {
			FFProbeStream
			ColorTransfer  string `json:"color_transfer"`
			ColorPrimaries string `json:"color_primaries"`
			ColorSpace     string `json:"color_space"`
			ColorRange     string `json:"color_range"`
		}
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return result, fmt.Errorf("generation metadata: %w", err)
	}
	// An absent origin cannot safely be assumed zero for concat packet seeks.
	if data.Format.StartTime == "" {
		data.Format.StartTime = "N/A"
	}
	count := 0
	audioCount := 0
	for _, s := range data.Streams {
		if s.CodecType == "audio" {
			audioCount++
		}
		if s.CodecType != "video" || s.Disposition.AttachedPic != 0 {
			continue
		}
		count++
		rotation, _ := strconv.Atoi(s.Tags.Rotate)
		var matrix *[9]int32
		for _, sd := range s.SideDataList {
			if sd.SideDataType == "Display Matrix" || sd.DisplayMatrix != "" {
				if matrix != nil {
					return result, fmt.Errorf("generation metadata contains multiple display matrices")
				}
				parsed, err := intelParseDisplayMatrix(sd.DisplayMatrix)
				if err != nil {
					return result, err
				}
				matrix = &parsed
				rotation = sd.Rotation
			} else if sd.Rotation != 0 {
				rotation = sd.Rotation
			}
		}
		result = IntelSource{Profile: s.Profile, Codec: s.CodecName, PixelFormat: s.PixFmt, Width: s.Width, Height: s.Height, Rotation: rotation, DisplayMatrix: matrix, StreamIndex: s.Index, ColorTransfer: s.ColorTransfer, ColorPrimaries: s.ColorPrimaries, ColorSpace: s.ColorSpace, ColorRange: s.ColorRange, FrameRate: s.RFrameRate, AverageFrameRate: s.AvgFrameRate, Duration: s.Duration, SampleAspectRatio: s.SampleAspectRatio, DisplayAspectRatio: s.DisplayAspectRatio, StartTime: data.Format.StartTime}
	}
	if requireUnambiguousAudio && audioCount > 1 {
		return result, fmt.Errorf("multiple audio streams require software generation to preserve automatic audio selection")
	}
	if count != 1 {
		return result, fmt.Errorf("generation requires exactly one video stream; found %d", count)
	}
	return result, nil
}

// GenerationOutputError identifies an artifact that cannot be published, even
// when FFmpeg returned zero. A header-only MP4 contains no video packets.
type GenerationOutputError struct{ Err error }

func (e *GenerationOutputError) Error() string {
	return "generation output validation: " + e.Err.Error()
}
func (e *GenerationOutputError) Unwrap() error { return e.Err }

func gpuMetadataProbeError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		message := exitErr.Stderr
		if len(message) > 4096 {
			message = message[:4096]
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(message)))
	}
	return err
}

// ValidateVideoOutput verifies at least one actual encoded video packet. It
// does not apply input eligibility rules: explicitly selected software outputs
// also need validation. The caller acquires CPU admission before this bounded probe.
func (f *FFProbe) ValidateVideoOutput(ctx context.Context, path string) error {
	return f.validateVideoOutput(ctx, path, false)
}

// ValidateVideoOutputMetadata verifies GPU output packets without probing or
// decoding compressed image/video pixels. It needs stream identity and packets,
// not decoded frame geometry; -nofind_stream_info also avoids JPEG dimension probes.
func (f *FFProbe) ValidateVideoOutputMetadata(ctx context.Context, path string) error {
	return f.validateVideoOutput(ctx, path, true)
}

func (f *FFProbe) validateVideoOutput(ctx context.Context, path string, metadataOnly bool) error {
	fail := func(err error) error { return &GenerationOutputError{Err: err} }
	if f == nil {
		return fail(fmt.Errorf("ffprobe unavailable; cannot verify generated video"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"-v", "error"}
	if metadataOnly {
		args = append(args, "-fflags", "+no_pixel_probe", "-nofind_stream_info")
	}
	args = append(args, "-select_streams", "v:0", "-count_packets", "-show_entries", "stream=codec_type,codec_name,nb_read_packets", "-of", "json", path)
	cmd := stashExec.CommandContext(ctx, f.path, args...)
	output, err := cmd.Output()
	if err != nil {
		if metadataOnly {
			err = gpuMetadataProbeError(err)
		}
		return fail(fmt.Errorf("generated video packet probe: %w", err))
	}
	var data struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Packets   string `json:"nb_read_packets"`
		}
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return fail(fmt.Errorf("invalid packet probe JSON: %w", err))
	}
	if len(data.Streams) != 1 || data.Streams[0].CodecType != "video" || data.Streams[0].CodecName == "" {
		return fail(fmt.Errorf("generated artifact has no encoded video stream"))
	}
	packets, err := strconv.ParseUint(data.Streams[0].Packets, 10, 64)
	if err != nil || packets == 0 {
		return fail(fmt.Errorf("generated artifact has no encoded video packets (count=%q)", data.Streams[0].Packets))
	}
	return nil
}
