package ffmpeg

// Intel generation is deliberately independent of playback and native generation.
// It is opt-in and accepts only the conservative 8-bit SDR candidate formats.
// Hardware and visual acceptance are measured separately on each GPU.
import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type IntelGenerationConfig struct {
	Backend      string
	Device       string
	ProbeTimeout time.Duration
}

type IntelSource struct {
	Profile                                                                   string
	Codec, PixelFormat, ColorTransfer, ColorPrimaries, ColorSpace, ColorRange string
	Width, Height, Rotation, StreamIndex                                      int
	// DisplayMatrix preserves reflections which the scalar rotation cannot express.
	DisplayMatrix                                                                *[9]int32
	FrameRate, AverageFrameRate, Duration, SampleAspectRatio, DisplayAspectRatio string
	// StartTime is the demuxer timestamp origin used by relative input seeks.
	StartTime string
}

type IntelGenerationDiagnostic struct {
	Selected, Actual, Stage, Reason string
}

type IntelProbeStep struct {
	Stage string
	Args  Args
}
type IntelGenerationRunner func(context.Context, Args) error

type IntelGenerationPlan struct {
	Config    IntelGenerationConfig
	Source    IntelSource
	InputArgs Args
	Filter    string
	Probes    []IntelProbeStep
}

func (c IntelGenerationConfig) Enabled() bool { return c.Backend != "" && c.Backend != "software" }
func (c IntelGenerationConfig) timeout() time.Duration {
	if c.ProbeTimeout <= 0 {
		return 10 * time.Second
	}
	if c.ProbeTimeout > 30*time.Second {
		return 30 * time.Second
	}
	return c.ProbeTimeout
}

var intelRenderDevice = regexp.MustCompile(`^/dev/dri/renderD[0-9]+$`)

func ValidateIntelDevice(device string) error {
	if !intelRenderDevice.MatchString(device) {
		return fmt.Errorf("select an explicit /dev/dri/renderD<number> device")
	}
	st, err := os.Stat(device)
	if err != nil {
		return fmt.Errorf("render device unavailable: %w", err)
	}
	if st.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("render device is not a character device")
	}
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("render device cannot be opened for read/write: %w", err)
	}
	return f.Close()
}

func (s IntelSource) Validate() error {
	if s.Codec != "h264" && s.Codec != "hevc" {
		return fmt.Errorf("unsupported input codec %q", s.Codec)
	}
	if s.PixelFormat != "yuv420p" && s.PixelFormat != "nv12" {
		return fmt.Errorf("input pixel format %q requires software generation (only 8-bit 4:2:0 validated)", s.PixelFormat)
	}
	if s.Width <= 0 || s.Height <= 0 {
		return fmt.Errorf("input dimensions unavailable")
	}
	if _, err := intelRotationDegrees(s); err != nil {
		return err
	}
	if s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67" || strings.HasPrefix(s.ColorPrimaries, "bt2020") || strings.HasPrefix(s.ColorSpace, "bt2020") {
		return fmt.Errorf("HDR/wide-gamut input requires software generation")
	}
	return nil
}

// IntelScaleFilter uses concrete dimensions to avoid QSV's differing negative
// height semantics. FFmpeg scale=w:-2 rounds the proportional height to even.
func IntelScaleFilter(config IntelGenerationConfig, source IntelSource, width int, download bool) string {
	displayWidth, displayHeight := IntelDisplayDimensions(source)
	height := int(math.Round(float64(displayHeight)*float64(width)/float64(displayWidth)/2)) * 2
	if height < 2 {
		height = 2
	}
	filter := fmt.Sprintf("scale_%s=w=%d:h=%d:format=nv12", config.Backend, width, height)
	if download {
		filter += ",hwdownload,format=nv12"
	}
	return filter
}

