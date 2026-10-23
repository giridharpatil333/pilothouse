package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/config"
	"github.com/xasdta/askgate/internal/core"
)

func isolate(t *testing.T) {
	t.Setenv("ASKGATE_HOME", t.TempDir())
	t.Setenv("ASKGATE_USER_HOME", t.TempDir())
	run, err := os.MkdirTemp("/tmp", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	t.Setenv("ASKGATE_RUN_DIR", run)
}

type fakeChannel struct {
	name   string
	remote bool
	delay  time.Duration
	err    error
	mu     sync.Mutex
	got    []core.Notification
	block  chan struct{}
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Remote() bool { return f.remote }
func (f *fakeChannel) Send(ctx context.Context, n core.Notification) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.got = append(f.got, n)
	f.mu.Unlock()
	return f.err
}

func (f *fakeChannel) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func sampleRequest() *api.Request {
	return &api.Request{ID: "r_abc", Agent: api.AgentClaude, Tool: "Bash", ToolKind: api.ToolShell,
		Command: "git push origin main", Cwd: "/src/askgate", Status: api.RequestOpen, ExpiresAt: time.Now().Add(5 * time.Minute)}
}

func sampleSession() *api.Session {
	return &api.Session{ID: "s_abc", Agent: api.AgentClaude, Title: "askgate", Branch: "main"}
}

func TestFanOutRouting(t *testing.T) {
	isolate(t)
	desk := &fakeChannel{name: "desktop"}
	push := &fakeChannel{name: "webpush", remote: true}
	hook := &fakeChannel{name: "webhook", remote: true, err: errors.New("boom")}
	n := NewWith(nil, time.Second, desk, push, hook)

	ctx := context.Background()
	_ = n.Notify(ctx, core.Notification{Kind: core.NotifyBlocked, Request: sampleRequest(), Remote: false})
	_ = n.Notify(ctx, core.Notification{Kind: core.NotifyRequest, Request: sampleRequest(), Session: sampleSession(), Remote: true})
	_ = n.Notify(ctx, core.Notification{Kind: core.NotifyTest, Remote: false})
	n.Close()

	if desk.count() != 3 || push.count() != 2 || hook.count() != 2 {
		t.Fatalf("desktop=%d push=%d webhook=%d", desk.count(), push.count(), hook.count())
	}
	got := push.got[0]
	if got.Title != "Claude Code · askgate (main)" || got.Body != "Wants to run `git push origin main`" {
		t.Fatalf("rendered %q / %q", got.Title, got.Body)
	}
	if err := n.Notify(ctx, core.Notification{Kind: core.NotifyTest}); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestNotifyNeverBlocks(t *testing.T) {
	isolate(t)
	slow := &fakeChannel{name: "slow", remote: true, block: make(chan struct{})}
	fast := &fakeChannel{name: "fast", remote: true}
	n := NewWith(nil, 50*time.Millisecond, slow, fast)
	start := time.Now()
	for i := 0; i < queueSize*3; i++ {
		_ = n.Notify(context.Background(), core.Notification{Kind: core.NotifyRequest, Request: sampleRequest(), Remote: true})
	}
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("Notify blocked for %v", el)
	}
	// The fast channel isn't held up by the slow one.
	deadline := time.Now().Add(2 * time.Second)
	for fast.count() < queueSize && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fast.count() < queueSize {
		t.Fatalf("fast channel delivered %d", fast.count())
	}
	close(slow.block)
	n.Close()
	if slow.count() > queueSize+1 {
		t.Fatalf("slow channel got %d; queue must be bounded", slow.count())
	}
}

func TestPerChannelTimeout(t *testing.T) {
	isolate(t)
	stuck := &fakeChannel{name: "stuck", block: make(chan struct{})}
	n := NewWith(nil, 30*time.Millisecond, stuck)
	_ = n.Notify(context.Background(), core.Notification{Kind: core.NotifyTest})
	_ = n.Notify(context.Background(), core.Notification{Kind: core.NotifyTest})
	done := make(chan struct{})
	go func() { n.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeouts not applied")
	}
}

func TestText(t *testing.T) {
	r := sampleRequest()
	s := sampleSession()
	if got := Line(core.Notification{Kind: core.NotifyRequest, Request: r, Session: s}); got != "Claude Code wants to run `git push origin main` · askgate (main)" {
		t.Fatal(got)
	}
	long := *r
	long.Command = strings.Repeat("echo hi && ", 30)
	if got := Action(&long); len([]rune(got)) > maxCommand+20 || !strings.Contains(got, "…") {
		t.Fatalf("long command not shortened: %q", got)
	}
	multi := *r
	multi.Command, multi.ToolKind = "", api.ToolEdit
	multi.Paths = []string{"/src/askgate/internal/web/web.go", "/src/askgate/README.md"}
	if got := Action(&multi); got != "wants to edit `internal/web/web.go` and 1 more" {
		t.Fatal(got)
	}
	codex := *r
	codex.Agent = api.AgentCodex
	if got := Line(core.Notification{Kind: core.NotifyBlocked, Request: &codex}); got != "Codex wants to run `git push origin main` · askgate — answer in the terminal" {
		t.Fatal(got)
	}
	w := Render(core.Notification{Kind: core.NotifyWaiting, Session: &api.Session{Agent: api.AgentCodex, Title: "site", LastMessage: "All done.\nTests pass."}})
	if w.Title != "Codex · site" || w.Body != "Finished: All done. Tests pass." {
		t.Fatalf("%q / %q", w.Title, w.Body)
	}
	if got := Render(core.Notification{Kind: core.NotifyRequest, Title: "Custom", Request: r}); got.Title != "Custom" {
		t.Fatal("caller title overwritten")
	}
}

func TestDesktopCommandEscaping(t *testing.T) {
	hostile := `"; do shell script "rm -rf ~" --` + "\n" + `end run`
	name, args := DesktopCommand("darwin", "Claude Code · repo", hostile, nil)
	if name != "osascript" {
		t.Fatal(name)
	}
	// The script is constant; the text only ever travels as argv.
	script := strings.Join(args[:len(osascriptProgram)], " ")
	if strings.Contains(script, "rm -rf") || strings.Contains(script, "Claude") {
		t.Fatalf("user text reached the script: %q", script)
	}
	tail := args[len(osascriptProgram):]
	if len(tail) != 3 || tail[0] != "--" || tail[1] != "Claude Code · repo" || !strings.Contains(tail[2], `do shell script "rm -rf ~"`) || strings.Contains(tail[2], "\n") {
		t.Fatalf("argv = %q", tail)
	}

	look := func(string) (string, error) { return "/usr/bin/notify-send", nil }
	name, args = DesktopCommand("linux", "-t 1", "<b>x</b> & y", look)
	if name != "/usr/bin/notify-send" || args[1] != "--" || args[2] != "-t 1" || args[3] != "&lt;b&gt;x&lt;/b&gt; &amp; y" {
		t.Fatalf("linux = %s %q", name, args)
	}
	missing := func(string) (string, error) { return "", errors.New("not found") }
	if name, _ := DesktopCommand("linux", "a", "b", missing); name != "" {
		t.Fatal("notify-send missing should be a no-op")
	}
	if name, _ := DesktopCommand("windows", "a", "b", look); name != "" {
		t.Fatal("unsupported OS should be a no-op")
	}

	var ran []string
	d := &Desktop{GOOS: "darwin", Run: func(_ context.Context, n string, a ...string) error {
		ran = append([]string{n}, a...)
		return nil
	}}
	if err := d.Send(context.Background(), Render(core.Notification{Kind: core.NotifyBlocked, Request: sampleRequest()})); err != nil || ran[0] != "osascript" {
		t.Fatalf("send: %v %q", err, ran)
	}
}

type fakePusher struct {
	public string
	pushed []core.Notification
}

func (p *fakePusher) Push(_ context.Context, n core.Notification) error {
	p.pushed = append(p.pushed, n)
	return nil
}
func (p *fakePusher) ActionURL(id string, b api.Behavior) string {
	if p.public == "" {
		return ""
	}
	return p.public + "/api/actions/tok-" + id + "-" + string(b)
}
func (p *fakePusher) BaseURL() string   { return p.public }
func (p *fakePusher) PublicURL() string { return p.public }

type captured struct {
	path   string
	header http.Header
	body   string
}

func capture(t *testing.T, status int) (*httptest.Server, func() []captured) {
	var mu sync.Mutex
	var got []captured
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, captured{r.URL.Path, r.Header.Clone(), string(b)})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []captured {
		mu.Lock()
		defer mu.Unlock()
		return append([]captured(nil), got...)
	}
}

