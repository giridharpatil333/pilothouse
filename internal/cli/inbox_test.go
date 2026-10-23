package cli

// Tests for workstream F's commands, against a fake daemon on a Unix
// socket in a short /tmp dir. Helpers are prefixed "ibx" so they never
// collide with other workstreams' tests in this package.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xasdta/askgate/internal/adapters"
	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/client"
	"github.com/xasdta/askgate/internal/presence"
	"github.com/xasdta/askgate/internal/service"
	"github.com/xasdta/askgate/internal/tui"
	"github.com/xasdta/askgate/internal/version"
)

var ibxT0 = time.Date(2026, 10, 22, 15, 4, 5, 0, time.UTC)

type ibxDecideCall struct {
	id     string
	body   api.DecideBody
	header string
}

type ibxDaemon struct {
	mu       sync.Mutex
	snap     api.Snapshot
	events   []api.Event
	decides  []ibxDecideCall
	presence []string // mode|header
	notified int
	refuse   bool // simulate an agent caller (403)
	policy   api.PolicyStatus
	devices  []api.Device
	explain  api.Explain
}

func ibxError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Error{Error: msg, Code: code})
}

func ibxJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (d *ibxDaemon) findRequest(ref string) (api.Request, int) {
	var hits []api.Request
	for _, r := range d.snap.Requests {
		if strings.HasPrefix(r.ID, ref) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return api.Request{}, 404
	case 1:
		return hits[0], 200
	}
	return api.Request{}, 400
}

func (d *ibxDaemon) findSession(ref string) (api.Session, bool) {
	for _, s := range d.snap.Sessions {
		if strings.HasPrefix(s.ID, ref) {
			return s, true
		}
	}
	return api.Session{}, false
}

func (d *ibxDaemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		ibxJSON(w, api.Health{Version: version.Version, Protocol: api.ProtocolVersion, PID: 4242, Machine: "box"})
	})
	mux.HandleFunc("GET /v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		ibxJSON(w, d.snap)
	})
	mux.HandleFunc("GET /v1/requests", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		ibxJSON(w, d.snap.Requests)
	})
	mux.HandleFunc("GET /v1/requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		req, st := d.findRequest(r.PathValue("id"))
		if st != 200 {
			ibxError(w, st, "not_found", "request not found")
			return
		}
		ibxJSON(w, req)
	})
	mux.HandleFunc("POST /v1/requests/{id}/decide", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		var b api.DecideBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		d.decides = append(d.decides, ibxDecideCall{r.PathValue("id"), b, r.Header.Get(client.ClientHeader)})
		if d.refuse {
			ibxError(w, 403, "agent_caller", "decisions are not accepted from agent processes")
			return
		}
		req, st := d.findRequest(r.PathValue("id"))
		if st != 200 {
			ibxError(w, 404, "not_found", "request not found")
			return
		}
		if req.ID == "r_raced00001" {
			ibxError(w, 409, "already_decided", "request already closed by device:d_phone")
			return
		}
		req.Status = api.RequestDecided
		req.Decision = &api.Decision{RequestID: req.ID, Behavior: b.Behavior, Scope: b.Scope, Message: b.Message, By: r.Header.Get(client.ClientHeader)}
		ibxJSON(w, req)
	})
	mux.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		s, ok := d.findSession(r.PathValue("id"))
		if !ok {
			ibxError(w, 404, "not_found", "session not found")
			return
		}
		ibxJSON(w, s)
	})
	mux.HandleFunc("GET /v1/sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var out []api.Event
		for i := len(d.events) - 1; i >= 0 && len(out) < limit; i-- {
			if d.events[i].SessionID == r.PathValue("id") {
				out = append(out, d.events[i])
			}
		}
		ibxJSON(w, out)
	})
	mux.HandleFunc("GET /v1/sessions/{id}/explain", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if !strings.HasPrefix(d.explain.Session.ID, r.PathValue("id")) {
			ibxError(w, 404, "not_found", "session not found")
			return
		}
		ibxJSON(w, d.explain)
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		d.mu.Lock()
		var out []api.Event
		for _, e := range d.events {
			if e.Seq > after {
				out = append(out, e)
			}
		}
		d.mu.Unlock()
		if r.URL.Query().Get("follow") != "1" {
			if out == nil {
				out = []api.Event{}
			}
			ibxJSON(w, out)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Live events: one new event after the log was printed.
		live := api.Event{Seq: 999, At: ibxT0, Type: api.EventPresenceChanged, Data: json.RawMessage(`{"state":"away","mode":"auto","source":"os_idle","idle_secs":120}`)}
		b, _ := json.Marshal(live)
		fmt.Fprintf(w, ": ping\n\nevent: event\ndata: %s\n\n", b)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("PUT /v1/presence", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		var b api.PresenceBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		d.presence = append(d.presence, string(b.Mode)+"|"+r.Header.Get(client.ClientHeader))
		if d.refuse {
			ibxError(w, 403, "agent_caller", "presence changes are not accepted from agent processes")
			return
		}
		st := api.PresencePresent
		if b.Mode == api.PresenceForceAway {
			st = api.PresenceAway
		}
		src := "manual"
		if b.Mode == api.PresenceAuto {
			src = "os_idle"
		}
		ibxJSON(w, api.Presence{State: st, Mode: b.Mode, Source: src, IdleSecs: 4})
	})
	mux.HandleFunc("POST /v1/notify/test", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.notified++
		d.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /v1/policy/status", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		ibxJSON(w, d.policy)
	})
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		ibxJSON(w, d.devices)
	})
	return mux
}

