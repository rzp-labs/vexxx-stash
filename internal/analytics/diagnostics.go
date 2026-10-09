package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/logger"
)

var deliveryAccepted, deliverySucceeded, deliveryFailed, enqueueFailed, sdkWarnings atomic.Uint64
var lastDeliveryWarning, lastEnqueueWarning, lastSDKWarning, lastLogWarning atomic.Int64
var credentialKeyNormalizer = strings.NewReplacer("_", "", "-", "", ".", "", " ", "")

// Delivery losses are counters, not exceptions: callbacks never recursively enqueue.
func deliveryWarning(category string) {
	limiter := &lastDeliveryWarning
	switch category {
	case "enqueue":
		limiter = &lastEnqueueWarning
	case "sdk":
		limiter = &lastSDKWarning
	case "logs":
		limiter = &lastLogWarning
	}
	now := time.Now().Unix()
	last := limiter.Load()
	if now-last >= 60 && limiter.CompareAndSwap(last, now) {
		logger.Warnf("telemetry %s warning: enqueue failures=%d discarded=%d SDK warnings=%d log export failures=%d", category, enqueueFailed.Load(), deliveryFailed.Load(), sdkWarnings.Load(), logExportFailures.Load())
	}
}

type deliveryCallback struct{}

func (deliveryCallback) Success(posthog.APIMessage) { deliverySucceeded.Add(1) }
func (deliveryCallback) Failure(posthog.APIMessage, error) {
	deliveryFailed.Add(1)
	deliveryWarning("discard")
}

type deliveryLogger struct{}

func (deliveryLogger) Debugf(string, ...interface{}) {}
func (deliveryLogger) Logf(string, ...interface{})   {}
func (deliveryLogger) Warnf(string, ...interface{})  { sdkWarnings.Add(1); deliveryWarning("sdk") }
func (deliveryLogger) Errorf(string, ...interface{}) { sdkWarnings.Add(1); deliveryWarning("sdk") }

type observedClient struct{ posthog.Client }

func (c observedClient) Enqueue(m posthog.Message) error {
	err := c.Client.Enqueue(m)
	if err != nil {
		enqueueFailed.Add(1)
		deliveryWarning("enqueue")
	} else {
		deliveryAccepted.Add(1)
	}
	return err
}

