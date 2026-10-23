// Package web serves the phone/browser app and the device API on a
// loopback TCP listener. STUB: workstream E replaces the bodies.
package web

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/config"
	"github.com/xasdta/askgate/internal/core"
)

type Deps struct {
	Core   core.Core
	DB     *sql.DB // shared SQLite handle; web owns tables prefixed web_
	Config *config.Config
	Log    *slog.Logger
}

type Server struct{ d Deps }

// New migrates web_* tables and loads/creates VAPID keys.
func New(d Deps) (*Server, error) { return &Server{d: d}, nil }

// Handler serves the PWA (/) and the device API (/api/*) on the TCP listener.
func (s *Server) Handler() http.Handler { return http.NotFoundHandler() }

// RegisterLocal adds local-admin routes to the Unix-socket mux:
// POST /v1/pair, POST /v1/login-link, GET /v1/devices, DELETE /v1/devices/{id}.
func (s *Server) RegisterLocal(mux *http.ServeMux) {}

// Push sends a Web Push notification to every paired device with a subscription.
func (s *Server) Push(ctx context.Context, n core.Notification) error { return nil }

// ActionURL is a signed one-time URL that answers a request with one POST
// (used by ntfy action buttons). Empty if no public URL is configured.
func (s *Server) ActionURL(requestID string, b api.Behavior) string { return "" }

// BaseURL is the URL devices should use: config web.public_url, else http://<web.addr>.
func (s *Server) BaseURL() string { return "" }
