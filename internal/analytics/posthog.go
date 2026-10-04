// Package analytics owns the process-wide PostHog client.
package analytics

import (
	"fmt"
	"os"

	"github.com/posthog/posthog-go"
)

var client posthog.Client

// Initialize creates the one PostHog client for this process. In production,
// missing configuration leaves analytics disabled without affecting the app.
// Debug builds return a clear error so missing events are not overlooked.
func Initialize() error {
	projectToken := os.Getenv("POSTHOG_PROJECT_TOKEN")
	if projectToken == "" {
		if os.Getenv("STASH_DEBUG") == "1" {
			return fmt.Errorf("POSTHOG_PROJECT_TOKEN variable required by PostHog is missing or un-configured, this causes events to be silently missed. This error stops appearing once POSTHOG_PROJECT_TOKEN is configured")
		}
		return nil
	}

	host := os.Getenv("POSTHOG_HOST")
	if host == "" {
		if os.Getenv("STASH_DEBUG") == "1" {
			return fmt.Errorf("POSTHOG_HOST variable required by PostHog is missing or un-configured, this causes events to be silently missed. This error stops appearing once POSTHOG_HOST is configured")
		}
		return nil
	}

	var err error
	client, err = posthog.NewWithConfig(projectToken, posthog.Config{Endpoint: host})
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