func TestNtfy(t *testing.T) {
	isolate(t)
	ts, got := capture(t, 200)
	p := &fakePusher{public: "https://mac.tail1.ts.net"}
	c := &Ntfy{Server: ts.URL + "/", Topic: "askgate-secret", Token: "tk_abc", Pusher: p, Client: ts.Client()}
	n := Render(core.Notification{Kind: core.NotifyRequest, Request: sampleRequest(), Session: &api.Session{Title: "répo"}, Remote: true})
	if err := c.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	r := got()[0]
	if r.path != "/askgate-secret" || r.body != "Claude Code wants to run `git push origin main` · répo" {
		t.Fatalf("publish %s %q", r.path, r.body)
	}
	h := r.header
	if title, err := new(mime.WordDecoder).DecodeHeader(h.Get("Title")); err != nil || title != "Claude Code · répo" {
		t.Errorf("Title = %q (%v)", h.Get("Title"), err)
	}
	if h.Get("Priority") != "high" || h.Get("Tags") != "lock" || h.Get("Authorization") != "Bearer tk_abc" || h.Get("Click") != "https://mac.tail1.ts.net/r/r_abc" {
		t.Errorf("headers = %v", h)
	}
	want := "http, Allow, https://mac.tail1.ts.net/api/actions/tok-r_abc-allow, method=POST, clear=true; " +
		"http, Deny, https://mac.tail1.ts.net/api/actions/tok-r_abc-deny, method=POST, clear=true; " +
		"view, Open, https://mac.tail1.ts.net/r/r_abc"
	if h.Get("Actions") != want {
		t.Errorf("Actions = %q", h.Get("Actions"))
	}

	// No public URL: no buttons, no click link.
	c2 := &Ntfy{Server: ts.URL, Topic: "t", Token: "user:pass", Pusher: &fakePusher{}, Client: ts.Client()}
	if err := c2.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	h = got()[1].header
	if h.Get("Actions") != "" || h.Get("Click") != "" || !strings.HasPrefix(h.Get("Authorization"), "Basic ") {
		t.Errorf("local-only headers = %v", h)
	}

	bad, _ := capture(t, 403)
	if err := (&Ntfy{Server: bad.URL, Topic: "t", Client: bad.Client()}).Send(context.Background(), n); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v", err)
	}
}