// ibxEnv points every Askgate path at temp dirs (BUILD-SPEC invariant 1)
// and swaps every system seam for a fake.
func ibxEnv(t *testing.T) string {
	t.Helper()
	run, err := os.MkdirTemp("/tmp", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	t.Setenv("ASKGATE_RUN_DIR", run)
	t.Setenv("ASKGATE_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("ASKGATE_USER_HOME", "/home/dev")
	t.Setenv("ASKGATE_NO_UPDATE_CHECK", "1")

	saved := []func(){}
	save := func(restore func()) { saved = append(saved, restore) }
	{
		v := inboxAdapters
		save(func() { inboxAdapters = v })
		inboxAdapters = func() []adapters.Adapter { return nil }
	}
	{
		v := inboxServiceMgr
		save(func() { inboxServiceMgr = v })
		inboxServiceMgr = func(string) serviceManager { return &ibxService{kind: ""} }
	}
	{
		v := inboxStartDaemon
		save(func() { inboxStartDaemon = v })
		inboxStartDaemon = func(context.Context, string) error { return errors.New("not in tests") }
	}
	{
		v := inboxExecutable
		save(func() { inboxExecutable = v })
		inboxExecutable = func() (string, error) { return "/opt/askgate/bin/askgate", nil }
	}
	{
		v := inboxRunTUI
		save(func() { inboxRunTUI = v })
		inboxRunTUI = func(context.Context, tui.Options) error { return errors.New("tui not expected") }
	}
	{
		v := inboxDetector
		save(func() { inboxDetector = v })
		inboxDetector = func() presence.Detector { return ibxDetector{} }
	}
	{
		v := inboxLookPath
		save(func() { inboxLookPath = v })
		inboxLookPath = func(n string) (string, error) {
			if n == "askgate" {
				return "/opt/askgate/bin/askgate", nil
			}
			return "/usr/bin/" + n, nil
		}
	}
	{
		v1, v2, v3, v4, v5 := inboxIsTerminal, inboxStdinTerminal, inboxLocation, inboxNow, inboxHealthWait
		save(func() {
			inboxIsTerminal, inboxStdinTerminal, inboxLocation, inboxNow, inboxHealthWait = v1, v2, v3, v4, v5
		})
		inboxIsTerminal = func() bool { return false }
		inboxStdinTerminal = func() bool { return false }
		inboxLocation = time.UTC
		inboxNow = func() time.Time { return ibxT0 }
		inboxHealthWait = 300 * time.Millisecond
	}
	t.Cleanup(func() {
		for _, r := range saved {
			r()
		}
	})
	return run
}

func ibxStart(t *testing.T) *ibxDaemon {
	t.Helper()
	run := ibxEnv(t)
	d := &ibxDaemon{snap: ibxSnapshot(), events: ibxEvents()}
	d.explain = api.Explain{Session: d.snap.Sessions[0], Rule: "2: an open permission request exists", Evidence: []api.StateEvidence{
		{At: ibxT0.Add(-12 * time.Second), Layer: api.LayerHook, State: api.StateBlocked, Kind: api.BlockPermission, Detail: "PermissionRequest Bash", Fresh: true},
		{At: ibxT0.Add(-20 * time.Minute), Layer: api.LayerOSC, State: api.StateWorking, Detail: "OSC 7501 working"},
	}}
	ln, err := net.Listen("unix", filepath.Join(run, "askgate.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(run, "askgate.sock"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(d.handler())
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return d
}

func ibxSnapshot() api.Snapshot {
	exit0 := 0
	exited := ibxT0.Add(-2 * time.Hour)
	return api.Snapshot{
		Seq: 17, Machine: "box", Version: version.Version,
		Presence: api.Presence{State: api.PresencePresent, Mode: api.PresenceAuto, Source: "os_idle"},
		Sessions: []api.Session{
			{ID: "s_k3m9q2xv7a", Agent: api.AgentClaude, Mode: api.ModeWrapped, Title: "askgate", Branch: "main", Cwd: "/home/dev/src/askgate",
				State: api.StateBlocked, BlockKind: api.BlockPermission, StateLayer: api.LayerHook, StateDetail: "Asked to run git push",
				StartedAt: ibxT0.Add(-time.Hour), UpdatedAt: ibxT0.Add(-12 * time.Second)},
			{ID: "s_p4t8w6n2rb", Agent: api.AgentCodex, Mode: api.ModeObserved, Title: "webapp", Cwd: "/home/dev/src/webapp",
				State: api.StateBlocked, BlockKind: api.BlockPermission, StartedAt: ibxT0.Add(-30 * time.Minute), UpdatedAt: ibxT0.Add(-time.Minute)},
			{ID: "s_q9r7s5t3uv", Agent: api.AgentClaude, Mode: api.ModeObserved, Title: "site", Cwd: "/home/dev/src/site", Unseen: true,
				State: api.StateWaiting, LastMessage: "Done.\nWant me to add pricing?", StartedAt: ibxT0.Add(-20 * time.Minute), UpdatedAt: ibxT0.Add(-2 * time.Minute)},
			{ID: "s_z0y9x8w7vu", Agent: api.AgentClaude, Mode: api.ModeObserved, Title: "old", Cwd: "/home/dev/src/old",
				State: api.StateExited, StartedAt: ibxT0.Add(-5 * time.Hour), UpdatedAt: exited, ExitedAt: &exited, ExitCode: &exit0},
		},
		Requests: []api.Request{
			{ID: "r_7hq2m4k9pd", SessionID: "s_k3m9q2xv7a", Agent: api.AgentClaude, Checkpoint: api.CheckpointPermission, Tool: "Bash",
				ToolKind: api.ToolShell, Command: "git push origin main", Summary: "Run: git push origin main", Cwd: "/home/dev/src/askgate",
				Status: api.RequestOpen, Route: api.RouteDevices, CreatedAt: ibxT0.Add(-12 * time.Second)},
			{ID: "r_b3c5d7e9fg", SessionID: "s_p4t8w6n2rb", Agent: api.AgentCodex, Checkpoint: api.CheckpointPermission, Tool: "apply_patch",
				Paths: []string{"/home/dev/src/webapp/a.ts"}, Status: api.RequestSteppedAside, Route: api.RouteTerminal, CreatedAt: ibxT0.Add(-time.Minute)},
			{ID: "r_raced00001", SessionID: "s_q9r7s5t3uv", Agent: api.AgentClaude, Tool: "Bash", Command: "rm -rf build", Status: api.RequestOpen, CreatedAt: ibxT0.Add(-5 * time.Second)},
			{ID: "r_done000001", SessionID: "s_q9r7s5t3uv", Agent: api.AgentClaude, Tool: "Bash", Command: "ls", Status: api.RequestDecided,
				Decision: &api.Decision{Behavior: api.BehaviorAllow, By: "device:d_phone"}, CreatedAt: ibxT0.Add(-time.Hour)},
		},
	}
}

func ibxEvents() []api.Event {
	mk := func(seq int64, typ api.EventType, sid string, v any) api.Event {
		b, _ := json.Marshal(v)
		return api.Event{Seq: seq, At: ibxT0.Add(time.Duration(seq) * time.Second), Type: typ, SessionID: sid, Data: b}
	}
	s := ibxSnapshot()
	req := s.Requests[0]
	decided := req
	decided.Status = api.RequestDecided
	decided.Decision = &api.Decision{Behavior: api.BehaviorDeny, Message: "open a PR", By: "device:d_phone"}
	return []api.Event{
		mk(10, api.EventSessionStarted, "s_k3m9q2xv7a", s.Sessions[0]),
		mk(11, api.EventHook, "s_k3m9q2xv7a", adapters.HookEvent{Kind: adapters.HookPreTool, VendorEvent: "PreToolUse", Tool: "Bash", Summary: "Run: git push origin main"}),
		mk(12, api.EventRequestOpened, "s_k3m9q2xv7a", req),
		mk(13, api.EventSessionUpdated, "s_k3m9q2xv7a", s.Sessions[0]),
		mk(14, api.EventRequestClosed, "s_k3m9q2xv7a", decided),
		mk(15, api.EventPolicyDecision, "s_p4t8w6n2rb", map[string]string{"rule": "no-force-push", "behavior": "deny", "summary": "Run: git push -f"}),
		mk(16, api.EventSessionExited, "s_z0y9x8w7vu", s.Sessions[3]),
		mk(17, api.EventPresenceChanged, "", api.Presence{State: api.PresenceAway, Mode: api.PresenceForceAway, Source: "manual"}),
	}
}

func ibxRun(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	return ibxRunCtx(t, context.Background(), stdin, args...)
}

func ibxRunCtx(t *testing.T, ctx context.Context, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRoot()
	var out bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

func ibxContains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestInboxLs(t *testing.T) {
	ibxStart(t)
	out, err := ibxRun(t, "", "ls")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "NEEDS YOU (3)", "r_7hq2m4k9pd", "$ git push origin main", "askgate approve|deny r_7hq2",
		"in its terminal", "SESSIONS", "s_k3m9q2xv7a", "blocked/permission", "[attach] Asked to run git push",
		"waiting •", "Done. Want me to add pricing?")
	if strings.Contains(out, "s_z0y9x8w7vu") || strings.Contains(out, "r_done000001") {
		t.Fatalf("exited sessions/closed requests must be hidden:\n%s", out)
	}
	out, err = ibxRun(t, "", "ls", "--all")
	if err != nil || !strings.Contains(out, "s_z0y9x8w7vu") {
		t.Fatalf("--all: %v\n%s", err, out)
	}
	out, err = ibxRun(t, "", "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got lsOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(got.Sessions) != 3 || len(got.Requests) != 3 {
		t.Fatalf("json: %d sessions %d requests", len(got.Sessions), len(got.Requests))
	}
}

func TestInboxApproveDeny(t *testing.T) {
	d := ibxStart(t)
	out, err := ibxRun(t, "", "approve", "r_7hq")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "✓ Allowed r_7hq2m4k9pd once: $ git push origin main")
	out, err = ibxRun(t, "", "approve", "s_k3m", "--session")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "for the rest of this session")
	out, err = ibxRun(t, "", "deny", "r_7hq", "-m", "open a PR instead")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "✗ Denied r_7hq2m4k9pd", "The agent was told: open a PR instead")

	d.mu.Lock()
	defer d.mu.Unlock()
	want := []ibxDecideCall{
		{"r_7hq2m4k9pd", api.DecideBody{Behavior: api.BehaviorAllow, Scope: api.ScopeOnce}, "cli"},
		{"r_7hq2m4k9pd", api.DecideBody{Behavior: api.BehaviorAllow, Scope: api.ScopeSession}, "cli"},
		{"r_7hq2m4k9pd", api.DecideBody{Behavior: api.BehaviorDeny, Message: "open a PR instead"}, "cli"},
	}
	if fmt.Sprint(d.decides) != fmt.Sprint(want) {
		t.Fatalf("decides:\n%v\nwant\n%v", d.decides, want)
	}
}

func TestInboxDecideErrors(t *testing.T) {
	d := ibxStart(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"raced", []string{"approve", "r_raced"}, "already answered (the first answer wins): request already closed by device:d_phone"},
		{"stepped aside", []string{"approve", "r_b3c"}, "showing in the agent's own permission dialog — answer it in the terminal"},
		{"closed", []string{"deny", "r_done"}, "already closed (decided by device:d_phone)"},
		{"unknown", []string{"approve", "r_nope"}, `nothing matches "r_nope"`},
		{"session without request", []string{"approve", "s_z0y"}, "has no request waiting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ibxRun(t, "", tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	d.mu.Lock()
	d.refuse = true
	d.mu.Unlock()
	_, err := ibxRun(t, "", "approve", "r_7hq")
	if err == nil || !strings.Contains(err.Error(), "agents can't approve themselves") {
		t.Fatalf("403: %v", err)
	}
}

func TestInboxDaemonDown(t *testing.T) {
	ibxEnv(t) // no daemon listening
	for _, args := range [][]string{{"ls"}, {"approve", "r_1"}, {"deny", "r_1"}, {"log"}, {"explain", "s_1"}, {"here"}, {"notify", "test"}} {
		_, err := ibxRun(t, "", args...)
		if err == nil || !strings.Contains(err.Error(), "isn't running — start it with `askgate start`") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestInboxLog(t *testing.T) {
	ibxStart(t)
	out, err := ibxRun(t, "", "log")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out,
		"15:04:15  s_k3m9q2  ▶ claude started in ~/src/askgate (wrapped)",
		"15:04:16  s_k3m9q2  ↪ PreToolUse: Run: git push origin main",
		"? $ git push origin main — held for your devices",
		"· blocked (permission): Asked to run git push",
		"✗ denied by device:d_phone: $ git push origin main — “open a PR”",
		"⚖ policy rule=no-force-push behavior=deny summary=Run: git push -f",
		"■ exited with code 0",
		"-         ◐ presence: away (away, manual)",
	)
	out, err = ibxRun(t, "", "log", "--json", "-n", "2")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("-n 2 --json: %d lines\n%s", len(lines), out)
	}
	var ev api.Event
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil || ev.Seq != 17 {
		t.Fatalf("ndjson: %v %+v", err, ev)
	}
	out, err = ibxRun(t, "", "log", "--session", "s_k3m")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "s_z0y9x8") || !strings.Contains(out, "▶ claude started") {
		t.Fatalf("--session:\n%s", out)
	}
	if i, j := strings.Index(out, "▶"), strings.Index(out, "✗ denied"); i > j {
		t.Fatalf("--session must print oldest first:\n%s", out)
	}
}

func TestInboxLogFollow(t *testing.T) {
	ibxStart(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := newRoot()
	var mu sync.Mutex
	var buf bytes.Buffer
	w := ibxLockedWriter{mu: &mu, b: &buf}
	root.SetArgs([]string{"log", "-f", "-n", "1"})
	root.SetOut(w)
	root.SetErr(w)
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		s := buf.String()
		mu.Unlock()
		if strings.Contains(s, "presence: away (auto, os_idle, idle 2m00s)") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no live event:\n%s", s)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("log -f returned %v after cancel", err)
	}
}

type ibxLockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w ibxLockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func TestInboxExplain(t *testing.T) {
	ibxStart(t)
	out, err := ibxRun(t, "", "explain", "s_k3m")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "s_k3m9q2xv7a  claude · askgate · main (wrapped)", "because  2: an open permission request exists",
		"EVIDENCE", "*  12s ago", "L1-osc", "OSC 7501 working")
	if _, err := ibxRun(t, "", "explain", "s_nope"); err == nil || !strings.Contains(err.Error(), "nothing matches") {
		t.Fatalf("unknown session: %v", err)
	}
	out, err = ibxRun(t, "", "explain", "s_k3m", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("json: %v %s", err, out)
	}
}

