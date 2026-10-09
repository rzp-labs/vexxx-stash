// Package analytics owns the process-wide PostHog client.
package analytics

import (
	"fmt"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/pkg/logger"
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
		BeforeSend:             beforeSend, Callback: deliveryCallback{}, Logger: deliveryLogger{},
	})
	if err != nil {
		return fmt.Errorf("create PostHog client: %w", err)
	}

	client = observedClient{client}
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

	err := client.Close()
	logger.Infof("telemetry shutdown summary: accepted=%d succeeded=%d discarded=%d enqueue_failures=%d SDK_warnings=%d log_export_failures=%d", deliveryAccepted.Load(), deliverySucceeded.Load(), deliveryFailed.Load(), enqueueFailed.Load(), sdkWarnings.Load(), logExportFailures.Load())
	return err
}