func IntelInputArgs(config IntelGenerationConfig, source IntelSource) Args {
	args := Args{"-init_hw_device", "vaapi=vex:" + config.Device}
	if source.Rotation != 0 || source.DisplayMatrix != nil {
		// Keep pixels native until the explicit GPU transform, and clear the
		// propagated display matrix so encoded outputs cannot rotate twice.
		args = append(args, "-noautorotate", "-display_rotation", "0")
	}
	if config.Backend == "qsv" {
		args = append(args, "-init_hw_device", "qsv=vexq@vex", "-filter_hw_device", "vexq", "-hwaccel", "qsv", "-hwaccel_device", "vexq", "-hwaccel_output_format", "qsv", "-c:v", source.Codec+"_qsv")
	} else {
		args = append(args, "-filter_hw_device", "vex", "-hwaccel", "vaapi", "-hwaccel_device", "vex", "-hwaccel_output_format", "vaapi")
	}
	return args
}

// NewIntelGenerationPlan builds separate, actual source-decode, scale/transfer
// and source encode probes. No encoders-list result is treated as capability.
// Device checking is deferred to RunIntelGeneration to keep construction pure.
func NewIntelGenerationPlan(config IntelGenerationConfig, source IntelSource, input string, start float64, width int, download bool) (IntelGenerationPlan, error) {
	p := IntelGenerationPlan{Config: config, Source: source}
	if config.Backend != "vaapi" && config.Backend != "qsv" {
		return p, fmt.Errorf("unsupported Intel generation backend %q", config.Backend)
	}
	if err := source.Validate(); err != nil {
		return p, err
	}
	if width <= 0 || width%2 != 0 {
		return p, fmt.Errorf("Intel output width must be positive and even")
	}
	rotation, err := IntelRotationFilter(config, source)
	if err != nil {
		return p, err
	}
	p.InputArgs = IntelInputArgs(config, source)
	p.Filter = intelPrependRotation(rotation, IntelScaleFilter(config, source, width, download))
	// Null output otherwise succeeds at EOF without exercising the hardware.
	// Every probe maps only video, so a packet reaching its muxer proves that
	// the bounded frame traversed the requested decode/filter/encode stage.
	base := Args{"-v", "error", "-nostdin", "-abort_on", "empty_output", "-threads", "1"}
	base = append(base, p.InputArgs...)
	if start > 0 {
		base = base.Seek(start)
	}
	base = base.Input(input)
	base = append(base, "-map", fmt.Sprintf("0:%d", source.StreamIndex), "-an", "-frames:v", "1")
	p.Probes = append(p.Probes, IntelProbeStep{"decode", append(append(Args{}, base...), "-f", "null", "-")})
	p.Probes = append(p.Probes, IntelProbeStep{"scale", append(append(Args{}, base...), "-vf", intelPrependRotation(rotation, IntelScaleFilter(config, source, width, false)), "-f", "null", "-")})
	if download {
		p.Probes = append(p.Probes, IntelProbeStep{"download", append(append(Args{}, base...), "-vf", p.Filter, "-f", "null", "-")})
	}
	encode := append(append(Args{}, base...), "-vf", intelPrependRotation(rotation, IntelScaleFilter(config, source, width, false)), "-c:v", "h264_"+config.Backend, "-f", "null", "-")
	p.Probes = append(p.Probes, IntelProbeStep{"encode", encode})
	return p, nil
}

// RunIntelGeneration performs at most one software retry. Its runner must apply
// the same process budget to probes, hardware generation and software fallback.
// Cancellation of the caller never starts another process. A probe timeout is a
// capability failure and can fall back if the caller itself is still alive.
// The runner must call IntelProbeExecutionContext AFTER budget admission so that
// queue wait does not consume the capability probe's execution timeout.
func RunIntelGeneration(ctx context.Context, plan IntelGenerationPlan, hardware, software Args, run IntelGenerationRunner) (IntelGenerationDiagnostic, error) {
	return RunIntelGenerationWork(ctx, plan, func(ctx context.Context) error { return run(ctx, hardware) }, func(ctx context.Context) error { return run(ctx, software) }, run)
}