func TestInboxPresence(t *testing.T) {
	d := ibxStart(t)
	cases := map[string]string{
		"here": "Presence: here (until you run `askgate auto`). Permission requests show in the agent's own dialog.",
		"away": "Presence: away (until you run `askgate auto`). Permission requests are held and sent to your paired devices.",
		"auto": "Presence: auto — now here (idle 4s).",
	}
	for _, mode := range []string{"here", "away", "auto"} {
		out, err := ibxRun(t, "", mode)
		if err != nil {
			t.Fatal(err)
		}
		ibxContains(t, out, cases[mode])
	}
	d.mu.Lock()
	got := strings.Join(d.presence, ",")
	d.refuse = true
	d.mu.Unlock()
	if got != "here|cli,away|cli,auto|cli" {
		t.Fatalf("presence calls = %s", got)
	}
	if _, err := ibxRun(t, "", "away"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("403: %v", err)
	}
}

func TestInboxVersionAndNotify(t *testing.T) {
	d := ibxStart(t)
	out, err := ibxRun(t, "", "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v versionInfo
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Protocol != api.ProtocolVersion || v.Version != version.Version || v.Go == "" || v.OS == "" {
		t.Fatalf("version: %v %+v", err, v)
	}
	out, _ = ibxRun(t, "", "version")
	ibxContains(t, out, "askgate "+version.Version, "protocol 1")

	out, err = ibxRun(t, "", "notify", "test")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "✓ Test notification sent to: desktop")
	if d.notified != 1 {
		t.Fatal("notify test not sent")
	}
}

