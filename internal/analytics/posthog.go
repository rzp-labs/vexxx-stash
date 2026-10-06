// Package analytics owns the process-wide PostHog client.
package analytics

import (
	"fmt"
	"time"

	"github.com/posthog/posthog-go"
)

var client posthog.Client

// Initialize creates the process-wide client using the browser's bundled public
// destination, or a complete runtime override. Unconfigured local builds stay usable.
func Initialize() error {
	projectToken, host := posthogDestination()
	if projectToken == "" || host == "" {
		client = nil
		return nil
	}

	var err error
	client, err = posthog.NewWithConfig(projectToken, posthog.Config{
		Endpoint:               host,
		ShutdownTimeout:        5 * time.Second,
		DefaultEventProperties: ReleaseProperties(),
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
