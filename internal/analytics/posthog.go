// Package analytics owns the process-wide PostHog client.
package analytics

import (
	"fmt"
	"os"
	"time"

	"github.com/stashapp/stash/internal/build"

	"github.com/posthog/posthog-go"
)

var client posthog.Client

// Initialize creates the one PostHog client for this process. In production,
// missing configuration leaves analytics disabled without affecting the app.
// Development builds also remain usable without external analytics configuration.
func Initialize() error {
	// Supplying both settings is the operator's explicit opt-in. Missing settings
	// disable telemetry in development as well as production.
	projectToken := os.Getenv("POSTHOG_PROJECT_TOKEN")
	host := os.Getenv("POSTHOG_HOST")
	if projectToken == "" || host == "" {
		return nil
	}

	var err error
	version, revision, _ := build.Version()
	client, err = posthog.NewWithConfig(projectToken, posthog.Config{
		Endpoint:               host,
		ShutdownTimeout:        5 * time.Second,
		DefaultEventProperties: posthog.NewProperties().Set("app_version", version).Set("app_revision", revision),
	})
	if err != nil {
		return fmt.Errorf("create PostHog client: %w", err)
	}

	return nil
}

// Client returns the process-wide PostHog client, or nil when analytics is not
// configured. Callers must guard captures when it is nil.
func Client() posthog.Client {
	return client
}

// Close flushes queued events during graceful process shutdown.
func Close() error {
	if client == nil {
		return nil
	}

	return client.Close()
}
