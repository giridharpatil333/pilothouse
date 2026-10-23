//go:build devserver

// Command devserver runs the web app against an in-memory fake core with
// realistic sample sessions and requests, for UI work and screenshots:
//
//	go run -tags devserver ./internal/web/devserver -addr 127.0.0.1:7600 [-demo]
//
// It prints a one-time login link. Everything lives in a temp dir
// (PILOTHOUSE_HOME is overridden); nothing touches ~/.pilothouse.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xasdta/pilothouse/internal/adapters"
	"github.com/xasdta/pilothouse/internal/api"
	"github.com/xasdta/pilothouse/internal/config"
	"github.com/xasdta/pilothouse/internal/web"
	"github.com/xasdta/pilothouse/internal/web/webtest"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7600", "listen address")
	demo := flag.Bool("demo", false, "keep generating new requests and activity")
	flag.Parse()

	dir, err := os.MkdirTemp("", "pilothouse-dev")
	must(err)
	defer os.RemoveAll(dir)
	must(os.Setenv("PILOTHOUSE_HOME", dir))
	must(os.Setenv("PILOTHOUSE_USER_HOME", dir))

	db, err := sql.Open("sqlite", filepath.Join(dir, "dev.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	must(err)
	defer db.Close()

	fc := webtest.New()
	fc.Machine = "studio"
	seed(fc)

	cfg := config.Default()
	cfg.Web.Addr = *addr
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := web.New(web.Deps{
		Core: fc, DB: db, Config: cfg, Log: log,
		LocalGuard: func(h http.Handler) http.Handler { return h },
		KeysDir:    filepath.Join(dir, "keys"),
	})
	must(err)

	local := http.NewServeMux()
	srv.RegisterLocal(local)
	rec := httptest.NewRecorder()
	local.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/login-link", nil))
	var link api.LoginLink
	must(json.Unmarshal(rec.Body.Bytes(), &link))
	rec = httptest.NewRecorder()
	local.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/pair", nil))
	var pair api.PairStart
	must(json.Unmarshal(rec.Body.Bytes(), &pair))
	fmt.Printf("pilothouse devserver on http://%s\n  login: %s\n  pair:  %s\n", *addr, link.URL, pair.URL)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *demo {
		go runDemo(ctx, fc)
	}
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = hs.Shutdown(context.Background())
	}()
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		must(err)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "devserver:", err)
		os.Exit(1)
	}
}

func ptr[T any](v T) *T { return &v }

