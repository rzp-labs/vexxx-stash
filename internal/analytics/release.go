package analytics

import (
	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/internal/build"
)

// ReleaseProperties uses the same version and revision embedded in the release
// binary. App version names the release; app build preserves commit provenance.
func ReleaseProperties() posthog.Properties {
	version, revision, _ := build.Version()
	if version == "" {
		version = "development"
	}
	if revision == "" {
		revision = "development"
	}
	return posthog.NewProperties().
		Set("$app_namespace", "vexxx-server").
		Set("$app_version", version).
		Set("$app_build", revision).
		Set("app_version", version).
		Set("app_revision", revision)
}
