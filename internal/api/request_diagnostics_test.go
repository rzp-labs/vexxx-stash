package api

import (
	"context"
	"encoding/json"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/errcode"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/google/uuid"
	"github.com/stashapp/stash/internal/analytics"
)

// Exercise the pinned POST decoder, executor, validator and presenter together.
// Every request fails before resolver execution; no database or live telemetry.
func TestGraphQLRequestValidationDiagnosticPrivacy(t *testing.T) {
	var mu sync.Mutex
	var events []map[string]any
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer receiver.Close()
	t.Setenv("POSTHOG_PROJECT_TOKEN", "synthetic-request-validation-project")
	t.Setenv("POSTHOG_HOST", receiver.URL)
	if err := analytics.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = analytics.Close()
		t.Setenv("POSTHOG_PROJECT_TOKEN", "")
		t.Setenv("POSTHOG_HOST", "")
		_ = analytics.Initialize()
	})
	srv := handler.New(NewExecutableSchema(Config{Resolvers: &Resolver{}}))
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.GET{})
	srv.SetErrorPresenter(gqlErrorHandler)
	request := func(query string, extra map[string]any) string {
		body := map[string]any{"query": query}
		for k, v := range extra {
			body[k] = v
		}
		data, _ := json.Marshal(body)
		return string(data)
	}
	cases := []struct{ name, body, private, retained string }{
		{"operation selection", request(`query DiagnosticProbe { __typename }`, map[string]any{"operationName": "JaneSmithPrivateMedia"}), "JaneSmithPrivateMedia", "operation"},
		{"GET operation selection", request(`query DiagnosticProbe { __typename }`, map[string]any{"operationName": "JaneSmithPrivateMedia"}), "JaneSmithPrivateMedia", "operation"},
		{"unknown argument", request(`{ findScene(JaneSmithPrivateMedia:"opaque") { id } }`, nil), "JaneSmithPrivateMedia", "Unknown argument"},
		{"unknown directive", request(`{ __typename @JaneSmithPrivateMedia }`, nil), "JaneSmithPrivateMedia", "Unknown directive"},
		{"unknown input field", request(`{ findScenes(filter:{JaneSmithPrivateMedia:1}) { count } }`, nil), "JaneSmithPrivateMedia", "is not defined by type"},
		{"fragment cycle", request(`{ ...JaneSmithPrivateMedia } fragment JaneSmithPrivateMedia on Query { ...JaneSmithPrivateMedia }`, nil), "JaneSmithPrivateMedia", "within itself"},
		{"duplicate operation", request(`query JaneSmithPrivateMedia { __typename } query JaneSmithPrivateMedia { __typename }`, nil), "JaneSmithPrivateMedia", "only one operation"},
		{"unknown field", request(`{ JaneSmithPrivateMedia }`, nil), "JaneSmithPrivateMedia", "Cannot query field"},
		{"unknown type", request(`query Probe($x: JaneSmithPrivateMedia) { findScene(id:$x) { id } }`, nil), "JaneSmithPrivateMedia", "Unknown type"},
		{"unknown fragment", request(`{ ...JaneSmithPrivateMedia }`, nil), "JaneSmithPrivateMedia", "Unknown fragment"},
		{"unknown variable", request(`{ findScene(id:$JaneSmithPrivateMedia) { id } }`, nil), "JaneSmithPrivateMedia", "is not defined"},
		{"alias conflict", request(`{ JaneSmithPrivateMedia: __typename JaneSmithPrivateMedia: findScene { id } }`, nil), "JaneSmithPrivateMedia", "conflict"},
		{"schema name alias", request(`{ count: __typename count: findScene { id } }`, nil), `Fields \"count\"`, "conflict"},
		{"string literal", request(`{ findScenes(filter:{page:"JaneSmithPrivateMedia"}) { count } }`, nil), "JaneSmithPrivateMedia", "Int cannot represent"},
		{"number literal", request(`{ findScenes(filter:{direction:918273645}) { count } }`, nil), "918273645", "SortDirectionEnum"},
		{"parse token", request(`JaneSmithPrivateMedia`, nil), "JaneSmithPrivateMedia", "Unexpected"},
		{"enum variable", request(`query Probe($filter:FindFilterType) { findScenes(filter:$filter) { count } }`, map[string]any{"variables": map[string]any{"filter": map[string]any{"direction": "JaneSmithPrivateMedia"}}}), "JaneSmithPrivateMedia", "is not a valid SortDirectionEnum"},
		{"variable path", request(`query Probe($filter:FindFilterType) { findScenes(filter:$filter) { count } }`, map[string]any{"variables": map[string]any{"filter": map[string]any{"JaneSmithPrivateMedia": true}}}), "JaneSmithPrivateMedia", "unknown field"},
		{"JSON numeric decode value", `{"operationName":918273645}`, "918273645", "cannot unmarshal number"},
		{"enum delimiter value", request(`query Probe($filter:FindFilterType) { findScenes(filter:$filter) { count } }`, map[string]any{"variables": map[string]any{"filter": map[string]any{"direction": "JaneSmithPrivateMedia is not a valid PrivateSuffix"}}}), "JaneSmithPrivateMedia", "is not a valid SortDirectionEnum"},
		{"malformed JSON", `{"query":"JaneSmithPrivateMedia",`, "JaneSmithPrivateMedia", "json request body could not be decoded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			before := len(events)
			mu.Unlock()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if strings.HasPrefix(tc.name, "GET ") {
				var params map[string]string
				_ = json.Unmarshal([]byte(tc.body), &params)
				values := url.Values{}
				for k, v := range params {
					values.Set(k, v)
				}
				req = httptest.NewRequest(http.MethodGet, "/graphql?"+values.Encode(), nil)
			}
			srv.ServeHTTP(rec, req)
			var response struct {
				Errors []struct {
					Message    string
					Extensions map[string]any
				}
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Errors) == 0 {
				t.Fatal("expected pre-resolver validation failure")
			}
			for _, e := range response.Errors {
				if e.Extensions["telemetry_captured"] != true {
					t.Fatal("accepted capture missing marker", e)
				}
				id, _ := e.Extensions["telemetry_event_id"].(string)
				if uuid.Validate(id) != nil {
					t.Fatal("invalid marker UUID")
				}
			}
			if tc.name == "fragment cycle" {
				// The inherited client sanitizer matches "read " inside
				// "spread "; retain that existing internal-error response.
				if response.Errors[0].Message != "an internal error occurred" {
					t.Fatal("client sanitizer changed")
				}
			} else if !strings.Contains(rec.Body.String(), tc.private) {
				t.Fatal("original framework response changed", rec.Body.String())
			}
			if err := analytics.Client().Flush(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			data, _ := json.Marshal(events[before:])
			count := len(events) - before
			mu.Unlock()
			if count != len(response.Errors) {
				t.Fatalf("events=%d errors=%d", count, len(response.Errors))
			}
			if strings.Contains(string(data), tc.private) {
				t.Error("request-derived value leaked to SDK")
			}
			if !strings.Contains(string(data), tc.retained) {
				t.Errorf("diagnosis lost %q", tc.retained)
			}
		})
	}
	// A successful builtin query still returns data and emits no exception.
	mu.Lock()
	beforeSuccess := len(events)
	mu.Unlock()
	success := httptest.NewRecorder()
	successRequest := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(request(`{ __typename }`, nil)))
	successRequest.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(success, successRequest)
	if !strings.Contains(success.Body.String(), `"__typename":"Query"`) || strings.Contains(success.Body.String(), `"errors"`) {
		t.Fatal("success response changed", success.Body.String())
	}
	if err := analytics.Client().Flush(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	afterSuccess := len(events)
	mu.Unlock()
	if afterSuccess != beforeSuccess {
		t.Fatal("success emitted exception")
	}
	if dir := os.Getenv("VEX80_EVIDENCE_DIR"); dir != "" && !t.Failed() {
		mu.Lock()
		data, _ := json.MarshalIndent(events, "", "  ")
		mu.Unlock()
		if err := os.WriteFile(dir+"/"+t.Name()+"-events.json", data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_ = analytics.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(cases[0].body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "telemetry_captured") || strings.Contains(rec.Body.String(), "telemetry_event_id") {
		t.Fatal("rejected enqueue marked")
	}
}

func TestGraphQLRequestDiagnosticBoundedInput(t *testing.T) {
	for _, query := range []string{strings.Repeat(" ", 256*1024) + "JaneSmithPrivateMedia", "{ " + strings.Repeat("__typename ", 4096) + "JaneSmithPrivateMedia }"} {
		ctx := graphql.WithOperationContext(context.Background(), &graphql.OperationContext{RawQuery: query})
		err := &gqlerror.Error{Message: `Cannot query field "JaneSmithPrivateMedia" on type "Query".`, Rule: "FieldsOnCorrectType", Extensions: map[string]any{"code": errcode.ValidationFailed}}
		diagnostic := graphQLRequestDiagnostic(ctx, err)
		if strings.Contains(diagnostic.Error(), "JaneSmithPrivateMedia") || !strings.Contains(diagnostic.Error(), "FieldsOnCorrectType") || !strings.Contains(diagnostic.Error(), "diagnostic input limit") {
			t.Fatal("input limit lost safe rule/omission", diagnostic)
		}
		if err.Message != `Cannot query field "JaneSmithPrivateMedia" on type "Query".` {
			t.Fatal("client error mutated")
		}
	}
}
