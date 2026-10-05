package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
)

func TestAuthenticatedCapturePrivacyAndAnonymousEvents(t *testing.T) {
	for _, user := range []*models.User{nil, {ID: 42, Username: "private-user-name"}} {
		ctx := context.Background()
		if user != nil {
			ctx = SetAuthContext(ctx, &AuthorizationContext{User: user})
		}
		event := authenticatedCapture(ctx, "scene_created")
		if err := event.Validate(); err != nil {
			t.Fatal(err)
		}
		expected := "server"
		if user != nil {
			expected = "42"
		}
		if event.DistinctId != expected {
			t.Fatalf("distinct ID = %q", event.DistinctId)
		}
		if event.Properties["$process_person_profile"] != false {
			t.Fatal("person profiles enabled")
		}
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "private-user-name") {
			t.Fatal("username exported")
		}
	}
}

// No manager/repository is initialized in this test: a disabled SDK must return
// before trying to resolve the account, even with a restored authenticated session.
func TestDisabledTelemetryDoesNotResolveUser(t *testing.T) {
	ctx := session.SetCurrentUserID(context.Background(), "private-user-name")
	captureAuthenticatedEvent(ctx, "scene_created")
}
