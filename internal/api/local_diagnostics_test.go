package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/stashapp/stash/internal/analytics"
	appLog "github.com/stashapp/stash/internal/log"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/vektah/gqlparser/v2/ast"
)

func localDiagnosticLog(t *testing.T) func() string {
	t.Helper()
	t.Setenv("POSTHOG_PROJECT_TOKEN", "")
	t.Setenv("POSTHOG_HOST", "")
	if err := analytics.Initialize(); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/synthetic-graphql.log"
	local := appLog.NewLogger()
	local.Init(path, false, "debug", 0)
	previous := logger.Logger
	logger.Logger = local
	t.Cleanup(func() { logger.Logger = previous })
	return func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func TestGraphQLPasswordMutationLocalLogPrivacy(t *testing.T) {
	readLog := localDiagnosticLog(t)
	srv := handler.New(NewExecutableSchema(Config{Resolvers: &Resolver{}}))
	srv.AddTransport(transport.POST{})
	srv.SetErrorPresenter(gqlErrorHandler)
	// Actual generated arguments and actual resolver: no user session causes the
	// authorization failure before any password hash/DB operation.
	body, _ := json.Marshal(map[string]any{
		"query":     `mutation SyntheticPasswordProbe($current:String!, $next:String!) { changeOwnPassword(current_password:$current,new_password:$next) }`,
		"variables": map[string]any{"current": "SyntheticCurrentSecret-9182", "next": "SyntheticNextSecret-7364"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"message":"not authenticated"`) || !strings.Contains(rec.Body.String(), `"code":"UNAUTHENTICATED"`) {
		t.Fatal("actual password mutation failure changed", rec.Body.String())
	}
	text := readLog()
	for _, secret := range []string{"SyntheticCurrentSecret-9182", "SyntheticNextSecret-7364"} {
		if strings.Contains(text, secret) {
			t.Error("actual password-mutation argument leaked to local log")
		}
	}
	for _, retained := range []string{"changeOwnPassword", "current_password", "new_password", "not authenticated"} {
		if !strings.Contains(text, retained) {
			t.Errorf("local operation/argument/cause lost %q", retained)
		}
	}
	if dir := os.Getenv("VEX80_EVIDENCE_DIR"); dir != "" && !t.Failed() {
		if err := os.WriteFile(dir+"/"+t.Name()+"-safe.log", []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Success retains its response and creates no error/debug argument record.
	success := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ __typename }"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(success, req)
	if !strings.Contains(success.Body.String(), `"__typename":"Query"`) || readLog() != text {
		t.Fatal("success response/logging changed")
	}
}

func TestGraphQLLocalLogRetainsCauseWithoutPrivateArgsOrAlias(t *testing.T) {
	readLog := localDiagnosticLog(t)
	ctx := graphql.WithOperationContext(context.Background(), &graphql.OperationContext{OperationName: "SyntheticPrivateOperation", Operation: &ast.OperationDefinition{Operation: ast.Mutation}})
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Field: graphql.CollectedField{Field: &ast.Field{Name: "sceneUpdate", Alias: "SyntheticPrivateAlias"}}, Args: map[string]any{"input": map[string]any{"title": "SyntheticPrivateTitle", "password": "SyntheticOpaqueCredential"}}})
	cause := errors.New("unfamiliar-driver-sentinel")
	err := fmt.Errorf("saving SyntheticPrivateTitle with SyntheticOpaqueCredential: %w: useful suffix", cause)
	ctx = graphql.WithResponseContext(ctx, gqlErrorHandler, recoverGraphQL)
	response := frameworkErrorResponse(ctx, err)
	if response.Path.String() != "SyntheticPrivateAlias" || !errors.Is(err, cause) {
		t.Fatal("original error/path changed")
	}
	text := readLog()
	for _, private := range []string{"SyntheticPrivateTitle", "SyntheticOpaqueCredential", "SyntheticPrivateAlias", "SyntheticPrivateOperation"} {
		if strings.Contains(text, private) {
			t.Error("known private input leaked in local error/debug log")
		}
	}
	for _, retained := range []string{"sceneUpdate", "saving", "unfamiliar-driver-sentinel", "useful suffix", "input"} {
		if !strings.Contains(text, retained) {
			t.Errorf("useful diagnostic lost %q", retained)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_ = gqlErrorHandler(canceled, errors.New("canceled operation"))
	if readLog() != text {
		t.Fatal("canceled context emitted local error/args")
	}
	if dir := os.Getenv("VEX80_EVIDENCE_DIR"); dir != "" && !t.Failed() {
		if err := os.WriteFile(dir+"/"+t.Name()+"-safe.log", []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGraphQLRecoveryLocalLogPrivacy(t *testing.T) {
	readLog := localDiagnosticLog(t)
	ctx := diagnosticAPIContext()
	_ = recoverGraphQL(ctx, "unfamiliar panic cause private scene title Cookie: sid=SyntheticSessionSecret")
	text := readLog()
	for _, private := range []string{"private scene title", "SyntheticSessionSecret", "JaneSmithPrivateMedia"} {
		if strings.Contains(text, private) {
			t.Error("private panic input leaked to local log")
		}
	}
	if !strings.Contains(text, "unfamiliar panic cause") {
		t.Fatal("panic cause suppressed")
	}
	if dir := os.Getenv("VEX80_EVIDENCE_DIR"); dir != "" && !t.Failed() {
		if err := os.WriteFile(dir+"/"+t.Name()+"-safe.log", []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGraphQLShortPrivateInputLocalLogRetainsCause(t *testing.T) {
	readLog := localDiagnosticLog(t)
	srv := handler.New(NewExecutableSchema(Config{Resolvers: &Resolver{}}))
	srv.AddTransport(transport.POST{})
	srv.SetErrorPresenter(gqlErrorHandler)
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation { changeOwnPassword(current_password:\"a\",new_password:\"t\") }"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	text := readLog()
	if !strings.Contains(text, "not authenticated") || !strings.Contains(text, "[argument value redacted]") {
		t.Fatal("short argument obscured local cause or remained raw")
	}
	ctx := graphql.WithFieldContext(context.Background(), &graphql.FieldContext{Field: graphql.CollectedField{Field: &ast.Field{Name: "sceneUpdate"}}, Args: map[string]any{"input": map[string]any{"password": "a"}}})
	ctx = graphQLPrivateContext(ctx)
	_ = gqlErrorHandler(ctx, errors.New("echo a: unfamiliar adapter cause"))
	text = readLog()
	if strings.Contains(text, "echo a:") || !strings.Contains(text, "unfamiliar adapter cause") {
		t.Fatal("short token leaked or erased diagnostic word")
	}
}
