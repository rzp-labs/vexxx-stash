package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/google/uuid"
	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/pkg/diagnostics"
	"github.com/stashapp/stash/pkg/plugin"
	"github.com/stashapp/stash/pkg/session"
	"github.com/vektah/gqlparser/v2/ast"
)

type capturePluginConfig struct{ root string }

func (c capturePluginConfig) GetHost() string              { return "localhost" }
func (c capturePluginConfig) GetPort() int                 { return 9999 }
func (c capturePluginConfig) GetConfigPathAbs() string     { return c.root }
func (c capturePluginConfig) HasTLSConfig() bool           { return false }
func (c capturePluginConfig) GetPluginsPath() string       { return c.root }
func (c capturePluginConfig) GetDisabledPlugins() []string { return nil }
func (c capturePluginConfig) GetPythonPath() string        { return "" }
func (c capturePluginConfig) GetUsername() string          { return "synthetic" }
func (c capturePluginConfig) GetAPIKey() string            { return "" }
func (c capturePluginConfig) GetSessionStoreKey() []byte {
	return []byte("synthetic-session-key-32-bytes!!")
}
func (c capturePluginConfig) GetMaxSessionAge() int                   { return 60 }
func (c capturePluginConfig) ValidateCredentials(string, string) bool { return false }

type pluginCaptureSchema struct {
	graphql.ExecutableSchema
	cache *plugin.Cache
}

type capturePrivateArgument struct{ scene string }

func (v capturePrivateArgument) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"scene": v.scene})
}

func (s pluginCaptureSchema) Exec(ctx context.Context) graphql.ResponseHandler {
	// Actual plugin execution and presenter, without the global manager/database.
	ctx = graphql.WithFieldContext(ctx, &graphql.FieldContext{Object: "Mutation", Field: graphql.CollectedField{Field: &ast.Field{Name: "runPluginOperation", Alias: "runPluginOperation"}}})
	_, err := s.cache.RunPlugin(ctx, "SyntheticPlugin", plugin.OperationInput{"private": capturePrivateArgument{scene: "SyntheticPrivateSceneIdentity9182"}})
	graphql.AddError(ctx, err)
	return graphql.OneShot(&graphql.Response{Data: json.RawMessage(`{"runPluginOperation":null}`), Errors: graphql.GetErrors(ctx)})
}

func TestPluginGraphQLCaptureOnceAndRejectedEnqueueRetry(t *testing.T) {
	for _, rejectFirst := range []bool{false, true} {
		name := "accepted observer"
		if rejectFirst {
			name = "rejected observer retries at API"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var events []map[string]any
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Batch []map[string]any }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				events = append(events, body.Batch...)
				mu.Unlock()
				_, _ = io.WriteString(w, `{"status":1}`)
			}))
			defer receiver.Close()
			t.Setenv("POSTHOG_PROJECT_TOKEN", "synthetic-plugin-request-project")
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
			root := t.TempDir()
			manifest := "name: SyntheticPlugin\ninterface: raw\nexec: ['/bin/sh', '-c', \"cat >&2; printf '\\nModuleNotFoundError: stashapi\\n' >&2; exit 7\"]\n"
			if err := os.WriteFile(filepath.Join(root, "SyntheticPlugin.yml"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := capturePluginConfig{root}
			cache := plugin.NewCache(cfg)
			cache.RegisterSessionStore(session.NewStore(cfg))
			cache.ReloadPlugins()
			observations := 0
			cache.OnError = func(ctx context.Context, err error) {
				observations++
				if rejectFirst {
					// Keep the closed SDK installed so Enqueue genuinely rejects.
					_ = analytics.Close()
				}
				analytics.CapturePluginFailure(ctx, err)
				if rejectFirst {
					if diagnostics.Reported(ctx, err) {
						t.Error("rejected enqueue reserved the error")
					}
					if err := analytics.Initialize(); err != nil {
						t.Error(err)
					}
				}
			}
			schema := pluginCaptureSchema{ExecutableSchema: NewExecutableSchema(Config{Resolvers: &Resolver{}}), cache: cache}
			srv := handler.New(schema)
			srv.AddTransport(transport.POST{})
			srv.SetErrorPresenter(gqlErrorHandler)
			srv.AroundOperations(graphQLCaptureState)
			for i := 0; i < 2; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"mutation { runPluginOperation(plugin_id:\"SyntheticPlugin\") }"}`))
				req.Header.Set("Content-Type", "application/json")
				srv.ServeHTTP(rec, req)
				var response struct {
					Errors []struct{ Extensions map[string]any }
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Errors) != 1 {
					t.Fatal("expected one actual plugin error", rec.Body.String(), err)
				}
				if rejectFirst {
					id, _ := response.Errors[0].Extensions["telemetry_event_id"].(string)
					if uuid.Validate(id) != nil {
						t.Fatal("API retry missing accepted UUID", rec.Body.String())
					}
				} else if response.Errors[0].Extensions["telemetry_captured"] != nil {
					t.Error("API presenter enqueued after accepted plugin capture")
				}
			}
			if err := analytics.Close(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(events) != 2 || observations != 2 {
				t.Fatalf("expected one capture per request: events=%d observations=%d", len(events), observations)
			}
			if events[0]["uuid"] == events[1]["uuid"] {
				t.Fatal("distinct operations deduplicated each other")
			}
			encoded, _ := json.Marshal(events)
			if strings.Contains(string(encoded), "SyntheticPrivateSceneIdentity9182") {
				t.Fatal("unexported serialized input reached actual SDK payload")
			}
			for _, event := range events {
				props := event["properties"].(map[string]any)
				if props["plugin_id"] != "SyntheticPlugin" || props["plugin_operation"] != "run" || props["plugin_exit_code"] != float64(7) || !strings.Contains(props["plugin_output"].(string), "ModuleNotFoundError") {
					t.Fatal("plugin identity/diagnostic lost", event)
				}
			}
		})
	}
}
