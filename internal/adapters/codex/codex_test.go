package codex

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xasdta/askgate/internal/adapters"
	"github.com/xasdta/askgate/internal/adapters/hookkit"
	"github.com/xasdta/askgate/internal/api"
)

var update = flag.Bool("update", false, "rewrite golden files")

// isolate points every Askgate path at temp dirs (BUILD-SPEC invariant 1).
// CODEX_HOME is set to a decoy too: ASKGATE_USER_HOME must win over it.
func isolate(t *testing.T) (userHome string) {
	t.Helper()
	userHome = t.TempDir()
	t.Setenv("ASKGATE_HOME", t.TempDir())
	t.Setenv("ASKGATE_USER_HOME", userHome)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "decoy"))
	run, err := os.MkdirTemp("/tmp", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	t.Setenv("ASKGATE_RUN_DIR", run)
	return userHome
}

// TestParseHookGolden: fixtures are built from Codex's generated input
// schemas (codex-rs/hooks/schema/generated) and the payload builders in
// codex-rs/core/src/tools (Codex is not installed where these were made).
func TestParseHookGolden(t *testing.T) {
	files, _ := filepath.Glob("testdata/hooks/*.json")
	if len(files) < 15 {
		t.Fatalf("only %d fixtures", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			payload, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			ev, err := Adapter{}.ParseHook(adapters.HookEnvelope{Agent: api.AgentCodex, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(ev); err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test -update)", err)
			}
			if !bytes.Equal(buf.Bytes(), want) {
				t.Errorf("mismatch:\ngot:\n%s\nwant:\n%s", buf.Bytes(), want)
			}
		})
	}
}