// RunIntelGenerationWork shares probes across compound workloads. A nil
// software callback makes hardware selection strict: device, capability and
// execution failures are returned without starting software rendering.
func RunIntelGenerationWork(ctx context.Context, plan IntelGenerationPlan, hardware, software func(context.Context) error, run IntelGenerationRunner) (IntelGenerationDiagnostic, error) {
	return runIntelGenerationWork(ctx, plan, hardware, software, run, ValidateIntelDevice)
}
func runIntelGenerationWork(ctx context.Context, plan IntelGenerationPlan, hardware, software func(context.Context) error, run IntelGenerationRunner, checkDevice func(string) error) (IntelGenerationDiagnostic, error) {
	d := IntelGenerationDiagnostic{Selected: plan.Config.Backend, Actual: "none"}
	if err := ctx.Err(); err != nil {
		d.Stage = "cancellation"
		d.Reason = err.Error()
		return d, err
	}
	fallback := func(stage string, err error) (IntelGenerationDiagnostic, error) {
		d.Stage = stage
		d.Reason = intelFailureReason(err)
		if ctx.Err() != nil {
			return d, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return d, err
		}
		if software == nil {
			d.Actual = "none"
			return d, fmt.Errorf("GPU %s generation failed at %s (software rendering disabled): %w", plan.Config.Backend, stage, err)
		}
		d.Actual = "software"
		fallbackErr := software(ctx)
		var outputError *GenerationOutputError
		if errors.As(fallbackErr, &outputError) {
			d.Stage = "output"
			d.Reason = intelFailureReason(fallbackErr)
		}
		return d, fallbackErr
	}
	if !plan.Config.Enabled() {
		if software == nil {
			return d, fmt.Errorf("GPU generation requires an explicitly selected hardware backend")
		}
		d.Actual = "software"
		return d, software(ctx)
	}
	if err := checkDevice(plan.Config.Device); err != nil {
		return fallback("device", err)
	}
	for _, p := range plan.Probes {
		if err := ctx.Err(); err != nil {
			return d, err
		}
		probeCtx := WithIntelProbeTimeout(ctx, plan.Config.timeout())
		err := run(probeCtx, p.Args)
		if err != nil {
			return fallback(p.Stage, err)
		}
	}
	if err := ctx.Err(); err != nil {
		d.Stage = "cancellation"
		d.Reason = err.Error()
		return d, err
	}
	d.Actual = plan.Config.Backend
	if err := hardware(ctx); err != nil {
		return fallback(intelFailureStage(err), err)
	}
	d.Actual = plan.Config.Backend
	return d, nil
}

// Keep FFmpeg's actionable stderr with a bounded diagnostic, rather than only
// its generic exit status. Unknown runtime failures retain the generation stage.
func intelFailureReason(err error) string {
	reason := err.Error()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		stderr := string(exitErr.Stderr)
		if len(stderr) > 4096 {
			stderr = stderr[:4096] + " [truncated]"
		}
		reason += ": " + strings.TrimSpace(stderr)
	}
	return reason
}
func intelFailureStage(err error) string {
	var outputError *GenerationOutputError
	if errors.As(err, &outputError) {
		return "output"
	}
	reason := strings.ToLower(intelFailureReason(err))
	switch {
	case strings.Contains(reason, "error while decoding"), strings.Contains(reason, "error submitting packet to decoder"):
		return "decode"
	case strings.Contains(reason, "error reinitializing filters"), strings.Contains(reason, "error initializing filter"), strings.Contains(reason, "no such filter"):
		return "filter"
	case strings.Contains(reason, "error while opening encoder"), strings.Contains(reason, "error initializing output stream"), strings.Contains(reason, "error initializing the encoder"):
		return "encode"
	default:
		return "generation"
	}
}

type intelProbeTimeoutKey struct{}

// WithIntelProbeTimeout records an execution limit without timing queue waits.
func WithIntelProbeTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, intelProbeTimeoutKey{}, timeout)
}

// IntelProbeExecutionContext starts a probe's limit after resource admission.
// Ordinary generation commands retain their caller's context unchanged.
func IntelProbeExecutionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if timeout, ok := ctx.Value(intelProbeTimeoutKey{}).(time.Duration); ok && timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
}
