package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xasdta/askgate/internal/adapters"
	"github.com/xasdta/askgate/internal/adapters/hookkit"
)

const userHooks = `{
  "description": "my hooks",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "/home/user/bin/lint.sh"
          }
        ]
      }
    ]
  }
}
`

const userConfig = `# my codex config
model = "gpt-5.5-codex"

[features]
hooks = true # on by default anyway
`

func fakeShim(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "askgate")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeCodexFiles(t *testing.T, home, hooks, config string) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if hooks != "" {
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(hooks), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallRoundTrip(t *testing.T) {
	home := isolate(t)
	writeCodexFiles(t, home, userHooks, userConfig)
	shim := fakeShim(t)
	a := Adapter{}
	opts := adapters.InstallOptions{ShimPath: shim}

	dry, err := a.InstallHooks(adapters.InstallOptions{ShimPath: shim, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.Changed || len(dry.More) != 1 || !strings.Contains(dry.More[0].After, "trusted_hash") {
		t.Fatalf("dry run: %+v", dry)
	}
	if read(t, HooksPath()) != userHooks || read(t, ConfigPath()) != userConfig {
		t.Fatal("dry run wrote")
	}

	ch, err := a.InstallHooks(opts)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Backup == "" || ch.More[0].Backup == "" {
		t.Errorf("backups: %+v", ch)
	}
	hooks := read(t, HooksPath())
	var doc struct {
		Description string                                  `json:"description"`
		Hooks       map[string][]map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(hooks), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Description != "my hooks" || len(doc.Hooks["PreToolUse"]) != 2 || len(doc.Hooks) != len(wiredEvents) {
		t.Errorf("hooks.json:\n%s", hooks)
	}
	config := read(t, ConfigPath())
	if !strings.HasPrefix(config, userConfig) || strings.Count(config, "[hooks.state.") != len(wiredEvents) {
		t.Errorf("config.toml:\n%s", config)
	}
	// Our PreToolUse handler sits after the user's: group 1.
	key := HooksPath() + ":pre_tool_use:1:0"
	parsed, err := parseTOML(config)
	if err != nil {
		t.Fatal(err)
	}
	want := trustHash(hookkit.Spec{Event: "PreToolUse", Matcher: "*", Command: hookkit.ShimCommand(shim, "codex"), Timeout: PreToolTimeout})
	if got, _ := trustState(parsed, key); got != want {
		t.Errorf("trust for %s = %q, want %q", key, got, want)
	}
	if _, ok := trustState(parsed, HooksPath()+":pre_tool_use:0:0"); ok {
		t.Error("trusted the user's own hook")
	}

	st, err := a.Status(opts)
	if err != nil || !st.Installed || !st.Current || len(st.Problems) != 0 {
		t.Errorf("status: %+v %v", st, err)
	}
	if again, _ := a.InstallHooks(opts); again.Changed {
		t.Errorf("second install changed: %+v", again)
	}

	un, err := a.UninstallHooks(opts)
	if err != nil || !un.Changed {
		t.Fatalf("%+v %v", un, err)
	}
	var restored, original any
	_ = json.Unmarshal([]byte(read(t, HooksPath())), &restored)
	_ = json.Unmarshal([]byte(userHooks), &original)
	if !jsonEqual(restored, original) {
		t.Errorf("hooks.json not restored:\n%s", read(t, HooksPath()))
	}
	if got := read(t, ConfigPath()); got != userConfig {
		t.Errorf("config.toml not restored:\n%q", got)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestInstallFreshHome(t *testing.T) {
	home := isolate(t)
	a := Adapter{}
	shim := fakeShim(t)
	ch, err := a.InstallHooks(adapters.InstallOptions{ShimPath: shim})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Path != filepath.Join(home, ".codex", "hooks.json") || ch.Backup != "" {
		t.Errorf("%+v", ch)
	}
	if st, _ := a.Status(adapters.InstallOptions{ShimPath: shim}); !st.Current {
		t.Errorf("status: %+v", st)
	}
	if _, err := a.UninstallHooks(adapters.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(read(t, ConfigPath())); got != "" {
		t.Errorf("config.toml after uninstall: %q", got)
	}
}

// The user's hook after ours keeps its trust when ours is removed.
func TestUninstallKeepsForeignTrust(t *testing.T) {
	home := isolate(t)
	writeCodexFiles(t, home, "", "")
	a := Adapter{}
	shim := fakeShim(t)
	if _, err := a.InstallHooks(adapters.InstallOptions{ShimPath: shim}); err != nil {
		t.Fatal(err)
	}
	// The user adds their own Stop hook after ours and trusts it in Codex.
	var doc map[string]map[string][]any
	_ = json.Unmarshal([]byte(read(t, HooksPath())), &doc)
	doc["hooks"]["Stop"] = append(doc["hooks"]["Stop"], map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "say done"}}})
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(HooksPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	userKey := HooksPath() + ":stop:1:0"
	cfg := read(t, ConfigPath()) + "\n[hooks.state." + quoteString(userKey) + "]\ntrusted_hash = \"sha256:user\"\n"
	if err := os.WriteFile(ConfigPath(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.UninstallHooks(adapters.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	out, err := parseTOML(read(t, ConfigPath()))
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := trustState(out, HooksPath()+":stop:0:0"); h != "sha256:user" {
		t.Errorf("user trust not moved to its new position:\n%s", read(t, ConfigPath()))
	}
	if len(out.tables) != 1 {
		t.Errorf("leftover tables:\n%s", read(t, ConfigPath()))
	}
}

func TestInstallUntrustedWhenLayoutUnsupported(t *testing.T) {
	home := isolate(t)
	cfg := "[hooks]\nstate = {}\n"
	writeCodexFiles(t, home, "", cfg)
	a := Adapter{}
	shim := fakeShim(t)
	ch, err := a.InstallHooks(adapters.InstallOptions{ShimPath: shim})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ch.Summary, "not trusted automatically") || len(ch.More) != 0 {
		t.Errorf("%+v", ch)
	}
	if read(t, ConfigPath()) != cfg {
		t.Error("config.toml touched")
	}
	st, _ := a.Status(adapters.InstallOptions{ShimPath: shim})
	if st.Current || len(st.Problems) == 0 || !strings.Contains(st.Problems[0], "trusted") {
		t.Errorf("status: %+v", st)
	}
}

func TestInstallRefusesInvalidHooksJSON(t *testing.T) {
	home := isolate(t)
	writeCodexFiles(t, home, "{nope", userConfig)
	a := Adapter{}
	if _, err := a.InstallHooks(adapters.InstallOptions{ShimPath: fakeShim(t)}); err == nil {
		t.Fatal("invalid hooks.json accepted")
	}
	if read(t, HooksPath()) != "{nope" || read(t, ConfigPath()) != userConfig {
		t.Error("files touched")
	}
}

func TestStatusFeatureOff(t *testing.T) {
	home := isolate(t)
	writeCodexFiles(t, home, "", "[features]\nhooks = false\n")
	a := Adapter{}
	shim := fakeShim(t)
	if _, err := a.InstallHooks(adapters.InstallOptions{ShimPath: shim}); err != nil {
		t.Fatal(err)
	}
	st, _ := a.Status(adapters.InstallOptions{ShimPath: shim})
	if st.Current || len(st.Problems) != 1 || !strings.Contains(st.Problems[0], "hooks = false") {
		t.Errorf("%+v", st)
	}
}