func parseFixture(t *testing.T, name string) adapters.HookEvent {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "hooks", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := Adapter{}.ParseHook(adapters.HookEnvelope{Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestParseHookKeyFields(t *testing.T) {
	for _, tc := range []struct {
		fixture, summary string
		kind             adapters.HookKind
		tool             api.ToolKind
		paths            []string
	}{
		{"pre_tool_bash", "Run: git push origin main", adapters.HookPreTool, api.ToolShell, nil},
		{"pre_tool_apply_patch", "Edit: src/app.go, docs/new.md, old.txt (+2 more)", adapters.HookPreTool, api.ToolEdit,
			[]string{"/home/user/project/src/app.go", "/home/user/project/docs/new.md", "/home/user/project/old.txt", "/home/user/project/lib/x.go", "/home/user/project/lib/y.go"}},
		{"pre_tool_mcp", "MCP memory: create_entities", adapters.HookPreTool, api.ToolMCP, nil},
		{"pre_tool_spawn_agent", "spawn_agent: Review the diff for bugs", adapters.HookPreTool, api.ToolOther, nil},
		{"permission_bash", "Run: npm install left-pad", adapters.HookPermission, api.ToolShell, nil},
		{"permission_apply_patch", "Edit: /etc/hosts.new", adapters.HookPermission, api.ToolEdit, []string{"/etc/hosts.new"}},
		{"permission_write_stdin", "Type into running command: rm -rf build", adapters.HookPermission, api.ToolShell, nil},
		{"post_tool_bash", "Run: go test ./...", adapters.HookPostTool, api.ToolShell, nil},
		{"stop", "Finished: Fixed the test; all packages pass.", adapters.HookStop, "", nil},
		{"stop_null_message", "Finished its turn", adapters.HookStop, "", nil},
		{"interrupt", "Interrupted", adapters.HookStop, "", nil},
		{"subagent_stop", "Subagent finished (reviewer)", adapters.HookSubagentStop, "", nil},
		{"session_start", "Session started", adapters.HookSessionStart, "", nil},
		{"session_start_null_transcript", "Session started (resume)", adapters.HookSessionStart, "", nil},
		{"session_end", "Session ended", adapters.HookSessionEnd, "", nil},
		{"pre_compact", "Compacting context (auto)", adapters.HookPreCompact, "", nil},
		{"unknown_post_model_request", "PostModelRequest", adapters.HookUnknown, "", nil},
	} {
		ev := parseFixture(t, tc.fixture)
		if ev.Kind != tc.kind || ev.ToolKind != tc.tool || ev.Summary != tc.summary {
			t.Errorf("%s: kind=%s tool=%s summary=%q", tc.fixture, ev.Kind, ev.ToolKind, ev.Summary)
		}
		if strings.Join(ev.Paths, ",") != strings.Join(tc.paths, ",") {
			t.Errorf("%s: paths=%v", tc.fixture, ev.Paths)
		}
	}
	ev := parseFixture(t, "pre_tool_bash")
	if ev.Command != "git push origin main" || ev.ToolUseID != "call_abc123" || ev.AgentSessionID == "" || !strings.Contains(ev.TranscriptPath, "rollout-") {
		t.Errorf("fields: %+v", ev)
	}
	if ev := parseFixture(t, "session_start_null_transcript"); ev.TranscriptPath != "" || ev.PermissionMode != "bypassPermissions" {
		t.Errorf("null transcript: %+v", ev)
	}
}

func TestEncodeResponse(t *testing.T) {
	a := Adapter{}
	ev := func(k adapters.HookKind) adapters.HookEvent { return adapters.HookEvent{Kind: k} }
	for _, tc := range []struct {
		name string
		ev   adapters.HookEvent
		out  adapters.HookOutcome
		want string
	}{
		{"pre deny", ev(adapters.HookPreTool), adapters.HookOutcome{Behavior: api.BehaviorDeny, Message: "No force-push."},
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"No force-push."}}`},
		{"pre deny needs a reason", ev(adapters.HookPreTool), adapters.HookOutcome{Behavior: api.BehaviorDeny},
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Blocked by Askgate policy."}}`},
		{"pre allow unsupported", ev(adapters.HookPreTool), adapters.HookOutcome{Behavior: api.BehaviorAllow, Message: "ok"}, ``},
		{"pre ask unsupported", ev(adapters.HookPreTool), adapters.HookOutcome{Behavior: api.BehaviorAsk, Message: "ask"}, ``},
		{"permission allow", ev(adapters.HookPermission), adapters.HookOutcome{Behavior: api.BehaviorAllow},
			`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`},
		{"permission deny", ev(adapters.HookPermission), adapters.HookOutcome{Behavior: api.BehaviorDeny, Message: "Denied from phone"},
			`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Denied from phone"}}}`},
		{"permission none", ev(adapters.HookPermission), adapters.HookOutcome{}, ``},
		{"session context", ev(adapters.HookSessionStart), adapters.HookOutcome{Context: "ctx"},
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"ctx"}}`},
		{"stop", ev(adapters.HookStop), adapters.HookOutcome{Behavior: api.BehaviorDeny}, ``},
	} {
		got := a.EncodeResponse(tc.ev, tc.out)
		if strings.TrimSuffix(got.Stdout, "\n") != tc.want || got.ExitCode != 0 {
			t.Errorf("%s: got %q\nwant %s", tc.name, got.Stdout, tc.want)
		}
	}
	if r := a.FailResponse(adapters.HookPreTool, true); !strings.Contains(r.Stdout, `"deny"`) {
		t.Errorf("fail closed: %+v", r)
	}
	if r := a.FailResponse(adapters.HookPermission, true); r.Stdout != "" {
		t.Errorf("fail closed permission: %+v", r)
	}
}

func TestMetadata(t *testing.T) {
	home := isolate(t)
	a := Adapter{}
	if Home() != filepath.Join(home, ".codex") {
		t.Errorf("Home = %s (CODEX_HOME must not win under ASKGATE_USER_HOME)", Home())
	}
	if a.MaxHold(adapters.HookPermission) != 600*time.Second || a.MaxHold(adapters.HookPreTool) != 0 {
		t.Error("MaxHold")
	}
	if strings.Join(a.ResumeArgs("019a"), " ") != "codex resume 019a" || a.ResumeArgs("") != nil {
		t.Error("ResumeArgs")
	}
	if pp := a.ProtectedPaths(); pp[0] != filepath.Join(home, ".codex", "config.toml") || len(pp) != 4 {
		t.Errorf("ProtectedPaths = %v", pp)
	}
	if a.Kind() != api.AgentCodex || a.ProcessNames()[0] != "codex" {
		t.Error("identity")
	}
}

func TestHomeHonorsCodexHomeOutsideTests(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASKGATE_USER_HOME", "")
	t.Setenv("CODEX_HOME", link)
	want, _ := filepath.EvalSymlinks(real)
	if Home() != want {
		t.Errorf("Home = %s, want canonical %s", Home(), want)
	}
}

// Vectors from Codex's own test suite (codex-rs/core/tests/suite/hooks.rs,
// declaration-only trust for a plugin's hooks.json).
func TestTrustHashMatchesCodex(t *testing.T) {
	cmd := "python3 -I -S ${PLUGIN_ROOT}/hooks/pre_tool_use_hook.py"
	for event, want := range map[string]string{
		"PreToolUse":  "sha256:c9ee1aca1b5d8c72b99df6571c87d61350b09f7f4594ae30e7250a2027e9d76b",
		"PostToolUse": "sha256:9eef43ad6f26b1f9b94f318a50445f8bb62c363da08152a2966b73d308bf42ba",
	} {
		if got := trustHash(hookkit.Spec{Event: event, Matcher: "^Bash$", Command: cmd}); got != want {
			t.Errorf("%s: %s, want %s", event, got, want)
		}
	}
	// Matchers are dropped for events without matcher support, and the
	// SessionEnd timeout is clamped, as Codex normalizes before hashing.
	a := trustHash(hookkit.Spec{Event: "Stop", Matcher: "*", Command: "x"})
	if b := trustHash(hookkit.Spec{Event: "Stop", Command: "x"}); a != b {
		t.Error("Stop matcher not ignored")
	}
	if trustHash(hookkit.Spec{Event: "SessionEnd", Command: "x", Timeout: 9}) != trustHash(hookkit.Spec{Event: "SessionEnd", Command: "x", Timeout: 3}) {
		t.Error("SessionEnd timeout not clamped")
	}
	if trustHash(hookkit.Spec{Event: "PreToolUse", Command: "a<b"}) == "" {
		t.Error("empty hash")
	}
}

func TestTrustKey(t *testing.T) {
	got := trustKey("/home/u/.codex/hooks.json", hookkit.Ref{Event: "PermissionRequest", Group: 2, Handler: 0})
	if got != "/home/u/.codex/hooks.json:permission_request:2:0" {
		t.Error(got)
	}
}
