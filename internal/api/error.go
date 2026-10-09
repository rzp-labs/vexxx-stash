package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// isInternalError determines if an error is an internal error that should be
// sanitized before being returned to the client. Internal errors include
// database errors, filesystem errors, and connection errors that may leak
// implementation details such as file paths, table names, or SQL statements.
func isInternalError(err error) bool {
	if err == nil {
		return false
	}

	var recovered *recoveredGraphQLError
	if errors.As(err, &recovered) {
		return true
	}

	// Database errors
	if errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) {
		return true
	}

	// Check error message patterns that indicate internal errors
	msg := err.Error()
	internalPatterns := []string{
		"sql:",
		"database",
		"SQLITE",
		"sqlite",
		"no such table",
		"constraint failed",
		"UNIQUE constraint",
		"FOREIGN KEY constraint",
		"open ",  // filesystem open errors
		"read ",  // filesystem read errors
		"write ", // filesystem write errors
		"permission denied",
		"connection refused",
		"dial tcp",
		"i/o timeout",
	}

	for _, pattern := range internalPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}

	return false
}

// errorCode returns a stable error code string for categorizing errors
// in GraphQL error extensions. Clients can use these codes for programmatic
// error handling.
func errorCode(err error) string {
	if errors.Is(err, ErrNotAuthenticated) {
		return "UNAUTHENTICATED"
	}
	if errors.Is(err, ErrNotAuthorized) {
		return "FORBIDDEN"
	}
	if errors.Is(err, ErrNotSupported) {
		return "NOT_SUPPORTED"
	}
	if errors.Is(err, ErrInput) {
		return "BAD_INPUT"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELLED"
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "NOT_FOUND"
	}
	if isInternalError(err) {
		return "INTERNAL_ERROR"
	}
	return "UNKNOWN"
}

// sanitizeErrorMessage returns a user-safe error message. Internal errors
// are replaced with a generic message to avoid leaking implementation details
// like SQL statements, file paths, or connection strings.
func sanitizeErrorMessage(err error) string {
	if errors.Is(err, ErrNotAuthenticated) {
		return "authentication required"
	}
	if errors.Is(err, ErrNotAuthorized) {
		return "insufficient permissions"
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "not found"
	}
	if isInternalError(err) {
		return "an internal error occurred"
	}
	// For non-internal errors, return the original message
	// (these are typically user-facing validation errors, input errors, etc.)
	return err.Error()
}

func gqlErrorHandler(ctx context.Context, e error) *gqlerror.Error {
	ctx = graphQLPrivateContext(ctx)
	diagnostic := graphQLRequestDiagnostic(ctx, e)
	if !errors.Is(ctx.Err(), context.Canceled) {
		if fc := graphql.GetFieldContext(ctx); fc != nil {
			field, operation := graphQLDiagnosticContext(ctx)
			logGraphQLDiagnostic(ctx, diagnostic.Error())
			// Argument names are schema-owned. Never serialize caller values,
			// including nested input objects, into the local debug stream.
			logger.DebugFunc(func() (string, []interface{}) {
				args := make(map[string]string, len(fc.Args))
				for name := range fc.Args {
					args[name] = "[argument value redacted]"
				}
				encoded, _ := json.Marshal(args)
				return "%s (%s): arguments %s", []interface{}{field, operation, string(encoded)}
			})
		}
	}

	// Build a sanitized error for the client response
	gqlErr := graphql.DefaultErrorPresenter(ctx, e)

	// Add error code to extensions for programmatic client handling
	code := errorCode(e)
	if gqlErr.Extensions == nil {
		gqlErr.Extensions = make(map[string]interface{})
	}
	// Only this boundary's successful enqueue may mark an error as captured.
	// Discard inherited markers from upstream GraphQL errors.
	delete(gqlErr.Extensions, "telemetry_captured")
	delete(gqlErr.Extensions, "telemetry_event_id")
	gqlErr.Extensions["code"] = code
	eventID := ""
	var recovered *recoveredGraphQLError
	if errors.As(e, &recovered) {
		eventID = recovered.eventID
	} else {
		field, operation := graphQLDiagnosticContext(ctx)
		eventID = analytics.CaptureAPIFailure(ctx, diagnostic, field, operation, code)
	}
	if eventID != "" {
		gqlErr.Extensions["telemetry_captured"] = true
		gqlErr.Extensions["telemetry_event_id"] = eventID
	}

	// Sanitize the message to avoid leaking internal details
	if isInternalError(e) {
		gqlErr.Message = sanitizeErrorMessage(e)
	}

	return gqlErr
}

// gqlgen decorates errors with response paths before presenting them. Use only
// the diagnostic message/cause for capture; the original error still supplies
// the client response and error code. Preserve genuine operation wrappers.
func graphQLDiagnosticError(err error) error {
	clean, _ := withoutGraphQLDecoration(err, 0)
	return clean
}

type graphQLDiagnosticMessage struct {
	message string
	cause   error
}