// The hook is defense in depth for all application events. SDK stack/image fields
// are kept typed, so native addresses, debug IDs and symbolication aren't lost.
func beforeSend(message posthog.Message) posthog.Message {
	switch event := message.(type) {
	case posthog.Exception:
		event.Properties = safeProperties(event.Properties)
		for i := range event.ExceptionList {
			event.ExceptionList[i].Value = diagnostics.Safe(event.ExceptionList[i].Value, nil)
			event.ExceptionList[i].Type = diagnostics.Safe(event.ExceptionList[i].Type, nil)
		}
		sanitizeStackImages(&event)
		return event
	case posthog.Capture:
		event.Properties = safeProperties(event.Properties)
		return event
	case posthog.Identify:
		event.Properties = safeProperties(event.Properties)
		return event
	case posthog.GroupIdentify:
		event.Properties = safeProperties(event.Properties)
		return event
	default:
		return message
	}
}
func sanitizeStackImages(event *posthog.Exception) {
	for i := range event.ExceptionList {
		if stack := event.ExceptionList[i].Stacktrace; stack != nil {
			for j := range stack.Frames {
				stack.Frames[j].Filename = diagnosticSourcePath(stack.Frames[j].Filename)
			}
		}
	}
	for i := range event.DebugImages {
		event.DebugImages[i].CodeFile = diagnosticBinaryPath(event.DebugImages[i].CodeFile)
	}
}
func safeProperties(properties posthog.Properties) posthog.Properties {
	result := posthog.NewProperties()
	nodes, bytes, omitted := 0, 0, 0
	var safe func(string, any, int) any
	safe = func(key string, value any, depth int) any {
		nodes++
		if nodes > 4096 || bytes > 128*1024 || depth > 16 {
			omitted++
			return "[diagnostic omitted]"
		}
		normalized := strings.ToLower(strings.TrimPrefix(key, "$"))
		credentialKey := credentialKeyNormalizer.Replace(normalized)
		switch credentialKey {
		case "password", "passwd", "pwd", "token", "apikey", "accesstoken", "refreshtoken", "sessiontoken", "clientsecret", "secretaccesskey", "awssecretaccesskey", "secret", "credential", "privatekey", "authorization", "proxyauthorization", "cookie", "setcookie", "connectionkey", "handykey":
			return "[credential redacted]"
		}
		switch normalized {
		case "password", "passwd", "token", "api_key", "authorization", "cookie", "set-cookie", "query", "variables", "args", "arguments", "request", "response", "body", "scene_title", "performer_name", "media_title", "title", "username", "user_name", "email", "description", "comment", "remoteaddr", "url", "uri":
			return "[private content redacted]"
		}
		switch v := value.(type) {
		case string:
			r := diagnostics.Sanitize(v, nil, diagnostics.MaxTextBytes)
			bytes += len(r.Value)
			omitted += r.OmittedBytes
			return r.Value
		case posthog.Properties:
			return safe(key, map[string]any(v), depth)
		case map[string]any:
			m := map[string]any{}
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			priority := func(k string) int {
				switch k {
				case "failure_origin", "job_correlation", "package_id", "package_operation", "package_stage", "package_error_cause", "package_failure_count", "package_failures_omitted", "python_exit_code", "python_module", "diagnostic_omitted_bytes", "error_causes", "package_failure_summaries":
					return 0
				}
				return 1
			}
			sort.SliceStable(keys, func(i, j int) bool { return priority(keys[i]) < priority(keys[j]) })
			// Keep causes before verbose output when the overall budget is exhausted.
			sort.SliceStable(keys, func(i, j int) bool {
				return !strings.Contains(keys[i], "output") && strings.Contains(keys[j], "output")
			})
			for index, k := range keys {
				if index >= 256 || nodes >= 4096 || bytes >= 128*1024 {
					omitted += len(keys) - index
					break
				}
				safeKey := diagnostics.Sanitize(k, nil, 256).Value
				bytes += len(safeKey)
				m[safeKey] = safe(k, v[k], depth+1)
			}
			return m
		case []map[string]any:
			items := make([]any, 0, min(len(v), 256))
			for i, item := range v {
				if i >= 256 || nodes >= 4096 || bytes >= 128*1024 {
					omitted += len(v) - i
					break
				}
				items = append(items, safe(key, item, depth+1))
			}
			return items
		case []any:
			items := make([]any, 0, min(len(v), 256))
			for i, item := range v {
				if i >= 256 || nodes >= 4096 || bytes >= 128*1024 {
					omitted += len(v) - i
					break
				}
				items = append(items, safe(key, item, depth+1))
			}
			return items
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				omitted++
				return "[non-finite diagnostic]"
			}
			return v
		case float32:
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				omitted++
				return "[non-finite diagnostic]"
			}
			return v

		case time.Time:
			return v.UTC().Format(time.RFC3339Nano)
		case nil, bool, int, int32, int64, uint, uint64, json.Number:
			return v
		default:
			rv := reflect.ValueOf(v)
			switch rv.Kind() {
			case reflect.String:
				return safe(key, rv.String(), depth+1)
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				return rv.Int()
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				return rv.Uint()
			case reflect.Map:
				if rv.Type().Key().Kind() != reflect.String {
					omitted++
					return "[unsupported diagnostic omitted]"
				}
				m := map[string]any{}
				iter := rv.MapRange()
				count := 0
				for iter.Next() {
					if count >= 256 || nodes >= 4096 || bytes >= 128*1024 {
						omitted += rv.Len() - count
						break
					}
					keyName := iter.Key().String()
					safeKey := diagnostics.Sanitize(keyName, nil, 256).Value
					bytes += len(safeKey)
					m[safeKey] = safe(keyName, iter.Value().Interface(), depth+1)
					count++
				}
				return m
			case reflect.Pointer:
				if rv.IsNil() {
					return nil
				}
				return safe(key, rv.Elem().Interface(), depth+1)
			case reflect.Slice, reflect.Array:
				if rv.Type().Elem().Kind() == reflect.Uint8 {
					omitted++
					return "[binary diagnostic omitted]"
				}
				items := make([]any, 0, min(rv.Len(), 256))
				for i := 0; i < rv.Len(); i++ {
					if i >= 256 || nodes >= 4096 || bytes >= 128*1024 {
						omitted += rv.Len() - i
						break
					}
					items = append(items, safe(key, rv.Index(i).Interface(), depth+1))
				}
				return items
			case reflect.Struct:
				m := map[string]any{}
				for i := 0; i < rv.NumField() && i < 128; i++ {
					field := rv.Type().Field(i)
					if !field.IsExported() {
						continue
					}
					name := strings.Split(field.Tag.Get("json"), ",")[0]
					if name == "-" {
						continue
					}
					if name == "" {
						name = field.Name
					}
					m[name] = safe(name, rv.Field(i).Interface(), depth+1)
				}
				return m
			}
			omitted++
			return "[unsupported diagnostic omitted]"
		}
	}
	cleaned := safe("", map[string]any(properties), 0).(map[string]any)
	for key, value := range cleaned {
		result[key] = value
	}
	result.Set("telemetry_delivery_accepted", deliveryAccepted.Load()).Set("telemetry_delivery_succeeded", deliverySucceeded.Load()).Set("telemetry_delivery_discarded", deliveryFailed.Load()).Set("telemetry_enqueue_failed", enqueueFailed.Load()).Set("telemetry_sdk_warnings", sdkWarnings.Load()).Set("telemetry_log_export_failed", logExportFailures.Load())
	if omitted > 0 {
		result.Set("diagnostics_omitted_units", omitted)
	}
	return result
}