func TestInboxDefaultCommand(t *testing.T) {
	ibxStart(t)
	out, err := ibxRun(t, "")
	if err != nil || !strings.Contains(out, "Usage:") {
		t.Fatalf("non-TTY default must print help: %v\n%s", err, out)
	}
	var got tui.Options
	inboxIsTerminal = func() bool { return true }
	inboxRunTUI = func(_ context.Context, o tui.Options) error { got = o; return nil }
	if _, err := ibxRun(t, ""); err != nil {
		t.Fatal(err)
	}
	if got.Backend == nil || got.Exe != "/opt/askgate/bin/askgate" {
		t.Fatalf("tui options = %+v", got)
	}
	inboxIsTerminal = func() bool { return false }
	if _, err := ibxRun(t, "", "inbox"); err == nil || !strings.Contains(err.Error(), "askgate ls") {
		t.Fatalf("inbox without a TTY: %v", err)
	}
}

// ---------------------------------------------------------------- service, doctor, setup fakes

type ibxService struct {
	mu        sync.Mutex
	kind      string
	calls     []string
	installed bool
	running   bool
	linger    *bool
}

func (s *ibxService) rec(c string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
}
func (s *ibxService) Kind() string { return s.kind }
func (s *ibxService) Path() string { return "/home/dev/Library/LaunchAgents/dev.askgate.daemon.plist" }
func (s *ibxService) Install(context.Context) (service.Result, error) {
	s.rec("install")
	changed := !s.installed
	s.installed, s.running = true, true
	res := service.Result{Path: s.Path(), Changed: changed}
	if changed {
		res.Actions = []string{"wrote " + s.Path(), "loaded dev.askgate.daemon (starts at login)"}
	} else {
		res.Actions = []string{"already loaded dev.askgate.daemon"}
	}
	return res, nil
}
func (s *ibxService) Uninstall(context.Context) (service.Result, error) {
	s.rec("uninstall")
	s.installed, s.running = false, false
	return service.Result{Path: s.Path(), Changed: true, Actions: []string{"removed " + s.Path()}}, nil
}
func (s *ibxService) Status(context.Context) (service.Status, error) {
	s.rec("status")
	return service.Status{Kind: s.kind, Path: s.Path(), Installed: s.installed, Current: s.installed, Loaded: s.installed,
		Running: s.running, PID: 4242, Linger: s.linger}, nil
}
func (s *ibxService) Start(context.Context) error   { s.rec("start"); s.running = true; return nil }
func (s *ibxService) Stop(context.Context) error    { s.rec("stop"); s.running = false; return nil }
func (s *ibxService) Restart(context.Context) error { s.rec("restart"); return nil }