func (e *graphQLDiagnosticMessage) Error() string { return e.message }
func (e *graphQLDiagnosticMessage) Unwrap() error { return e.cause }

func withoutGraphQLDecoration(err error, depth int) (error, bool) {
	if err == nil {
		return nil, false
	}
	if depth >= 128 {
		return errors.New("[diagnostic depth omitted]"), true
	}
	if gql, ok := err.(*gqlerror.Error); ok {
		child, _ := withoutGraphQLDecoration(gql.Err, depth+1)
		message := gql.Message
		if gql.Err != nil {
			if message == gql.Err.Error() {
				return child, true
			}
			if prefix, suffix, embedded := strings.Cut(message, gql.Err.Error()); embedded {
				message = prefix + child.Error() + suffix
			} else {
				message += ": " + child.Error()
			}
		}
		return &graphQLDiagnosticMessage{message: message, cause: child}, true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		cleaned := make([]error, len(children))
		changed := false
		for i, child := range children {
			var replaced bool
			cleaned[i], replaced = withoutGraphQLDecoration(child, depth+1)
			changed = changed || replaced
		}
		if changed {
			return errors.Join(cleaned...), true
		}
		return err, false
	}
	child := errors.Unwrap(err)
	cleaned, changed := withoutGraphQLDecoration(child, depth+1)
	if !changed {
		return err, false
	}
	message := err.Error()
	if prefix, suffix, embedded := strings.Cut(message, child.Error()); embedded {
		message = prefix + cleaned.Error() + suffix
	}
	return &graphQLDiagnosticMessage{message: message, cause: cleaned}, true
}

// Request input and aliases never become telemetry context.
func graphQLDiagnosticContext(ctx context.Context) (string, string) {
	field, operation := "unknown", "unknown"
	if fc := graphql.GetFieldContext(ctx); fc != nil {
		field = fc.Field.Name
	}
	if graphql.HasOperationContext(ctx) {
		if op := graphql.GetOperationContext(ctx); op != nil && op.Operation != nil {
			operation = string(op.Operation.Operation)
		}
	}
	return field, operation
}

type recoveredGraphQLError struct{ eventID string }

func (*recoveredGraphQLError) Error() string { return "an internal error occurred" }
func recoverGraphQL(ctx context.Context, value interface{}) error {
	ctx = graphQLPrivateContext(ctx)
	logGraphQLDiagnostic(ctx, fmt.Sprint(value))
	debug.PrintStack()
	field, operation := graphQLDiagnosticContext(ctx)
	return &recoveredGraphQLError{eventID: analytics.CaptureAPIPanic(ctx, value, field, operation)}
}

// Local API diagnostics use the same targeted redaction as structured capture.
// Schema field and operation type supply context without exposing response aliases.
func logGraphQLDiagnostic(ctx context.Context, message string) {
	field, operation := graphQLDiagnosticContext(ctx)
	private := make([]string, 0, len(diagnostics.Private(ctx)))
	for _, value := range diagnostics.Private(ctx) {
		// A short password such as "a" must not erase letters in "authenticated".
		// Redact standalone short tokens; retain complete diagnostic words.
		if len(value) > 0 && len(value) <= 3 && graphQLNameOrNumber.MatchString(value) {
			message = regexp.MustCompile(`\b`+regexp.QuoteMeta(value)+`\b`).ReplaceAllString(message, "[argument value redacted]")
		} else {
			private = append(private, value)
		}
	}
	text := diagnostics.Summary(message, private, diagnostics.MaxTextBytes)
	if text.OmittedBytes > 0 {
		logger.Errorf("%s (%s): %s [diagnostic omitted bytes=%d]", field, operation, text.Value, text.OmittedBytes)
		return
	}
	logger.Errorf("%s (%s): %s", field, operation, text.Value)
}

// Argument values are used locally as exact redaction fixtures, never exported.
func graphQLPrivateContext(ctx context.Context) context.Context {
	fc := graphql.GetFieldContext(ctx)
	values := append([]string(nil), diagnostics.Private(ctx)...)
	if graphql.HasOperationContext(ctx) {
		values = append(values, graphql.GetOperationContext(ctx).OperationName)
	}
	if fc == nil {
		return diagnostics.WithPrivate(ctx, values)
	}
	nodes, bytes := 0, 0
	var walk func(reflect.Value, int)
	walk = func(v reflect.Value, depth int) {
		nodes++
		if !v.IsValid() || depth > 8 || nodes > 1024 || bytes > 128*1024 {
			return
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem(), depth+1)
			}
		case reflect.String:
			if v.Len() > 0 {
				values = append(values, v.String())
				bytes += v.Len()
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() {
					walk(v.Field(i), depth+1)
				}
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				walk(iter.Value(), depth+1)
				if nodes > 1024 {
					break
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len() && nodes <= 1024; i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(fc.Args), 0)
	return diagnostics.WithPrivate(ctx, values)
}
