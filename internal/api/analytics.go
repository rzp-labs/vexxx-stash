package api

import (
	"context"
	"fmt"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/internal/analytics"
)

// captureAuthenticatedEvent records a completed API action. Database-backed
// requests use the immutable user ID established by authentication middleware;
// legacy single-user mode uses a non-identifying server ID without person profiles.
func captureAuthenticatedEvent(ctx context.Context, event string) {
	client := analytics.Client()
	if client == nil {
		return
	}

	client.Enqueue(authenticatedCapture(ctx, event))
}

func authenticatedCapture(ctx context.Context, event string) posthog.Capture {
	capture := posthog.Capture{
		DistinctId: "server",
		Event:      event,
		Properties: posthog.NewProperties().Set("$process_person_profile", false),
	}
	if authCtx := GetAuthContext(ctx); authCtx != nil && authCtx.User != nil {
		capture.DistinctId = fmt.Sprint(authCtx.User.ID)
	}

	return capture
}
