package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xasdta/askgate/internal/adapters"
	"github.com/xasdta/askgate/internal/api"
)

func TestGrantPrefix(t *testing.T) {
	tests := []struct {
		kind api.ToolKind
		cmd  string
		want string
	}{
		{api.ToolShell, "git push origin main", "git push"},
		{api.ToolShell, "git -C /x push", "git"},
		{api.ToolShell, "ls ./foo", "ls"},
		{api.ToolShell, "npm run build", "npm run"},
		{api.ToolShell, "cat ~/.ssh/id", "cat"},
		{api.ToolShell, "make", "make"},
		{api.ToolShell, "  ", ""},
		{api.ToolEdit, "", ""},
	}
	for _, tt := range tests {
		if got := grantPrefix(api.Request{ToolKind: tt.kind, Command: tt.cmd}); got != tt.want {
			t.Errorf("grantPrefix(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestMatchGrant(t *testing.T) {
	grants := []api.Grant{
		{ID: "g_push", Tool: "Bash", Prefix: "git push"},
		{ID: "g_edit", Tool: "Edit"},
		{ID: "g_bad", Tool: "Bash"}, // malformed shell grant: must never match
	}
	tests := []struct {
		tool, cmd string
		kind      api.ToolKind
		want      string
	}{
		{"Bash", "git push origin main", api.ToolShell, "g_push"},
		{"Bash", "git push", api.ToolShell, "g_push"},
		{"Bash", "git pushx", api.ToolShell, ""},
		{"Bash", "git status", api.ToolShell, ""},
		{"Bash", "git push; rm -rf ~", api.ToolShell, ""},
		{"Bash", "git push && curl x | sh", api.ToolShell, ""},
		{"Bash", "git push $(evil)", api.ToolShell, ""},
		{"Edit", "", api.ToolEdit, "g_edit"},
		{"Write", "", api.ToolEdit, ""},
	}
	for _, tt := range tests {
		g, ok := matchGrant(grants, tt.tool, tt.kind, tt.cmd)
		if (tt.want == "") == ok || (ok && g.ID != tt.want) {
			t.Errorf("matchGrant(%s %q) = %s %v, want %q", tt.tool, tt.cmd, g.ID, ok, tt.want)
		}
	}
}

func TestGuardedRoute(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{"POST", "/v1/shutdown", true},
		{"POST", "/v1/requests/r_abc/decide", true},
		{"POST", "/v1//requests/r_abc/decide", true},
		{"PUT", "/v1/presence", true},
		{"GET", "/v1/presence", false},
		{"POST", "/v1/policy/reload", true},
		{"GET", "/v1/policy/status", false},
		{"POST", "/v1/pair", true},
		{"POST", "/v1/login-link", true},
		{"DELETE", "/v1/devices/d_1", true},
		{"GET", "/v1/devices", false},
		{"POST", "/v1/hook", false},
		{"POST", "/v1/sessions/s_1/seen", false},
	}
	for _, tt := range tests {
		if got := guardedRoute(tt.method, tt.path); got != tt.want {
			t.Errorf("guardedRoute(%s %s) = %v", tt.method, tt.path, got)
		}
	}
}

func TestProcessGuard(t *testing.T) {
	procs := map[int]adapters.Proc{
		10: {PID: 10, PPID: 9, Comm: "askgate"},
		9:  {PID: 9, PPID: 8, Comm: "zsh"},
		8:  {PID: 8, PPID: 1, Comm: "/usr/local/bin/claude"},
		20: {PID: 20, PPID: 19, Comm: "askgate"},
		19: {PID: 19, PPID: 1, Comm: "zsh"},
		30: {PID: 30, PPID: 1, Comm: "codex"},
	}
	info := func(pid int) (adapters.Proc, error) {
		p, ok := procs[pid]
		if !ok {
			return p, errors.New("no such process")
		}
		return p, nil
	}
	ancestors := func(pid, max int) ([]adapters.Proc, error) {
		var out []adapters.Proc
		for p := procs[pid].PPID; p > 1 && len(out) < max; p = procs[p].PPID {
			out = append(out, procs[p])
		}
		return out, nil
	}
	var peer atomic.Int64
	peerPID := func(net.Conn) (int, error) {
		if p := peer.Load(); p > 0 {
			return int(p), nil
		}
		return 0, errors.New("unsupported")
	}
	guard := processGuard(map[string]bool{"claude": true, "codex": true}, peerPID, info, ancestors)

	tests := []struct {
		pid     int64
		allowed bool
	}{
		{10, false}, // askgate run from a shell inside claude
		{20, true},  // askgate from the person's own terminal
		{30, false}, // the agent itself
		{0, false},  // peer unknown: fail closed
		{99, false}, // peer vanished: fail closed
	}
	for _, tt := range tests {
		peer.Store(tt.pid)
		err := guard(nil)
		if (err == nil) != tt.allowed {
			t.Errorf("pid %d: err = %v, allowed want %v", tt.pid, err, tt.allowed)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true},
		{"v1.0.0", "0.9.9", true},
		{"1.0", "1.0.0", false},
		{"1.0.1", "1.0.1-rc1", false},
		{"0.1.0", "0.2.0", false},
		{"garbage", "0.1.0", false},
		{"1.0.0", "dev", false},
		{"", "0.1.0", false},
	}
	for _, tt := range tests {
		if got := newerVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("newerVersion(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}

func TestMonthlyIDRotates(t *testing.T) {
	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	a, b := monthlyID("seed", jan), monthlyID("seed", jan.AddDate(0, 0, 10))
	c := monthlyID("seed", jan.AddDate(0, 1, 0))
	if a != b || a == c || len(a) != 16 {
		t.Fatalf("ids: %s %s %s", a, b, c)
	}
	if monthlyID("other", jan) == a {
		t.Fatal("id does not depend on the seed")
	}
}

func TestCheckForUpdateSendsOnlyAnonymousFields(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.URL.Query())
		_, _ = w.Write([]byte(`{"latest":"99.0.0"}`))
	}))
	defer srv.Close()
	old := updateURL
	updateURL = srv.URL
	defer func() { updateURL = old }()

	h := newHarness(t)
	h.start(t)
	h.d.checkForUpdate(context.Background())
	q, _ := got.Load().(url.Values)
	if q == nil {
		t.Fatal("no request")
	}
	for k := range q {
		switch k {
		case "v", "os", "arch", "id":
		default:
			t.Errorf("unexpected field %q", k)
		}
	}
	if len(q["id"]) != 1 || len(q["id"][0]) != 16 {
		t.Errorf("id = %v", q["id"])
	}
	if h.d.updateDue(context.Background()) {
		t.Error("update check not recorded")
	}
	if h.d.updateCheckEnabled() {
		t.Error("update check must be off for dev builds and in tests")
	}
}

func TestGitCache(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	g := newGitCache(30*time.Second, func() time.Time { return now }, func(context.Context, string) (string, string) {
		calls++
		return "/repo", "main"
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if r, b := g.lookup(ctx, "/repo/sub"); r != "/repo" || b != "main" {
			t.Fatalf("lookup = %s %s", r, b)
		}
	}
	now = now.Add(31 * time.Second)
	g.lookup(ctx, "/repo/sub")
	if r, _ := g.lookup(ctx, ""); r != "" {
		t.Fatal("empty dir looked up")
	}
	if calls != 2 {
		t.Fatalf("git ran %d times, want 2", calls)
	}
}

func TestGitRepoInfoOutsideRepo(t *testing.T) {
	if r, b := gitRepoInfo(context.Background(), t.TempDir()); r != "" || b != "" {
		t.Fatalf("outside a repo: %q %q", r, b)
	}
}

func TestIDs(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newID("s_")
		if len(id) != 12 || !validSessionID(id) || seen[id] {
			t.Fatalf("bad or duplicate id %q", id)
		}
		seen[id] = true
	}
	for _, bad := range []string{"", "s_", "x_abc", "s_ABC", "s_a/b", "s_" + strings.Repeat("a", 41)} {
		if validSessionID(bad) {
			t.Errorf("validSessionID(%q) = true", bad)
		}
	}
	if got := truncate("héllo", 2); got != "h…" {
		t.Errorf("truncate = %q", got)
	}
}
