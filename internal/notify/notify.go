// Package notify delivers notifications: desktop, Web Push (through the web
// server), ntfy and webhooks. STUB: workstream E replaces the bodies.
package notify

import (
	"context"
	"log/slog"

	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/config"
	"github.com/xasdta/askgate/internal/core"
)

// Pusher is implemented by *web.Server.
type Pusher interface {
	Push(ctx context.Context, n core.Notification) error
	ActionURL(requestID string, b api.Behavior) string
	BaseURL() string
}

// New returns a Notifier that fans out to every configured channel.
// p may be nil (no Web Push).
func New(cfg *config.Config, p Pusher, log *slog.Logger) core.Notifier { return core.NopNotifier{} }
