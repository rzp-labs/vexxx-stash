package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/stashapp/stash/internal/analytics"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func diagnosticAPIContext() context.Context {
	ctx := graphql.WithOperationContext(context.Background(), &graphql.OperationContext{RawQuery: "private query text", Operation: &ast.OperationDefinition{Operation: ast.Mutation, Name: "private operation name"}})
	return graphql.WithFieldContext(ctx, &graphql.FieldContext{Field: graphql.CollectedField{Field: &ast.Field{Name: "sceneUpdate", Alias: "JaneSmithPrivateMedia"}}, Args: map[string]any{"input": map[string]any{"title": "private scene title", "password": "private credential"}}})
}
func TestGraphQLCapturesOriginalSafeContextAndExactlyOnceRecovery(t *testing.T) {
	var mu sync.Mutex
	var events []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batch []map[string]any `json:"batch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		events = append(events, body.Batch...)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":1}`)
	}))
	defer server.Close()
	t.Setenv("POSTHOG_PROJECT_TOKEN", "synthetic-api-project")
	t.Setenv("POSTHOG_HOST", server.URL)
	if err := analytics.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = analytics.Close()
		t.Setenv("POSTHOG_PROJECT_TOKEN", "")
		t.Setenv("POSTHOG_HOST", "")
		_ = analytics.Initialize()
	})
	ctx := graphql.WithResponseContext(diagnosticAPIContext(), gqlErrorHandler, recoverGraphQL)
	cause := fmt.Errorf("database save failed for private scene title: %w", errors.New("unfamiliar-driver-sentinel password='private credential'"))
	// AddError calls ErrorOnPath before the registered presenter, just as gqlgen
	// resolver execution does. Keep a genuine wrapper around that decoration too.
	decorated := fmt.Errorf("operation wrapper: %w: operation suffix", graphql.ErrorOnPath(ctx, cause))
	response := frameworkErrorResponse(ctx, decorated)
	if response.Message != "an internal error occurred" || response.Extensions["telemetry_captured"] != true {
		t.Fatal("response/capture marker incorrect", response)
	}
	if uuid.Validate(response.Extensions["telemetry_event_id"].(string)) != nil {
		t.Fatal("invalid correlation UUID")
	}
	recovered := graphql.Recover(ctx, "unknown panic sentinel private scene title Cookie: sid=opaque-session")
	panicResponse := frameworkErrorResponse(ctx, recovered)
	if panicResponse.Message != "an internal error occurred" || panicResponse.Extensions["telemetry_captured"] != true {
		t.Fatal("recovery not marked")
	}
	// Unmarked cancellation in a mixed response stays unmarked.
	canceled := frameworkErrorResponse(ctx, context.Canceled)
	if canceled.Extensions["telemetry_captured"] != nil {
		t.Fatal("cancellation marked")
	}
	explicit := frameworkErrorResponse(ctx, &gqlerror.Error{Message: "unfamiliar explicit resolver diagnostic"})
	if explicit.Message != "unfamiliar explicit resolver diagnostic" || explicit.Extensions["telemetry_captured"] != true || response.Path.String() != "JaneSmithPrivateMedia" {
		t.Fatal("framework response or marker changed")
	}
	if err := analytics.Client().Flush(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	data, _ := json.Marshal(events)
	count := len(events)
	mu.Unlock()
	if count != 3 {
		t.Fatalf("ordinary + recovery + explicit message count=%d", count)
	}
	for _, private := range []string{"private scene title", "private credential", "JaneSmithPrivateMedia", "private operation name", "private query text", "opaque-session"} {
		if strings.Contains(string(data), private) {
			t.Errorf("private API fixture leaked %s", private)
		}
	}
	for _, retained := range []string{"operation wrapper", "operation suffix", "unfamiliar explicit resolver diagnostic", "database save failed", "unfamiliar-driver-sentinel", "unknown panic sentinel", "sceneUpdate", "mutation", "INTERNAL_ERROR", "$exception_list"} {
		if !strings.Contains(string(data), retained) {
			t.Errorf("diagnostic lost %s", retained)
		}
	}
	if dir := os.Getenv("VEX80_EVIDENCE_DIR"); dir != "" && !t.Failed() {
		if err := os.WriteFile(dir+"/"+t.Name()+"-framework-events.json", data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Enqueue rejected by the closed SDK must not tell the browser to suppress.
	_ = analytics.Close()
	failed := frameworkErrorResponse(ctx, errors.New("fresh failure"))
	if failed.Extensions["telemetry_captured"] != nil || failed.Extensions["telemetry_event_id"] != nil {
		t.Fatal("enqueue failure suppressed client")
	}
	recoveredFailed := frameworkErrorResponse(ctx, graphql.Recover(ctx, "fresh panic"))
	if recoveredFailed.Extensions["telemetry_captured"] != nil {
		t.Fatal("failed recovery enqueue marked")
	}
}
func TestGraphQLMissingClientNeverMarksCapture(t *testing.T) {
	t.Setenv("POSTHOG_PROJECT_TOKEN", "")
	t.Setenv("POSTHOG_HOST", "")
	if err := analytics.Initialize(); err != nil {
		t.Fatal(err)
	}
	ctx := graphql.WithResponseContext(diagnosticAPIContext(), gqlErrorHandler, recoverGraphQL)
	response := frameworkErrorResponse(ctx, &gqlerror.Error{
		Message: "unfamiliar diagnostic",
		Extensions: map[string]interface{}{
			"telemetry_captured": true,
			"telemetry_event_id": uuid.NewString(),
		},
	})
	if response.Extensions["telemetry_captured"] != nil || response.Extensions["telemetry_event_id"] != nil {
		t.Fatal("missing client marked capture")
	}
}

func frameworkErrorResponse(ctx context.Context, err error) *gqlerror.Error {
	graphql.AddError(ctx, err)
	responses := graphql.GetErrors(ctx)
	return responses[len(responses)-1]
}

func TestGraphQLDiagnosticViewPreservesMessagesWrappersAndCauseIdentity(t *testing.T) {
	cause := errors.New("unfamiliar filesystem cause")
	path := &fs.PathError{Op: "open", Path: "/private/media/index", Err: cause}
	decorated := graphql.ErrorOnPath(diagnosticAPIContext(), path)
	wrapped := fmt.Errorf("genuine operation: %w: genuine suffix", decorated)
	annotated := &gqlerror.Error{Message: "driver annotation", Err: wrapped, Path: ast.Path{ast.PathName("JaneSmithPrivateMedia")}}
	diagnostic := graphQLDiagnosticError(annotated)
	for _, part := range []string{"driver annotation", "genuine operation", "genuine suffix", "unfamiliar filesystem cause"} {
		if !strings.Contains(diagnostic.Error(), part) {
			t.Errorf("lost diagnostic %s", part)
		}
	}
	if strings.Contains(diagnostic.Error(), "JaneSmithPrivateMedia") {
		t.Fatal("response path decoration retained")
	}
	var retained *fs.PathError
	if !errors.As(diagnostic, &retained) || retained != path || !errors.Is(diagnostic, cause) {
		t.Fatal("underlying cause identity changed")
	}
}
