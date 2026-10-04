package api

import (
	"context"
	"fmt"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/internal/analytics"
)

// captureAuthenticatedEvent records a completed API action. Database-backed
// requests use the immutable user ID established by authentication middleware;
// legacy single-user mode has no stable user ID, so its events are personless.
func captureAuthenticatedEvent(ctx context.Context, event string) {
	client := analytics.Client()
	if client == nil {
		return
	}

	capture := posthog.Capture{Event: event}
	if authCtx := GetAuthContext(ctx); authCtx != nil && authCtx.User != nil {
		capture.DistinctId = fmt.Sprint(authCtx.User.ID)
	}

	client.Enqueue(capture)
}