type ibxDetector struct{ none bool }

func (d ibxDetector) Idle() (time.Duration, bool) {
	if d.none {
		return 0, false
	}
	return 3 * time.Second, true
}
func (d ibxDetector) Source() string {
	if d.none {
		return presence.SourceNone
	}
	return presence.SourceMacHID
}

type ibxAdapter struct {
	mu        sync.Mutex
	kind      api.AgentKind
	name      string
	installed bool // agent present on the machine
	hooks     bool // our hooks installed
	installs  []adapters.InstallOptions
}

func (a *ibxAdapter) Kind() api.AgentKind      { return a.kind }
func (a *ibxAdapter) DisplayName() string      { return a.name }
func (a *ibxAdapter) ProcessNames() []string   { return []string{string(a.kind)} }
func (a *ibxAdapter) ProtectedPaths() []string { return nil }
func (a *ibxAdapter) ResumeArgs(string) []string {
	return nil
}
func (a *ibxAdapter) Detect() adapters.Detection {
	return adapters.Detection{Installed: a.installed, Version: "2.1.0", SettingsPath: "/home/dev/.claude/settings.json"}
}
func (a *ibxAdapter) ParseHook(adapters.HookEnvelope) (adapters.HookEvent, error) {
	return adapters.HookEvent{}, nil
}
func (a *ibxAdapter) EncodeResponse(adapters.HookEvent, adapters.HookOutcome) adapters.HookResponse {
	return adapters.HookResponse{}
}
func (a *ibxAdapter) FailResponse(adapters.HookKind, bool) adapters.HookResponse {
	return adapters.HookResponse{}
}
func (a *ibxAdapter) MaxHold(adapters.HookKind) time.Duration { return 0 }
func (a *ibxAdapter) InstallHooks(o adapters.InstallOptions) (adapters.Change, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.installs = append(a.installs, o)
	ch := adapters.Change{Path: "/home/dev/.claude/settings.json", Changed: !a.hooks, Summary: "add hooks for 9 events"}
	if !o.DryRun {
		a.hooks = true
		ch.Backup = "/home/dev/.claude/settings.json.askgate-bak-1"
	}
	return ch, nil
}
func (a *ibxAdapter) UninstallHooks(adapters.InstallOptions) (adapters.Change, error) {
	return adapters.Change{}, nil
}
func (a *ibxAdapter) Status(adapters.InstallOptions) (adapters.HookStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return adapters.HookStatus{Installed: a.hooks, Current: a.hooks, Events: make([]string, 9), Path: "/home/dev/.claude/settings.json"}, nil
}

