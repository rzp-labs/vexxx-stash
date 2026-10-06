package analytics

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
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
// Joined marker/output failures are captured individually; job summary errors
// have no second capture hook. Cancellation never becomes an exception.
func CaptureGenerationFailure(ctx context.Context, err error, info GenerationFailureContext) {
	if client == nil || err == nil || ctx.Err() != nil {
		return
	}
	seen := make(map[error]bool)
	var capture func(error)
	capture = func(err error) {
		if err == nil {
			return
		}
		// An ordinary fmt wrapper around errors.Join must not collapse distinct
		// failures, or discard them because a sibling is cancellation.
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				capture(child)
			}
			return
		}
		if child := errors.Unwrap(err); child != nil && containsJoinedError(child) {
			capture(child)
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		// Errors may be implemented by non-comparable values. Deduplicate pointer
		// instances only, rather than comparing arbitrary application errors.
		if comparableError(err) {
			if seen[err] {
				return
			}
			seen[err] = true
		}
		_ = client.Enqueue(GenerationException(err, info))
	}
	capture(err)
}

func containsJoinedError(err error) bool {
	for err != nil {
		if _, ok := err.(interface{ Unwrap() []error }); ok {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func comparableError(err error) bool { return reflect.TypeOf(err).Comparable() }

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
	if errors.As(err, &intel) {
		selected, actual = safeBackend(intel.Diagnostic.Selected), safeBackend(intel.Diagnostic.Actual)
		stage = safeStage(intel.Diagnostic.Stage)
		if intel.Source.Width > 0 && intel.Source.Height > 0 {
			properties.Set("source_width", intel.Source.Width).Set("source_height", intel.Source.Height)
		}
		if technicalIdentifier.MatchString(intel.Source.Codec) {
			properties.Set("source_codec", intel.Source.Codec)
		}
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
	// Prefer stderr over the local wrapper, whose command can dwarf diagnostics
	// and includes private paths. Start/filesystem failures retain their OS cause.
	message := err.Error()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		properties.Set("ffmpeg_exit_code", exitErr.ExitCode())
		message = exitErr.Error()
		if len(exitErr.Stderr) > 0 {
			message = diagnosticStderr(exitErr.Stderr)
		}
	} else {
		if command != nil && strings.HasPrefix(command.Err.Error(), "ffmpeg command produced no output:") {
			message = "ffmpeg command produced no output"
		}
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			private = append(private, pathErr.Path)
			message = pathErr.Op + ": " + pathErr.Err.Error()
		}
	}
	message = sanitizeTechnicalMessage(message, private)
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
	return posthog.Exception{Timestamp: time.Now(), DistinctId: "server", Properties: properties,
		ExceptionList: []posthog.ExceptionItem{{Type: "GenerationError", Value: message,
			Mechanism: &posthog.ExceptionMechanism{Handled: &handled, Synthetic: &synthetic}}}}
}

var technicalIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_]{1,40}$`)
var vaStatus = regexp.MustCompile(`(?i)(?:VA_STATUS|va(?:api)? (?:status|error)|failed to (?:end picture|create (?:decode )?(?:configuration|context|surface)))[^\n]*?[:= ](0x[0-9a-f]+|[0-9]+)(?:\b|:)`)
var hwaccelCode = regexp.MustCompile(`(?i)hwaccel initiali[sz]ation returned error (-?[0-9]+)\b`)

func safeBackend(value string) string {
	switch value {
	case "vaapi", "qsv", "software", "none":
		return value
	default:
		return "unknown"
	}
}

func safeWorkload(value string) string {
	switch value {
	case "sprite", "preview", "marker", "cover", "transcode", "phash", "image_phash", "image_preview", "image_thumbnail", "gallery", "clip_preview", "interactive_heatmap":
		return value
	default:
		return "generation"
	}
}

func safeStage(value string) string {
	switch value {
	case "device", "metadata", "eligibility", "plan", "backend", "API", "quality", "webp", "decode", "filter", "encode", "output", "generation":
		return value
	default:
		return "generation"
	}
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

var metadataLine = regexp.MustCompile(`^\s*(?:[A-Za-z][A-Za-z0-9 _.-]*\s+:|Metadata:|Input #|Output #|Duration:|Stream mapping:|ffmpeg version|built with|configuration:|libav\w+\s+\d|frame=|size=)`)

func diagnosticStderr(stderr []byte) string {
	// Bound work to complete diagnostic lines at the tail, where FFmpeg reports
	// the final cause. Never slice through a credential/path-bearing record.
	const maxInputBytes = 64 * 1024
	if len(stderr) > maxInputBytes {
		stderr = stderr[len(stderr)-maxInputBytes:]
		if newline := bytes.IndexByte(stderr, '\n'); newline >= 0 {
			stderr = stderr[newline+1:]
		} else {
			return "FFmpeg diagnostic record exceeded 64 KiB [record omitted]"
		}
	}
	// Media/container metadata is not technical failure text. Remove those
	// data-bearing records while keeping FFmpeg/driver diagnostics verbatim.
	lines := []string{}
	for _, line := range strings.Split(string(stderr), "\n") {
		if metadataLine.MatchString(line) || strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

var credentials = regexp.MustCompile(`(?i)\b(?:authorization\s*:\s*(?:bearer|basic)\s+[^\s,;]+|(?:password|passwd|pwd|token|api[_-]?key|access[_-]?token|secret|cookie)\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+))`)
var urls = regexp.MustCompile(`(?i)\b(?:https?|rtsp|rtmp|ftp|s3)://[^\s'"<>]+`)
var quotedPath = regexp.MustCompile(`(?:"(?:[A-Za-z]:[\\/]|/|\\\\)[^"]*"|'(?:[A-Za-z]:[\\/]|/|\\\\)[^']*')`)
var pathStart = regexp.MustCompile(`(^|[\s=:"'(\[])((?:[A-Za-z]:[\\/]|\\\\|/))`)
var pathErrorSuffix = regexp.MustCompile(`(?i): (?:permission denied|no such file or directory|input/output error|cannot allocate memory|invalid argument|operation not permitted|read-only file system|no space left on device|file exists|is a directory|not a directory)[.!]?$`)
var mediaFilename = regexp.MustCompile(`(?i)(?:[A-Za-z0-9_. -]+\.(?:mp4|mkv|avi|mov|webm|jpg|jpeg|png|webp|vtt|m3u8|ts|ffconcat))\b`)
var emailAddress = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)

func sanitizeTechnicalMessage(message string, private []string) string {
	for _, value := range private {
		if value != "" && value != "-" {
			message = strings.ReplaceAll(message, value, "[path redacted]")
		}
	}
	message = credentials.ReplaceAllString(message, "[credential redacted]")
	message = urls.ReplaceAllString(message, "[URL redacted]")
	message = quotedPath.ReplaceAllString(message, "[path redacted]")
	message = redactUnquotedPaths(message)
	message = mediaFilename.ReplaceAllString(message, "[media filename redacted]")
	message = emailAddress.ReplaceAllString(message, "[identity redacted]")
	message = strings.ToValidUTF8(message, "?")
	message = strings.TrimSpace(message)
	const maxMessageBytes = 4096
	if len(message) > maxMessageBytes {
		// Preserve the tail containing the final failure, after sanitization. Never
		// truncate raw credentials or paths into fragments that evade redaction.
		message = message[len(message)-(maxMessageBytes-len("[truncated]\n")):]
		for len(message) > 0 && !utf8.RuneStart(message[0]) {
			message = message[1:]
		}
		message = "[truncated]\n" + message
	}
	if message == "" {
		return "FFmpeg generation failed (no stderr diagnostic)"
	}
	return message
}

var ffmpegDevice = regexp.MustCompile(`^/dev/dri/renderD[0-9]+$`)

// An unquoted auxiliary path can contain spaces and need not be a command input.
// Its endpoint is ambiguous, so redact the remainder of that diagnostic line,
// retaining only a recognized terminal OS error. Never join adjacent records.
func redactUnquotedPaths(message string) string {
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		offset := 0
		for offset < len(line) {
			match := pathStart.FindStringSubmatchIndex(line[offset:])
			if match == nil {
				break
			}
			start := offset + match[4]
			end := start + strings.IndexAny(line[start:], " \t\r\"'<>[](),;")
			if end < start {
				end = len(line)
			}
			if ffmpegDevice.MatchString(line[start:end]) {
				offset = end
				continue
			}
			suffix := ""
			if errorSpan := pathErrorSuffix.FindStringIndex(line[start:]); errorSpan != nil {
				suffix = line[start+errorSpan[0]:]
			}
			lines[i] = line[:start] + "[path redacted]" + suffix
			break
		}
	}
	return strings.Join(lines, "\n")
}