func TestWebhook(t *testing.T) {
	isolate(t)
	ts, got := capture(t, 204)
	w := &Webhook{URL: ts.URL + "/hook", Client: ts.Client()}
	if err := w.Send(context.Background(), Render(core.Notification{Kind: core.NotifyRequest, Request: sampleRequest(), Session: sampleSession()})); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(got()[0].body), &body); err != nil {
		t.Fatal(err)
	}
	want := "Claude Code wants to run `git push origin main` · askgate (main)"
	if body["text"] != want || body["content"] != want || got()[0].header.Get("Content-Type") != "application/json" {
		t.Fatalf("body = %v", body)
	}
}

func TestChannelsFromConfig(t *testing.T) {
	isolate(t)
	cfg := config.Default()
	cfg.Notify.Ntfy.Topic = "t"
	cfg.Notify.Webhooks = []string{"https://hooks.example/1", ""}
	names := func(cs []Channel) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Name())
		}
		return strings.Join(s, ",")
	}
	if got := names(Channels(cfg, &fakePusher{}, nil)); got != "desktop,webpush,ntfy,webhook" {
		t.Fatal(got)
	}
	var nilPusher *fakePusher
	cfg.Notify.Desktop = false
	if got := names(Channels(cfg, nilPusher, nil)); got != "ntfy,webhook" {
		t.Fatalf("typed nil pusher: %s", got)
	}
	// New wires the same channels. Desktop is off here: tests never show
	// real notifications.
	quiet := config.Default()
	quiet.Notify.Desktop = false
	n := New(quiet, nil, nil).(*Notifier)
	if len(n.workers) != 0 {
		t.Fatalf("workers = %d", len(n.workers))
	}
	if err := n.Notify(context.Background(), core.Notification{Kind: core.NotifyTest}); err != nil {
		t.Fatal(err)
	}
	n.Close()
}