func TestInboxService(t *testing.T) {
	ibxStart(t)
	svc := &ibxService{kind: "launchd"}
	inboxServiceMgr = func(exe string) serviceManager {
		if exe != "/opt/askgate/bin/askgate" {
			t.Errorf("service exe = %s", exe)
		}
		return svc
	}
	out, err := ibxRun(t, "", "service", "install")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "✓ wrote /home/dev/Library/LaunchAgents/dev.askgate.daemon.plist", "✓ daemon running (pid 4242")
	out, err = ibxRun(t, "", "service", "status")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "manager    launchd", "installed  yes", "running    yes", "pid        4242")
	for _, sub := range []string{"stop", "start", "restart", "uninstall"} {
		if _, err := ibxRun(t, "", "service", sub); err != nil {
			t.Fatal(sub, err)
		}
	}
	if got := strings.Join(svc.calls, ","); got != "install,status,stop,start,restart,uninstall" {
		t.Fatalf("calls = %s", got)
	}
	out, err = ibxRun(t, "", "service", "status", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("status --json: %v %s", err, out)
	}

	svc.kind = ""
	if _, err := ibxRun(t, "", "service", "install"); err == nil || !strings.Contains(err.Error(), "askgate start") {
		t.Fatalf("unsupported: %v", err)
	}
	inboxExecutable = func() (string, error) { return "/tmp/go-build123/b001/exe/askgate", nil }
	if _, err := ibxRun(t, "", "service", "install"); err == nil || !strings.Contains(err.Error(), "go run") {
		t.Fatalf("go-run binary must be refused: %v", err)
	}
}

