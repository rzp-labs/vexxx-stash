package analytics

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/generationbudget"
)

type GenerationFailureContext struct {
	JobCorrelation  string
	Workload        string
	Configured      generationbudget.Settings
	Effective       generationbudget.Settings
	ParallelTasks   int
	BudgetEnabled   bool
	SelectedBackend string
	// PrivateValues are used only for redaction, never serialized as properties.
	PrivateValues []string
}

// CaptureGenerationFailure is called once at the failed production task boundary.
// Joined marker/output failures are captured individually; accepted identities
// prevent a duplicate at the ordinary job boundary. Pure cancellation is omitted.
func CaptureGenerationFailure(ctx context.Context, err error, info GenerationFailureContext) {
	if client == nil {
		return
	}
	for _, entry := range diagnostics.Split(err).Entries {
		event := generationException(entry.Err, err, info)
		private := append(diagnostics.ErrorPrivate(entry.Err), info.PrivateValues...)
		event.Properties.Set("operation_cause", diagnostics.Safe(entry.Cause, private))
		captureException(ctx, entry.Err, event)
	}

}

func CaptureWorkerPanic(_ context.Context, value any, correlation string) {
	if client == nil {
		return
	}
	exception := PanicException(value)
	exception.Properties.Set("$exception_level", "error").Set("job_correlation", safeCorrelation(correlation)).Set("failure_origin", "worker_panic")
	handled := true
	exception.ExceptionList[0].Mechanism.Handled = &handled
	_ = client.Enqueue(exception)
}

func safeCorrelation(value string) string {
	if _, err := uuid.Parse(value); err == nil {
		return value
	}
	return "unavailable"
}

