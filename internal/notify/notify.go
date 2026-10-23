// Package notify delivers notifications: desktop, Web Push (through the web
// server), ntfy and webhooks.
//
// Notify never blocks the caller: each channel has its own bounded queue
// and worker, so a slow webhook can't delay a push, and every delivery has
// a timeout. Failures are logged, never returned.
//
// Routing: desktop notifications are always sent (when enabled); the
// remote channels (Web Push, ntfy, webhooks) only when n.Remote is true —
// the person is away — or for a test notification.
package notify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/config"
	"github.com/xasdta/askgate/internal/core"
)

// Pusher is implemented by *web.Server.
type Pusher interface {
	Push(ctx context.Context, n core.Notification) error
	ActionURL(requestID string, b api.Behavior) string
	BaseURL() string
	// PublicURL is the URL reachable off this machine, or "" (local only).
	PublicURL() string
}

// Channel is one delivery mechanism.
type Channel interface {
	Name() string
	// Remote reports whether the channel reaches a person away from the
	// desk (sent only when Notification.Remote is true).
	Remote() bool
	Send(ctx context.Context, n core.Notification) error
}

// ErrClosed is returned by Notify after Close.
var ErrClosed = errors.New("notifier closed")

const (
	queueSize      = 32
	defaultTimeout = 10 * time.Second
)

// Notifier fans notifications out to channels.
type Notifier struct {
	log     *slog.Logger
	mu      sync.RWMutex
	closed  bool
	workers []*worker
	wg      sync.WaitGroup
}

type worker struct {
	ch      Channel
	q       chan core.Notification
	timeout time.Duration
}

// New returns a Notifier that fans out to every configured channel.
// p may be nil (no Web Push).
func New(cfg *config.Config, p Pusher, log *slog.Logger) core.Notifier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return NewWith(log, defaultTimeout, Channels(cfg, p, log)...)
}

// Channels builds the channels enabled in cfg.
func Channels(cfg *config.Config, p Pusher, log *slog.Logger) []Channel {
	if cfg == nil {
		cfg = config.Default()
	}
	if isNil(p) {
		p = nil
	}
	hc := &http.Client{Timeout: defaultTimeout}
	var chans []Channel
	if cfg.Notify.Desktop {
		chans = append(chans, NewDesktop())
	}
	if p != nil {
		chans = append(chans, pushChannel{p: p})
	}
	if cfg.Notify.Ntfy.Topic != "" {
		chans = append(chans, &Ntfy{Server: cfg.Notify.Ntfy.Server, Topic: cfg.Notify.Ntfy.Topic, Token: cfg.Notify.Ntfy.Token, Pusher: p, Client: hc})
	}
	for _, u := range cfg.Notify.Webhooks {
		if u != "" {
			chans = append(chans, &Webhook{URL: u, Client: hc})
		}
	}
	return chans
}

// isNil catches a typed nil pointer passed as a Pusher interface.
func isNil(p Pusher) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// NewWith starts a Notifier over explicit channels (tests, custom setups).
func NewWith(log *slog.Logger, timeout time.Duration, chans ...Channel) *Notifier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	n := &Notifier{log: log.With("component", "notify")}
	for _, c := range chans {
		w := &worker{ch: c, q: make(chan core.Notification, queueSize), timeout: timeout}
		n.workers = append(n.workers, w)
		n.wg.Add(1)
		go n.run(w)
	}
	return n
}

func (n *Notifier) run(w *worker) {
	defer n.wg.Done()
	for note := range w.q {
		ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
		start := time.Now()
		err := w.ch.Send(ctx, note)
		cancel()
		if err != nil {
			n.log.Warn("notification failed", "channel", w.ch.Name(), "kind", note.Kind, "err", err)
		} else {
			n.log.Debug("notification sent", "channel", w.ch.Name(), "kind", note.Kind, "took", time.Since(start).Round(time.Millisecond))
		}
	}
}

// Notify queues n for every channel it applies to and returns at once.
func (n *Notifier) Notify(_ context.Context, note core.Notification) error {
	note = Render(note)
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.closed {
		return ErrClosed
	}
	for _, w := range n.workers {
		if w.ch.Remote() && !note.Remote && note.Kind != core.NotifyTest {
			continue
		}
		select {
		case w.q <- note:
		default:
			n.log.Warn("notification dropped: queue full", "channel", w.ch.Name(), "kind", note.Kind)
		}
	}
	return nil
}

// Close stops accepting notifications and waits for queued deliveries.
func (n *Notifier) Close() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	for _, w := range n.workers {
		close(w.q)
	}
	n.mu.Unlock()
	n.wg.Wait()
}

// pushChannel delivers through the web server's Web Push.
type pushChannel struct{ p Pusher }

func (pushChannel) Name() string { return "webpush" }
func (pushChannel) Remote() bool { return true }
func (c pushChannel) Send(ctx context.Context, n core.Notification) error {
	return c.p.Push(ctx, n)
}