func TestInboxDoctor(t *testing.T) {
	d := ibxStart(t)
	claude := &ibxAdapter{kind: api.AgentClaude, name: "Claude Code", installed: true, hooks: true}
	codex := &ibxAdapter{kind: api.AgentCodex, name: "Codex", installed: false}
	inboxAdapters = func() []adapters.Adapter { return []adapters.Adapter{claude, nil, codex} }
	svc := &ibxService{kind: "launchd", installed: true, running: true}
	inboxServiceMgr = func(string) serviceManager { return svc }
	d.policy = api.PolicyStatus{Rules: 7, Files: []api.PolicyFile{{Path: "/x/policy.yaml", Scope: "user", Rules: 4}, {Scope: "builtin", Rules: 3}}}
	d.devices = []api.Device{{ID: "d_phone", Name: "iPhone"}}

	out, err := ibxRun(t, "", "doctor")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ibxContains(t, out,
		"✓ binary", "/opt/askgate/bin/askgate",
		"✓ daemon", "running (pid 4242",
		"✓ socket", "(0600)",
		"✓ hooks: Claude Code", "9 events wired",
		"· hooks: Codex", "agent not found",
		"✓ service", "launchd, running",
		"✓ presence", "macos_hid works",
		"✓ notifications", "1 paired device(s)",
		"· remote", "askgate remote tailscale",
		"✓ policy", "7 rules from 1 file(s)",
		"Everything looks good.",
	)

	// Broken things: hooks missing, policy error, no idle source.
	claude.hooks = false
	d.mu.Lock()
	d.policy.Errors = []string{"/x/policy.yaml: line 3: unknown action \"block\""}
	d.mu.Unlock()
	inboxDetector = func() presence.Detector { return ibxDetector{none: true} }
	out, err = ibxRun(t, "", "doctor")
	var ee ExitError
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("doctor must exit 1 on problems: %v", err)
	}
	ibxContains(t, out, "✗ hooks: Claude Code", "→ `askgate setup` or `askgate hooks install`",
		"✗ policy", "unknown action", "! presence", "askgate away", "2 problem(s)")

	out, _ = ibxRun(t, "", "doctor", "--json")
	var checks []doctorCheck
	if err := json.Unmarshal([]byte(out), &checks); err != nil || len(checks) < 8 {
		t.Fatalf("json: %v\n%s", err, out)
	}
}

