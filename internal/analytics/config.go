package analytics

import "os"

// The linker embeds the same public ingestion settings used by the browser.
// Upload/API credentials must never populate these values.
var bundledProjectToken, bundledHost string

func posthogDestination() (projectToken, host string) {
	projectToken, host = os.Getenv("POSTHOG_PROJECT_TOKEN"), os.Getenv("POSTHOG_HOST")
	if projectToken != "" && host != "" {
		return projectToken, host
	}
	// Never mix half of an override with the bundled project's other setting.
	// Unconfigured development/local builds remain usable without inventing a
	// project destination. Published builds share the browser's bundled pair.
	return bundledProjectToken, bundledHost
}
