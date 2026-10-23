// Package codex adapts the OpenAI Codex CLI through its lifecycle hooks.
//
// Codex (github.com/openai/codex, codex-rs/hooks) runs Claude-style hooks
// declared in ~/.codex/hooks.json or [hooks] in config.toml: SessionStart,
// SessionEnd, UserPromptSubmit, PreToolUse, PermissionRequest,
// PostToolUse, Stop, SubagentStart/Stop, Pre/PostCompact, Interrupt and
// model-request events. The feature flag `hooks` is stable and on by
// default (codex-rs/features/src/lib.rs). What Askgate gets from it:
//
//   - PreToolUse can block (permissionDecision "deny" with a reason).
//     "allow" without updatedInput and "ask" are rejected by Codex as
//     unsupported (hooks/src/engine/output_parser.rs), so policy allow/ask
//     rules are no-ops on Codex: its own approval policy decides.
//   - PermissionRequest runs before Codex shows its approval prompt and
//     may answer allow or deny (decision.behavior), so remote approvals work.
//   - Hooks from user files run only once trusted (see trust.go);
//     InstallHooks records that trust in config.toml.
//
// docs/AGENTS.md has the full comparison with Claude Code.
package codex

import (
	"os"
	"path/filepath"
	"time"

	"github.com/xasdta/askgate/internal/adapters"
	"github.com/xasdta/askgate/internal/adapters/hookkit"
	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/paths"
)

// PermissionTimeout is the PermissionRequest hook timeout we install
// (Codex's default for command hooks is the same 600 s).
const PermissionTimeout = 600

// PreToolTimeout bounds PreToolUse; the daemon answers in milliseconds.
const PreToolTimeout = 30

// shortTimeout is for SessionEnd and Interrupt, which Codex clamps to 3 s.
const shortTimeout = 3

// Adapter is the Codex CLI.
type Adapter struct{}

// New returns the Codex adapter.
func New() adapters.Adapter { return Adapter{} }

func (Adapter) Kind() api.AgentKind { return api.AgentCodex }
func (Adapter) DisplayName() string { return "Codex" }

// ProcessNames: the npm launcher (node) spawns the native `codex` binary,
// so the native process is always in a hook's ancestry.
func (Adapter) ProcessNames() []string { return []string{"codex"} }

// Home is Codex's home directory, resolved the way Codex does
// (codex-rs/utils/home-dir): $CODEX_HOME canonicalized, else ~/.codex.
// Trust keys embed this path, so it must match Codex exactly.
// $ASKGATE_USER_HOME wins over $CODEX_HOME so tests never reach a real
// Codex home through an inherited variable.
func Home() string {
	if os.Getenv("ASKGATE_USER_HOME") == "" {
		if h := os.Getenv("CODEX_HOME"); h != "" {
			if real, err := filepath.EvalSymlinks(h); err == nil {
				return real
			}
			return h
		}
	}
	return filepath.Join(paths.UserHome(), ".codex")
}

// HooksPath is the hooks file Askgate manages.
func HooksPath() string { return filepath.Join(Home(), "hooks.json") }

// ConfigPath is Codex's user config, where hook trust lives.
func ConfigPath() string { return filepath.Join(Home(), "config.toml") }

func (Adapter) Detect() adapters.Detection {
	d := adapters.Detection{SettingsPath: HooksPath()}
	d.Binary, d.Version = hookkit.DetectBinary("codex")
	_, err := os.Stat(Home())
	d.Installed = d.Binary != "" || err == nil
	if d.Binary == "" && d.Installed {
		d.Note = Home() + " exists but `codex` is not on PATH"
	}
	return d
}

// MaxHold: Codex kills a PermissionRequest hook at its timeout (600 s as
// installed); nothing else may block.
func (Adapter) MaxHold(kind adapters.HookKind) time.Duration {
	if kind == adapters.HookPermission {
		return PermissionTimeout * time.Second
	}
	return 0
}

// ProtectedPaths: whoever edits these can add or trust a hook that
// approves everything. "**/" entries are glob patterns for project
// layers (<repo>/.codex/).
func (Adapter) ProtectedPaths() []string {
	return []string{
		ConfigPath(),
		HooksPath(),
		"**/.codex/config.toml",
		"**/.codex/hooks.json",
	}
}

// ResumeArgs: `codex resume <SESSION_ID>` (codex-rs/cli/src/main.rs).
func (Adapter) ResumeArgs(agentSessionID string) []string {
	if agentSessionID == "" {
		return nil
	}
	return []string{"codex", "resume", agentSessionID}
}