func TestInboxDoctorDaemonDown(t *testing.T) {
	ibxEnv(t)
	out, err := ibxRun(t, "", "doctor")
	if err == nil {
		t.Fatal("want exit 1")
	}
	ibxContains(t, out, "✗ daemon", "not running", "→ `askgate start`")
}

func TestInboxSetup(t *testing.T) {
	d := ibxStart(t)
	claude := &ibxAdapter{kind: api.AgentClaude, name: "Claude Code", installed: true}
	codex := &ibxAdapter{kind: api.AgentCodex, name: "Codex", installed: false}
	inboxAdapters = func() []adapters.Adapter { return []adapters.Adapter{claude, codex} }
	svc := &ibxService{kind: "launchd"}
	inboxServiceMgr = func(string) serviceManager { return svc }

	// Without a terminal and without --yes, setup changes nothing.
	if _, err := ibxRun(t, "", "setup"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive: %v", err)
	}
	if len(claude.installs) != 0 {
		t.Fatal("nothing may be installed without consent")
	}

	out, err := ibxRun(t, "", "setup", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ibxContains(t, out, "binary  /opt/askgate/bin/askgate", "• Claude Code 2.1.0  add hooks for 9 events",
		"· Codex", "not found", "? Install Askgate hooks for Claude Code? [Y/n] yes",
		"backup: ~/.claude/settings.json.askgate-bak-1", "✓ loaded dev.askgate.daemon", "✓ daemon running (pid 4242",
		"✓ test notification sent", "askgate policy init", "askgate pair")
	if len(claude.installs) != 2 || !claude.installs[0].DryRun || claude.installs[1].DryRun || claude.installs[1].ShimPath != "/opt/askgate/bin/askgate" {
		t.Fatalf("installs = %+v", claude.installs)
	}
	if d.notified != 1 {
		t.Fatal("test notification not sent")
	}

	// Idempotent: second run changes nothing.
	out, err = ibxRun(t, "", "setup", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	ibxContains(t, out, "✓ Claude Code 2.1.0  hooks installed and current", "already loaded")
	if len(claude.installs) != 2 {
		t.Fatalf("second run reinstalled hooks: %+v", claude.installs)
	}

	// Interactive "no" answers skip both steps.
	claude.hooks = false
	svc.installed = false
	inboxStdinTerminal = func() bool { return true }
	out, err = ibxRun(t, "n\nn\n", "setup")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ibxContains(t, out, "hooks skipped", "login service skipped")
	if len(claude.installs) != 3 { // just the dry run
		t.Fatalf("installs = %d", len(claude.installs))
	}
}

func TestInboxSetupNoDaemon(t *testing.T) {
	ibxEnv(t)
	started := false
	inboxStartDaemon = func(context.Context, string) error { started = true; return nil }
	out, err := ibxRun(t, "", "setup", "--yes")
	if !started {
		t.Fatal("without a service manager setup must fall back to `askgate start`")
	}
	var ee ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v", err)
	}
	ibxContains(t, out, "No supported agent found yet", "the daemon isn't answering", "askgate doctor")
}

func TestFormatEventLineFallback(t *testing.T) {
	inboxLocation = time.UTC
	got := FormatEventLine(api.Event{Seq: 1, At: ibxT0, Type: api.EventDeviceChanged, Data: json.RawMessage(`{"id":"d_1"}`)})
	if got != `15:04:05  -         device.changed {"id":"d_1"}` {
		t.Fatalf("got %q", got)
	}
}