func shouldCapture(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	for _, entry := range diagnostics.Split(err).Entries {
		if errors.Is(entry.Err, context.Canceled) || (ctx.Err() != nil && errors.Is(entry.Err, ctx.Err())) {
			continue
		}
		var exit *exec.ExitError
		if ctx.Err() != nil && errors.As(entry.Err, &exit) && exit.ProcessState != nil && exit.ExitCode() == -1 {
			continue
		}
		return true
	}
	return false
}
func captureException(ctx context.Context, err error, event posthog.Exception) string {
	if client == nil || !shouldCapture(ctx, err) || diagnostics.Reported(ctx, err) {
		return ""
	}
	claimed, finish := diagnostics.Claim(ctx, err)
	if !claimed {
		return ""
	}
	event.Uuid = uuid.NewString()
	if client.Enqueue(event) != nil {
		finish(false)
		return ""
	}
	finish(true)
	return event.Uuid
}

// Ordinary failures get their catch-site stack explicitly labeled. Saved failure
// stacks on specialized reports remain the primary origin stack.
func FailureException(err error, origin string, properties posthog.Properties, private ...[]string) posthog.Exception {
	privateValues := diagnostics.ErrorPrivate(err)
	for _, v := range private {
		privateValues = append(privateValues, v...)
	}
	failures := diagnostics.Split(err)
	var summaries []string
	omittedCauseBytes := 0
	for _, entry := range failures.Entries {
		cause := diagnostics.Summary(entry.Cause, privateValues, 1024)
		summaries = append(summaries, cause.Value)
		omittedCauseBytes += cause.OmittedBytes
	}
	headline := "operation failed"
	if len(failures.Entries) > 0 {
		headline = failures.Entries[0].Cause
	}
	text := diagnostics.Summary(headline, privateValues, diagnostics.MaxTextBytes)
	if text.Value == "" {
		text.Value = "operation failed"
	}
	event := posthog.NewDefaultException(time.Now(), "server", "OperationError", text.Value)
	event.Properties = ReleaseProperties().Set("$process_person_profile", false).Set("$exception_level", "error").Set("failure_origin", origin).Set("failure_count", failures.Count).Set("failures_omitted", failures.Omitted).Set("diagnostic_omitted_bytes", text.OmittedBytes).Set("stack_origin", "capture_boundary").Set("error_causes", summaries).Set("cause_omitted_bytes", omittedCauseBytes)
	for key, value := range properties {
		event.Properties.Set(key, value)
	}
	handled, synthetic := true, false
	event.ExceptionList[0].Mechanism = &posthog.ExceptionMechanism{Handled: &handled, Synthetic: &synthetic}
	sanitizeStackImages(&event)
	return event
}
func CaptureJobFailure(ctx context.Context, err error, correlation, kind string) {
	if client == nil {
		return
	}
	for _, entry := range diagnostics.Split(err).Entries {
		if !shouldCapture(ctx, entry.Err) || diagnostics.Reported(ctx, entry.Err) {
			continue
		}
		event := FailureException(entry.Err, "job", posthog.NewProperties().Set("job_correlation", safeCorrelation(correlation)).Set("job_type", kind), diagnostics.Private(ctx))
		// Entry cause already contains its wrappers: don't duplicate them in Value.
		private := append(diagnostics.ErrorPrivate(entry.Err), diagnostics.Private(ctx)...)
		event.ExceptionList[0].Value = diagnostics.Safe(entry.Cause, private)
		captureException(ctx, entry.Err, event)
	}
}
func CaptureAPIFailure(ctx context.Context, err error, field, operation, code string) string {
	if client == nil || !shouldCapture(ctx, err) {
		return ""
	}
	event := FailureException(err, "graphql", posthog.NewProperties().Set("graphql_field", field).Set("graphql_operation_type", operation).Set("graphql_error_code", code), diagnostics.Private(ctx))
	return captureException(ctx, err, event)
}
func CaptureAPIPanic(ctx context.Context, value any, field, operation string) string {
	if client == nil {
		return ""
	}
	event := panicException(value, diagnostics.Private(ctx))
	event.Uuid = uuid.NewString()
	handled := true
	event.ExceptionList[0].Mechanism.Handled = &handled
	event.Properties.Set("$exception_level", "error").Set("failure_origin", "graphql_recovery").Set("graphql_field", field).Set("graphql_operation_type", operation)
	if client.Enqueue(event) != nil {
		return ""
	}
	return event.Uuid
}
