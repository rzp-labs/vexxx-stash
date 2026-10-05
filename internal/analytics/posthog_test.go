package analytics

import "testing"

func TestMissingConfigurationKeepsDevelopmentOffline(t *testing.T) {
	t.Setenv("STASH_DEBUG", "1")
	t.Setenv("POSTHOG_PROJECT_TOKEN", "")
	t.Setenv("POSTHOG_HOST", "")
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