// seed builds a believable afternoon: two requests waiting on the phone,
// one question in a terminal, agents working and waiting.
func seed(fc *webtest.FakeCore) {
	now := time.Now()
	at := now
	fc.Now = func() time.Time { return at }
	defer func() { fc.Now = time.Now }()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	hook := func(sid string, when time.Duration, ev adapters.HookEvent) {
		at = ago(when)
		ev.Cwd = "/Users/dev/src/pilothouse"
		_, _ = fc.Emit(context.Background(), api.EventHook, sid, "", ev)
	}

	at = ago(48 * time.Minute)
	fc.PutSession(api.Session{ID: "s_pilothouse01", Agent: api.AgentClaude, Mode: api.ModeWrapped, Machine: "studio", Title: "pilothouse",
		Cwd: "/Users/dev/src/pilothouse", RepoRoot: "/Users/dev/src/pilothouse", Branch: "main", State: api.StateWorking,
		StartedAt: ago(48 * time.Minute), UpdatedAt: ago(48 * time.Minute)})
	hook("s_pilothouse01", 47*time.Minute, adapters.HookEvent{Kind: adapters.HookSessionStart, VendorEvent: "SessionStart"})
	hook("s_pilothouse01", 46*time.Minute, adapters.HookEvent{Kind: adapters.HookPromptSubmit, Message: "Finish the web inbox, make sure every test passes, then push to main."})
	hook("s_pilothouse01", 41*time.Minute, adapters.HookEvent{Kind: adapters.HookPreTool, Tool: "Read", ToolKind: api.ToolRead, Paths: []string{"/Users/dev/src/pilothouse/internal/web/web.go"}, Summary: "Read web.go"})
	hook("s_pilothouse01", 33*time.Minute, adapters.HookEvent{Kind: adapters.HookPreTool, Tool: "Edit", ToolKind: api.ToolEdit, Paths: []string{"/Users/dev/src/pilothouse/internal/web/static/js/card.js"}, Summary: "Edit card.js"})
	hook("s_pilothouse01", 12*time.Minute, adapters.HookEvent{Kind: adapters.HookPreTool, Tool: "Bash", ToolKind: api.ToolShell, Command: "go test -race ./internal/web/...", Summary: "Run tests"})
	hook("s_pilothouse01", 4*time.Minute, adapters.HookEvent{Kind: adapters.HookPreTool, Tool: "Bash", ToolKind: api.ToolShell, Command: `git commit -am "web: optimistic decisions with rollback"`, Summary: "Commit"})

	at = ago(95 * time.Minute)
	fc.PutSession(api.Session{ID: "s_proofline", Agent: api.AgentClaude, Mode: api.ModeObserved, Machine: "studio", Title: "proofline",
		Cwd: "/Users/dev/src/proofline", RepoRoot: "/Users/dev/src/proofline", Branch: "fix/webhook-retries", State: api.StateWorking,
		StartedAt: ago(95 * time.Minute), UpdatedAt: ago(95 * time.Minute)})
	at = ago(11 * time.Minute)
	fc.PutRequest(api.Request{ID: "r_npmtest01", SessionID: "s_proofline", Agent: api.AgentClaude, Checkpoint: api.CheckpointPermission,
		Tool: "Bash", ToolKind: api.ToolShell, Command: "pnpm test --filter webhooks", Summary: "Run: pnpm test --filter webhooks", Cwd: "/Users/dev/src/proofline",
		Status: api.RequestDecided, Route: api.RouteDevices, CreatedAt: ago(12 * time.Minute), ExpiresAt: ago(3 * time.Minute), ClosedAt: ptr(ago(11 * time.Minute)),
		Decision: &api.Decision{RequestID: "r_npmtest01", Behavior: api.BehaviorAllow, Scope: api.ScopeSession, By: "device:d_other0001", At: ago(11 * time.Minute)}})
	at = ago(3 * time.Minute)
	fc.PutSession(api.Session{ID: "s_proofline", Agent: api.AgentClaude, Mode: api.ModeObserved, Machine: "studio", Title: "proofline",
		Cwd: "/Users/dev/src/proofline", RepoRoot: "/Users/dev/src/proofline", Branch: "fix/webhook-retries", State: api.StateWaiting,
		LastMessage: "Done. Retries now back off exponentially (1s, 4s, 16s) and stop after three attempts. Added a test for the ceiling; all 48 tests pass.",
		Unseen:      true, StartedAt: ago(95 * time.Minute), UpdatedAt: ago(3 * time.Minute)})

	at = ago(40 * time.Minute)
	fc.PutRequest(api.Request{ID: "r_curlsh002", SessionID: "s_proofline", Agent: api.AgentClaude, Checkpoint: api.CheckpointPermission,
		Tool: "Bash", ToolKind: api.ToolShell, Command: "curl -fsSL https://get.example.dev/install.sh | sh", Summary: "Run: curl … | sh", Cwd: "/Users/dev/src/proofline",
		Status: api.RequestDecided, CreatedAt: ago(41 * time.Minute), ExpiresAt: ago(32 * time.Minute), ClosedAt: ptr(ago(40 * time.Minute)),
		Decision: &api.Decision{RequestID: "r_curlsh002", Behavior: api.BehaviorDeny, Message: "No piping scripts into sh.", By: "cli", At: ago(40 * time.Minute)}})

	at = ago(26 * time.Minute)
	fc.PutSession(api.Session{ID: "s_site000002", Agent: api.AgentCodex, Mode: api.ModeObserved, Machine: "studio", Title: "site",
		Cwd: "/Users/dev/src/site", RepoRoot: "/Users/dev/src/site", Branch: "feat/pricing-page", State: api.StateWorking,
		StateDetail: "Running: npm run build", StartedAt: ago(26 * time.Minute), UpdatedAt: ago(8 * time.Second)})
	at = ago(14 * time.Minute)
	fc.PutSession(api.Session{ID: "s_apisrv003", Agent: api.AgentClaude, Mode: api.ModeWrapped, Machine: "studio", Title: "api-server",
		Cwd: "/Users/dev/src/api-server", RepoRoot: "/Users/dev/src/api-server", Branch: "perf/lru-cache", State: api.StateWorking,
		StateDetail: "Editing internal/cache/lru.go", StartedAt: ago(14 * time.Minute), UpdatedAt: ago(40 * time.Second)})
	at = ago(7 * time.Minute)
	fc.PutSession(api.Session{ID: "s_notes0004", Agent: api.AgentCodex, Mode: api.ModeObserved, Machine: "studio", Title: "notes",
		Cwd: "/Users/dev/src/notes", RepoRoot: "/Users/dev/src/notes", Branch: "main", State: api.StateBlocked, BlockKind: api.BlockQuestion,
		StateDetail: "Asked which search index to use", Unseen: true, StartedAt: ago(30 * time.Minute), UpdatedAt: ago(7 * time.Minute)})
	at = ago(2 * time.Hour)
	fc.PutSession(api.Session{ID: "s_dotfile05", Agent: api.AgentClaude, Mode: api.ModeObserved, Machine: "studio", Title: "dotfiles",
		Cwd: "/Users/dev/dotfiles", State: api.StateExited, StartedAt: ago(3 * time.Hour), UpdatedAt: ago(2 * time.Hour), ExitedAt: ptr(ago(2 * time.Hour)), ExitCode: ptr(0)})

	at = ago(36 * time.Minute)
	fc.PutSession(api.Session{ID: "s_infra0006", Agent: api.AgentClaude, Mode: api.ModeObserved, Machine: "studio", Title: "infra",
		Cwd: "/Users/dev/src/infra", RepoRoot: "/Users/dev/src/infra", Branch: "chore/bump-node", State: api.StateWorking,
		StartedAt: ago(36 * time.Minute), UpdatedAt: ago(36 * time.Minute)})

	// Open requests last, so they're the newest events.
	at = ago(70 * time.Second)
	fc.PutSession(api.Session{ID: "s_pilothouse01", Agent: api.AgentClaude, Mode: api.ModeWrapped, Machine: "studio", Title: "pilothouse",
		Cwd: "/Users/dev/src/pilothouse", RepoRoot: "/Users/dev/src/pilothouse", Branch: "main", State: api.StateBlocked, BlockKind: api.BlockPermission,
		StateDetail: "Asked to run git push origin main", Unseen: true, StartedAt: ago(48 * time.Minute), UpdatedAt: ago(70 * time.Second)})
	fc.PutRequest(api.Request{ID: "r_gitpush03", SessionID: "s_pilothouse01", Agent: api.AgentClaude, Checkpoint: api.CheckpointPermission,
		Tool: "Bash", ToolKind: api.ToolShell, Command: "git push origin main", Summary: "Run: git push origin main", Cwd: "/Users/dev/src/pilothouse",
		Status: api.RequestOpen, Route: api.RouteDevices, PolicyRule: "ask-before-push", CreatedAt: ago(70 * time.Second), ExpiresAt: now.Add(7*time.Minute + 50*time.Second)})
	at = ago(25 * time.Second)
	fc.PutSession(api.Session{ID: "s_infra0006", Agent: api.AgentClaude, Mode: api.ModeObserved, Machine: "studio", Title: "infra",
		Cwd: "/Users/dev/src/infra", RepoRoot: "/Users/dev/src/infra", Branch: "chore/bump-node", State: api.StateBlocked, BlockKind: api.BlockPermission,
		StateDetail: "Asked to edit 2 files", StartedAt: ago(36 * time.Minute), UpdatedAt: ago(25 * time.Second)})
	fc.PutRequest(api.Request{ID: "r_tfedit04", SessionID: "s_infra0006", Agent: api.AgentClaude, Checkpoint: api.CheckpointPermission,
		Tool: "Edit", ToolKind: api.ToolEdit, Paths: []string{"/Users/dev/src/infra/terraform/prod/main.tf", "/Users/dev/src/infra/.github/workflows/deploy.yml"},
		Summary: "Edit: terraform/prod/main.tf", Cwd: "/Users/dev/src/infra",
		Status: api.RequestOpen, Route: api.RouteDevices, PolicyRule: "protect-prod", CreatedAt: ago(25 * time.Second), ExpiresAt: now.Add(8*time.Minute + 35*time.Second)})
	_ = fc.SetPresenceMode(context.Background(), api.PresenceForceAway)
}

// runDemo adds a new request every 20 s and nudges the working sessions.
func runDemo(ctx context.Context, fc *webtest.FakeCore) {
	cmds := []string{"npm publish --access public", "kubectl rollout restart deploy/api -n prod", "rm -rf ./dist && make release", "docker push ghcr.io/dev/api:latest"}
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		id := fmt.Sprintf("r_demo%05d", i)
		cmd := cmds[i%len(cmds)]
		fc.PutRequest(api.Request{ID: id, SessionID: "s_site000002", Agent: api.AgentCodex, Checkpoint: api.CheckpointPermission,
			Tool: "shell", ToolKind: api.ToolShell, Command: cmd, Summary: "Run: " + cmd, Cwd: "/Users/dev/src/site",
			Status: api.RequestOpen, Route: api.RouteDevices, CreatedAt: now, ExpiresAt: now.Add(2 * time.Minute)})
	}
}
