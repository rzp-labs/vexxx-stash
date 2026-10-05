package api

import (
	"context"
	"fmt"

	"github.com/posthog/posthog-go"
	"github.com/stashapp/stash/internal/analytics"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
)

// captureAuthenticatedEvent records a completed API action. Database-backed
// requests resolve the immutable user ID only when emitting an enabled event;
// legacy single-user mode uses a non-identifying server ID without person profiles.
func captureAuthenticatedEvent(ctx context.Context, event string) {
	client := analytics.Client()
	if client == nil {
		return
	}

	// Keep telemetry identity resolution off streaming/assets/ordinary API paths,
	// and do no database work at all when telemetry is disabled.
	if authCtx := GetAuthContext(ctx); authCtx == nil || authCtx.User == nil {
		if username := session.GetCurrentUserID(ctx); username != nil && *username != "" {
			mgr := manager.GetInstance()
			var user *models.User
			if err := mgr.Repository.WithReadTxn(ctx, func(ctx context.Context) error {
				var err error
				user, err = mgr.Repository.User.FindByUsername(ctx, *username)
				return err
			}); err != nil {
				logger.Errorf("Error resolving analytics user ID: %v", err)
			} else if user != nil {
				ctx = SetAuthContext(ctx, &AuthorizationContext{User: user})
			}
		}
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