func GenerationException(err error, info GenerationFailureContext) posthog.Exception {
	return generationException(err, err, info)
}
func generationException(err, root error, info GenerationFailureContext) posthog.Exception {
	properties := ReleaseProperties().Set("$process_person_profile", false).Set("$exception_level", "error").
		Set("failure_origin", "generation").Set("job_correlation", safeCorrelation(info.JobCorrelation)).
		Set("generation_workload", safeWorkload(info.Workload)).Set("generation_budget_enabled", info.BudgetEnabled).
		Set("generation_parallel_tasks", info.ParallelTasks)
	for _, entry := range []struct {
		prefix string
		limits generationbudget.Settings
	}{{"configured", info.Configured}, {"effective", info.Effective}} {
		properties.Set("generation_"+entry.prefix+"_processes", entry.limits.MaxProcesses).
			Set("generation_"+entry.prefix+"_gpu_processes", entry.limits.MaxGPUProcesses).
			Set("generation_"+entry.prefix+"_threads", entry.limits.Threads)
	}
	selected, actual, stage := safeBackend(info.SelectedBackend), "none", "generation"
	private := append([]string(nil), info.PrivateValues...)
	var intel *ffmpeg.IntelGenerationError
	if errors.As(root, &intel) {
		selected, actual = safeBackend(intel.Diagnostic.Selected), safeBackend(intel.Diagnostic.Actual)
		stage = safeStage(intel.Diagnostic.Stage)
		if intel.Source.Width > 0 && intel.Source.Height > 0 {
			properties.Set("source_width", intel.Source.Width).Set("source_height", intel.Source.Height)
		}
		addSourceProperties(properties, intel.Source)
		properties.Set("generation_reason", diagnostics.Safe(intel.Diagnostic.Reason, info.PrivateValues)).Set("generation_device", diagnostics.Safe(intel.Diagnostic.Device, info.PrivateValues)).Set("generation_filter", diagnostics.Safe(intel.Diagnostic.Filter, info.PrivateValues)).Set("generation_probe_timeout_ms", intel.Diagnostic.ProbeTimeout.Milliseconds()).Set("generation_probe_stages", intel.Diagnostic.ProbeStages)
	}
	var command *ffmpeg.GenerationCommandError
	if errors.As(err, &command) {
		properties.Set("generation_admitted_slots", command.Admitted).
			Set("generation_command_process_limit", command.Limits.MaxProcesses).
			Set("generation_command_gpu_limit", command.Limits.MaxGPUProcesses).
			Set("generation_command_threads", command.Limits.Threads)
		private = append(private, command.PrivateValues...)
		if intel == nil && selected == "software" && command.Started {
			actual = "software"
		}
	}
	// Retain the operation/cause chain independently of process output.
	failure := diagnostics.Split(err)
	cause := err.Error()
	if len(failure.Entries) > 0 {
		cause = failure.Entries[0].Cause
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		private = append(private, pathErr.Path)
	}
	causeResult := diagnostics.Summary(cause, private, 4096)
	properties.Set("operation_cause", causeResult.Value).Set("diagnostic_omitted_bytes", causeResult.OmittedBytes)
	message := causeResult.Value
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		properties.Set("ffmpeg_exit_code", exitErr.ExitCode())
		if len(exitErr.Stderr) > 0 {
			output, omittedRecords := diagnosticStderrDetails(exitErr.Stderr)
			properties.Set("ffmpeg_metadata_omitted_records", omittedRecords)
			for _, line := range strings.Split(string(exitErr.Stderr), "\n") {
				for prefix, key := range map[string]string{"ffmpeg version": "ffmpeg_version", "built with": "ffmpeg_build", "configuration:": "ffmpeg_configuration"} {
					if strings.HasPrefix(strings.TrimSpace(line), prefix) {
						properties.Set(key, diagnostics.Safe(line, private))
					}
				}
			}
			safeOutput := diagnostics.Sanitize(output, private, 8192)
			properties.Set("ffmpeg_output", safeOutput.Value).Set("ffmpeg_output_omitted_bytes", safeOutput.OmittedBytes).Set("ffmpeg_stderr_bytes", len(exitErr.Stderr))
			message += "\n" + safeOutput.Value
		}
	}
	message = diagnostics.Safe(message, private)
	if status := vaStatus.FindStringSubmatch(message); len(status) > 1 {
		if value, parseErr := strconv.ParseInt(status[1], 0, 64); parseErr == nil {
			properties.Set("va_status", value)
		}
	}
	if code := hwaccelCode.FindStringSubmatch(message); len(code) > 1 {
		if value, parseErr := strconv.Atoi(code[1]); parseErr == nil {
			properties.Set("hwaccel_error_code", value)
		}
	}
	if stage == "generation" {
		stage = technicalFailureStage(message)
	}
	properties.Set("generation_selected_backend", selected).Set("generation_actual_backend", actual).Set("generation_stage", stage)
	if command == nil {
		properties.Set("generation_admitted_slots", 0)
	}
	// This is a handled external process error, not an application panic. An
	// invented stack at the capture function would obscure its real origin.
	handled, synthetic := true, false
	event := posthog.Exception{Timestamp: time.Now(), DistinctId: "server", Properties: properties,
		ExceptionList: []posthog.ExceptionItem{{Type: "GenerationError", Value: message,
			Mechanism: &posthog.ExceptionMechanism{Handled: &handled, Synthetic: &synthetic}}}}
	if command != nil && command.NativeStack.Trace != nil {
		snapshot := command.NativeStack.Copy()
		event.ExceptionList[0].Stacktrace = snapshot.Trace
		event.DebugImages = snapshot.Images
		event.Properties.Set("stack_origin", "process_failure")
		sanitizeStackImages(&event)
	}
	return event
}

var vaStatus = regexp.MustCompile(`(?i)(?:VA_STATUS|va(?:api)? (?:status|error)|failed to (?:end picture|create (?:decode )?(?:configuration|context|surface)))[^\n]*?[:= ](0x[0-9a-f]+|[0-9]+)(?:\b|:)`)
var hwaccelCode = regexp.MustCompile(`(?i)hwaccel initiali[sz]ation returned error (-?[0-9]+)\b`)

