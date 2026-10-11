package diagnostics

import (
	"context"
	"errors"
	"math"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PreviewRequest contains typed producer values, never source names or argv.
// Nil means unavailable, rather than an inferred zero seek or duration.
type PreviewRequest struct {
	Segments         int      `json:"segments"`
	SegmentSeconds   *float64 `json:"segment_seconds,omitempty"`
	DurationSeconds  *float64 `json:"duration_seconds,omitempty"`
	LimitStart       *float64 `json:"limit_start_seconds,omitempty"`
	LimitEnd         *float64 `json:"limit_end_seconds,omitempty"`
	ExcludeStart     *float64 `json:"exclude_start,omitempty"`
	ExcludeEnd       *float64 `json:"exclude_end,omitempty"`
	ExcludeStartUnit string   `json:"exclude_start_unit,omitempty"`
	ExcludeEndUnit   string   `json:"exclude_end_unit,omitempty"`
	Audio            bool     `json:"audio"`
	Projection       string   `json:"projection,omitempty"`
}

type PreviewCommand struct {
	Stage              string         `json:"stage"`
	Started            *bool          `json:"started,omitempty"`
	SeekSeconds        *float64       `json:"seek_seconds,omitempty"`
	DurationSeconds    *float64       `json:"duration_seconds,omitempty"`
	StreamIndex        *int           `json:"stream_index,omitempty"`
	VideoSelection     string         `json:"video_selection,omitempty"`
	FrameLimit         *int           `json:"frame_limit,omitempty"`
	PlanID             string         `json:"plan_id,omitempty"`
	PlanFingerprint    string         `json:"plan_fingerprint,omitempty"`
	FilterConfigured   bool           `json:"filter_configured"`
	TargetWidth        *int           `json:"target_width,omitempty"`
	TargetHeight       *int           `json:"target_height,omitempty"`
	Audio              *bool          `json:"audio,omitempty"`
	Vsync2             *bool          `json:"vsync2,omitempty"`
	Projection         string         `json:"projection,omitempty"`
	RuntimeFingerprint string         `json:"runtime_fingerprint,omitempty"`
	InputPixels        *PreviewPixels `json:"plan_input_pixels,omitempty"`
	ProcessedPixels    *PreviewPixels `json:"plan_processed_pixels,omitempty"`
}

// PreviewPixels keeps the numerical/color inputs used to build the canonical
// filter, including the physical input before a projection changes geometry.
type PreviewPixels struct {
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Rotation       int       `json:"rotation"`
	StreamIndex    int       `json:"metadata_stream_index"`
	BitDepth       int       `json:"bit_depth"`
	IsRGB          bool      `json:"is_rgb"`
	PixelFormat    string    `json:"pixel_format,omitempty"`
	DisplayMatrix  *[9]int32 `json:"display_matrix,omitempty"`
	SARNum         *int64    `json:"sar_numerator,omitempty"`
	SARDen         *int64    `json:"sar_denominator,omitempty"`
	ColorSpace     string    `json:"color_space,omitempty"`
	ColorPrimaries string    `json:"color_primaries,omitempty"`
	ColorTransfer  string    `json:"color_transfer,omitempty"`
	ColorRange     string    `json:"color_range,omitempty"`
}

// PreviewReplay is safe to serialize. Internal identifiers and the retention
// callback exist only in the request's private context binding.
type PreviewReplay struct {
	Version         int            `json:"version"`
	Reference       string         `json:"reference"`
	SourceReference string         `json:"source_reference"`
	Retained        bool           `json:"retained"`
	Retention       string         `json:"retention"`
	SourceResolved  bool           `json:"source_resolved"`
	Request         PreviewRequest `json:"requested_parameters"`
	Command         PreviewCommand `json:"command"`
}

type PreviewReplayRecord struct {
	RecordedAt     time.Time     `json:"recorded_at"`
	JobCorrelation string        `json:"job_correlation,omitempty"`
	SceneID        int           `json:"scene_id,omitempty"`
	FileID         int           `json:"file_id,omitempty"`
	Replay         PreviewReplay `json:"replay"`
}

type previewSource struct {
	reference, job string
	scene, file    int
	request        PreviewRequest
	retain         func(PreviewReplayRecord) error
}
type previewSourceKey struct{}

// WithPreviewSource creates a per-request opaque reference. No path is stored.
func WithPreviewSource(ctx context.Context, sceneID, fileID int, job string, retain func(PreviewReplayRecord) error) context.Context {
	return context.WithValue(ctx, previewSourceKey{}, previewSource{reference: uuid.NewString(), job: replayUUID(job), scene: max(sceneID, 0), file: max(fileID, 0), retain: retain})
}

func WithPreviewRequest(ctx context.Context, request PreviewRequest) context.Context {
	source, ok := ctx.Value(previewSourceKey{}).(previewSource)
	if !ok {
		return ctx
	}
	source.request = request.safe()
	return context.WithValue(ctx, previewSourceKey{}, source)
}

// HasPreviewSource prevents unrelated generation operations from acquiring
// preview records or doing persistent IO.
func HasPreviewSource(ctx context.Context) bool {
	_, ok := ctx.Value(previewSourceKey{}).(previewSource)
	return ok
}

type PreviewReplayError struct {
	Err    error
	Replay PreviewReplay
}

func (e *PreviewReplayError) Error() string               { return e.Err.Error() }
func (e *PreviewReplayError) Unwrap() error               { return e.Err }
func (e *PreviewReplayError) DiagnosticTransparent() bool { return true }

// WithPreviewFailure runs only at a known failing producer. Wrappers above a
// detailed failure keep its execution facts; joined siblings keep their own.
// Pure cancellation continues to be omitted by the existing diagnostics policy.
func WithPreviewFailure(ctx context.Context, err error, command PreviewCommand) error {
	if err == nil || containsJoin(err) || errors.Is(err, context.Canceled) {
		return err
	}
	source, ok := ctx.Value(previewSourceKey{}).(previewSource)
	if !ok {
		return err
	}
	var existing *PreviewReplayError
	if errors.As(err, &existing) {
		return err
	}
	var execution interface{ DiagnosticCommandStarted() bool }
	if errors.As(err, &execution) {
		started := execution.DiagnosticCommandStarted()
		command.Started = &started
	} else {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ProcessState != nil {
			started := true
			command.Started = &started
		}
	}
	replay := PreviewReplay{Version: 1, Reference: uuid.NewString(), SourceReference: source.reference, Retention: "unavailable",
		SourceResolved: source.scene > 0 && source.file > 0, Request: source.request.safe(), Command: command.safe()}
	if source.retain != nil {
		record := PreviewReplayRecord{RecordedAt: time.Now().UTC(), JobCorrelation: source.job, SceneID: source.scene, FileID: source.file, Replay: replay}
		err := source.retain(record)
		if err == nil {
			replay.Retained, replay.Retention = true, "retained"
		} else if errors.Is(err, ErrPreviewReplayQueued) {
			replay.Retention = "queued"
		}
	}
	return &PreviewReplayError{Err: err, Replay: replay}
}

// PreviewFailureSnapshot never borrows command facts from a joined sibling.
func PreviewFailureSnapshot(err error) (PreviewReplay, bool) {
	if err == nil || containsJoin(err) {
		return PreviewReplay{}, false
	}
	var failure *PreviewReplayError
	if !errors.As(err, &failure) {
		return PreviewReplay{}, false
	}
	r := failure.Replay
	r.Request, r.Command = r.Request.safe(), r.Command.safe()
	return r, true
}

var replayHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func replayUUID(value string) string {
	id, err := uuid.Parse(value)
	if err != nil {
		return ""
	}
	return id.String()
}
func replayFloat(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	copy := *value
	return &copy
}
func replayInt(value *int) *int {
	if value == nil || *value < 0 {
		return nil
	}
	copy := *value
	return &copy
}
func replayBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func replayUnit(value string) string {
	if value == "seconds" || value == "percent" {
		return strings.Clone(value)
	}
	return ""
}
func (r PreviewRequest) safe() PreviewRequest {
	r.Segments = max(r.Segments, 0)
	r.SegmentSeconds, r.DurationSeconds = replayFloat(r.SegmentSeconds), replayFloat(r.DurationSeconds)
	r.LimitStart, r.LimitEnd = replayFloat(r.LimitStart), replayFloat(r.LimitEnd)
	r.ExcludeStart, r.ExcludeEnd = replayFloat(r.ExcludeStart), replayFloat(r.ExcludeEnd)
	r.ExcludeStartUnit, r.ExcludeEndUnit = replayUnit(r.ExcludeStartUnit), replayUnit(r.ExcludeEndUnit)
	r.Projection = replayProjection(r.Projection)
	if r.ExcludeStartUnit == "" {
		r.ExcludeStart = nil
	}
	if r.ExcludeEndUnit == "" {
		r.ExcludeEnd = nil
	}
	return r
}
func (c PreviewCommand) safe() PreviewCommand {
	switch c.Stage {
	case "header", "metadata", "device", "eligibility", "decode", "filter", "projection", "encode", "render", "concat", "output", "generation":
		c.Stage = strings.Clone(c.Stage)
	default:
		c.Stage = "unavailable"
	}
	c.SeekSeconds, c.DurationSeconds = replayFloat(c.SeekSeconds), replayFloat(c.DurationSeconds)
	c.StreamIndex, c.FrameLimit = replayInt(c.StreamIndex), replayInt(c.FrameLimit)
	c.TargetWidth, c.TargetHeight = replayInt(c.TargetWidth), replayInt(c.TargetHeight)
	c.Audio, c.Vsync2, c.Started = replayBool(c.Audio), replayBool(c.Vsync2), replayBool(c.Started)
	c.InputPixels, c.ProcessedPixels = c.InputPixels.safe(), c.ProcessedPixels.safe()
	c.VideoSelection = replayEnum(c.VideoSelection, "automatic mapped first_video")
	c.Projection = replayProjection(c.Projection)
	if c.PlanID != "intel-scene-preview-v1" {
		c.PlanID = ""
	} else {
		c.PlanID = "intel-scene-preview-v1"
	}
	if !replayHash.MatchString(c.PlanFingerprint) {
		c.PlanFingerprint = ""
	} else {
		c.PlanFingerprint = strings.Clone(c.PlanFingerprint)
	}
	if !replayHash.MatchString(c.RuntimeFingerprint) {
		c.RuntimeFingerprint = ""
	} else {
		c.RuntimeFingerprint = strings.Clone(c.RuntimeFingerprint)
	}
	return c
}

func replayProjection(value string) string {
	switch value {
	case "", "LR180", "TB360", "MONO360", "FISHEYE190":
		return strings.Clone(value)
	}
	return ""
}

func (value *PreviewPixels) safe() *PreviewPixels {
	if value == nil {
		return nil
	}
	p := *value
	p.Width, p.Height, p.StreamIndex, p.BitDepth = max(p.Width, 0), max(p.Height, 0), max(p.StreamIndex, 0), max(p.BitDepth, 0)
	if p.DisplayMatrix != nil {
		copy := *p.DisplayMatrix
		p.DisplayMatrix = &copy
	}
	if p.SARNum != nil && p.SARDen != nil && *p.SARNum >= 0 && *p.SARDen > 0 {
		n, d := *p.SARNum, *p.SARDen
		p.SARNum, p.SARDen = &n, &d
	} else {
		p.SARNum, p.SARDen = nil, nil
	}
	p.ColorSpace = replayEnum(p.ColorSpace, "gbr bt709 fcc bt470bg smpte170m smpte240m ycgco ycgco-re ycgco-ro bt2020nc bt2020c smpte2085 chroma-derived-nc chroma-derived-c ictcp ipt-c2")
	p.ColorPrimaries = replayEnum(p.ColorPrimaries, "bt709 bt470m bt470bg smpte170m smpte240m film bt2020 smpte428 smpte431 smpte432 jedec-p22 ebu3213 vgamut")
	p.ColorTransfer = replayEnum(p.ColorTransfer, "bt709 bt470m bt470bg smpte170m smpte240m linear log100 log316 iec61966-2-4 bt1361e iec61966-2-1 bt2020-10 bt2020-12 smpte2084 smpte428 arib-std-b67 vlog")
	p.ColorRange = replayEnum(p.ColorRange, "tv pc")
	p.PixelFormat = replayEnum(p.PixelFormat, "yuv420p yuv422p yuv444p yuv440p yuv410p yuv411p yuvj420p yuvj422p yuvj444p nv12 nv21 rgb24 bgr24 rgba bgra argb abgr gbrp gray p010le p010be x2rgb10le x2rgb10be x2bgr10le x2bgr10be yuv420p10le yuv422p10le yuv444p10le gbrp10le yuv420p12le yuv422p12le yuv444p12le gbrp12le yuv420p16le yuv422p16le yuv444p16le gbrp16le")
	return &p
}
func replayEnum(value, allowed string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n") && strings.Contains(" "+allowed+" unknown unspecified ", " "+value+" ") {
		return strings.Clone(value)
	}
	return ""
}
