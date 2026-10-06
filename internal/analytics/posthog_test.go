package analytics

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/posthog/posthog-go"
)

func setBundledDestination(t *testing.T, token, host string) {
	t.Helper()
	previousToken, previousHost := bundledProjectToken, bundledHost
	bundledProjectToken, bundledHost = token, host
	t.Cleanup(func() { bundledProjectToken, bundledHost = previousToken, previousHost })
}

func TestMissingConfigurationKeepsDevelopmentOffline(t *testing.T) {
	t.Setenv("STASH_DEBUG", "1")
	t.Setenv("POSTHOG_PROJECT_TOKEN", "")
	t.Setenv("POSTHOG_HOST", "")
	setBundledDestination(t, "", "")
	if err := Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := InitializeLogs(); err != nil {
		t.Fatal(err)
	}
	if Client() != nil || logsProvider != nil {
		t.Fatal("telemetry enabled without configuration")
	}
}

// Run this fixture with the Make-generated linker flags to verify that the
// public settings survive compilation, not merely assignment in a unit test.
func TestBundledBuildSettingsSurviveLinking(t *testing.T) {
	if bundledProjectToken == "" && bundledHost == "" {
		t.Skip("ordinary unit builds do not embed public ingestion settings")
	}
	t.Setenv("POSTHOG_PROJECT_TOKEN", "")
	t.Setenv("POSTHOG_HOST", "")
	token, host := posthogDestination()
	if token != "phc_linker_configuration_fixture" || host != "https://us.i.posthog.com" {
		t.Fatal("compiled backend did not resolve the browser's public build inputs")
	}
}

func TestPostHogDestinationKeepsProjectPairsTogether(t *testing.T) {
	for _, test := range []struct {
		name, runtimeToken, runtimeHost, wantToken, wantHost string
	}{
		{"bundled default", "", "", "phc_bundled", "https://bundled.example"},
		{"complete override", "phc_override", "https://override.example", "phc_override", "https://override.example"},
		{"partial token uses bundle", "phc_override", "", "phc_bundled", "https://bundled.example"},
		{"partial host uses bundle", "", "https://override.example", "phc_bundled", "https://bundled.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setBundledDestination(t, "phc_bundled", "https://bundled.example")
			t.Setenv("POSTHOG_PROJECT_TOKEN", test.runtimeToken)
			t.Setenv("POSTHOG_HOST", test.runtimeHost)
			token, host := posthogDestination()
			if token != test.wantToken || host != test.wantHost {
				t.Fatalf("destination=%q/%q, want complete selected pair", token, host)
			}
		})
	}
}

func TestIncompleteBuildConfigurationStaysOffline(t *testing.T) {
	for _, pair := range [][2]string{{"phc_bundled", ""}, {"", "https://bundled.example"}} {
		setBundledDestination(t, pair[0], pair[1])
		t.Setenv("POSTHOG_PROJECT_TOKEN", "")
		t.Setenv("POSTHOG_HOST", "")
		if err := Initialize(); err != nil {
			t.Fatal(err)
		}
		if err := InitializeLogs(); err != nil {
			t.Fatal(err)
		}
		if Client() != nil || logsProvider != nil {
			t.Fatal("invented a destination from incomplete public build settings")
		}
	}
}

func TestBundledDestinationEnablesEventsAndLogsForSameProject(t *testing.T) {
	for _, override := range []bool{false, true} {
		name := "bundle_without_runtime_pair"
		if override {
			name = "complete_runtime_override"
		}
		t.Run(name, func(t *testing.T) {
			const projectToken = "phc_synthetic_configuration_project"
			var mu sync.Mutex
			var eventRequests, logRequests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/batch/":
					var body io.Reader = r.Body
					if r.Header.Get("Content-Encoding") == "gzip" {
						reader, err := gzip.NewReader(r.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						defer reader.Close()
						body = reader
					}
					var payload struct {
						APIKey string `json:"api_key"`
					}
					if err := json.NewDecoder(body).Decode(&payload); err != nil || payload.APIKey != projectToken {
						t.Error("event SDK used a different project")
					}
					mu.Lock()
					eventRequests++
					mu.Unlock()
					_, _ = io.WriteString(w, `{"status":1}`)
				case "/i/v1/logs":
					if r.Header.Get("Authorization") != "Bearer "+projectToken {
						t.Error("logs SDK used a different project")
					}
					_, _ = io.Copy(io.Discard, r.Body)
					mu.Lock()
					logRequests++
					mu.Unlock()
				default:
					t.Errorf("unexpected SDK endpoint %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			setBundledDestination(t, projectToken, server.URL)
			t.Setenv("POSTHOG_PROJECT_TOKEN", "")
			t.Setenv("POSTHOG_HOST", "")
			if override {
				// A bundle for a different project must not affect the override.
				bundledProjectToken, bundledHost = "phc_other_project", "http://127.0.0.1:1"
				t.Setenv("POSTHOG_PROJECT_TOKEN", projectToken)
				t.Setenv("POSTHOG_HOST", server.URL)
			}
			if err := Initialize(); err != nil {
				t.Fatal(err)
			}
			if err := InitializeLogs(); err != nil {
				t.Fatal(err)
			}
			if Client() == nil || logsProvider == nil {
				t.Fatal("configured clients were not enabled")
			}
			t.Cleanup(func() {
				_ = Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = CloseLogs(ctx)
				client, logsProvider, logsLogger = nil, nil, nil
			})
			if err := Client().Enqueue(posthog.Capture{DistinctId: "server", Event: "synthetic_configuration_check"}); err != nil {
				t.Fatal(err)
			}
			LogInfo("synthetic_configuration_check")
			if err := Client().Flush(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := logsProvider.ForceFlush(ctx); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if eventRequests != 1 || logRequests != 1 {
				t.Fatalf("event/log requests=%d/%d, want 1/1", eventRequests, logRequests)
			}
		})
	}
}