func safeBackend(value string) string {
	if value == "" {
		return "unknown"
	}
	return diagnostics.Safe(value, nil)
}
func safeWorkload(value string) string {
	if value == "" {
		return "generation"
	}
	return diagnostics.Safe(value, nil)
}
func safeStage(value string) string {
	if value == "" {
		return "generation"
	}
	return diagnostics.Safe(value, nil)
}

func technicalFailureStage(message string) string {
	text := strings.ToLower(message)
	switch {
	case strings.Contains(text, "encoder"), strings.Contains(text, "encoding"), strings.Contains(text, "picture encode issue"):
		return "encode"
	case strings.Contains(text, "decod"), strings.Contains(text, "reference frame"):
		return "decode"
	case strings.Contains(text, "filter"), strings.Contains(text, "vpp"):
		return "filter"
	default:
		return "generation"
	}
}

// Drop content-bearing metadata tags and input/output locations; retain versions,
// technical stream descriptors, mappings and driver messages.
var metadataLine = regexp.MustCompile(`(?i)^\s*(?:title|artist|album|comment|description|synopsis|author|copyright|creation_time|location|handler_name|filename|encoder)\s*:`)
var mediaLocationLine = regexp.MustCompile(`^\s*(?:Input|Output) #`)

func diagnosticStderr(stderr []byte) string {
	value, _ := diagnosticStderrDetails(stderr)
	return value
}
func diagnosticStderrDetails(stderr []byte) (string, int) {
	lines := []string{}
	omitted := 0
	metadataIndent := -1
	for _, line := range strings.Split(string(stderr), "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if trimmed == "Metadata:" {
			metadataIndent = indent
			omitted++
			continue
		}
		if metadataIndent >= 0 {
			if indent > metadataIndent && strings.Contains(trimmed, ":") && !strings.HasPrefix(trimmed, "[") {
				omitted++
				continue
			}
			if trimmed != "" {
				metadataIndent = -1
			}
		}
		if metadataLine.MatchString(line) || mediaLocationLine.MatchString(line) {
			omitted++
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), omitted
}

func sanitizeTechnicalMessage(message string, private []string) string {
	value := diagnostics.Sanitize(message, private, 4096).Value
	if value == "" {
		return "FFmpeg generation failed (no stderr diagnostic)"
	}
	return value
}
func addSourceProperties(p posthog.Properties, s ffmpeg.IntelSource) {
	for key, value := range map[string]string{"codec": s.Codec, "profile": s.Profile, "pixel_format": s.PixelFormat, "color_transfer": s.ColorTransfer, "color_primaries": s.ColorPrimaries, "color_space": s.ColorSpace, "color_range": s.ColorRange, "frame_rate": s.FrameRate, "average_frame_rate": s.AverageFrameRate, "sample_aspect_ratio": s.SampleAspectRatio, "display_aspect_ratio": s.DisplayAspectRatio, "start_time": s.StartTime, "duration": s.Duration, "runtime_fingerprint": s.RuntimeFingerprint} {
		if value != "" {
			p.Set("source_"+key, diagnostics.Safe(value, nil))
		}
	}
	p.Set("source_rotation", s.Rotation).Set("source_stream_index", s.StreamIndex).Set("source_bit_depth", s.BitDepth).Set("source_is_rgb", s.IsRGB)
	if s.DisplayMatrix != nil {
		p.Set("source_display_matrix", s.DisplayMatrix)
	}
	p.Set("source_metadata_candidate_count", len(s.MetadataCandidates))
	if len(s.MetadataCandidates) > 0 {
		var candidates []map[string]any
		for index, candidate := range s.MetadataCandidates {
			if index >= 16 {
				break
			}
			candidate.MetadataCandidates = nil
			properties := posthog.NewProperties()
			addSourceProperties(properties, candidate)
			candidates = append(candidates, map[string]any(properties))
		}
		p.Set("source_metadata_candidates", candidates).Set("source_metadata_candidates_omitted", max(0, len(s.MetadataCandidates)-16))
	}

	if s.MetadataError != nil {
		p.Set("source_metadata_error", diagnostics.Safe(s.MetadataError.Error(), nil))
	}
}
