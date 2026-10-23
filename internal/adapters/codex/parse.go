package codex

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xasdta/pilothouse/internal/adapters"
	"github.com/xasdta/pilothouse/internal/adapters/hookkit"
	"github.com/xasdta/pilothouse/internal/api"
)

// kinds maps Codex hook events (codex-rs/hooks/src/lib.rs HOOK_EVENT_NAMES)
// to Pilothouse's. Interrupt (the person pressed Esc) ends the turn the same
// way Stop does: the session now waits for its next prompt.
var kinds = map[string]adapters.HookKind{
	"SessionStart":      adapters.HookSessionStart,
	"SessionEnd":        adapters.HookSessionEnd,
	"UserPromptSubmit":  adapters.HookPromptSubmit,
	"PreToolUse":        adapters.HookPreTool,
	"PermissionRequest": adapters.HookPermission,
	"PostToolUse":       adapters.HookPostTool,
	"Stop":              adapters.HookStop,
	"Interrupt":         adapters.HookStop,
	"SubagentStop":      adapters.HookSubagentStop,
	"PreCompact":        adapters.HookPreCompact,
}

// ParseHook normalizes one Codex hook payload (schemas:
// codex-rs/hooks/schema/generated/*.command.input.schema.json).
func (Adapter) ParseHook(env adapters.HookEnvelope) (adapters.HookEvent, error) {
	p, err := hookkit.ParsePayload(env)
	if err != nil {
		return adapters.HookEvent{}, err
	}
	kind, ok := kinds[p.Event]
	if !ok {
		kind = adapters.HookUnknown
	}
	ev := p.HookEvent(kind)
	if p.Event == "Interrupt" {
		ev.Summary = "Interrupted"
	}
	if p.ToolName != "" {
		describeTool(&ev)
	}
	return ev, nil
}

// describeTool classifies Codex's hook tool names
// (codex-rs/core/src/tools/hook_names.rs and the handlers' payloads):
// "Bash" {command} for exec_command and network-access approvals,
// "apply_patch" {command: <patch>}, "mcp__<server>__<tool>" with the raw
// arguments, "write_stdin" {chars, …} for input to a running command,
// plus spawn_agent, request_permissions and extension tools.
func describeTool(ev *adapters.HookEvent) {
	field := func(name string) string { return hookkit.Field(ev.Input, name) }
	switch ev.Tool {
	case "Bash":
		ev.ToolKind = api.ToolShell
		ev.Command = field("command")
		ev.Summary = "Run: " + hookkit.OneLine(ev.Command, 160)
	case "write_stdin":
		// Keystrokes into a running command (often a shell): treat them as
		// a command so shell rules still apply.
		ev.ToolKind = api.ToolShell
		ev.Command = strings.TrimSpace(field("chars"))
		ev.Summary = "Type into running command: " + hookkit.OneLine(ev.Command, 120)
	case "apply_patch":
		ev.ToolKind = api.ToolEdit
		ev.Paths = patchPaths(field("command"), ev.Cwd)
		ev.Summary = editSummary(ev.Paths, ev.Cwd)
	case "request_permissions":
		ev.ToolKind = api.ToolOther
		ev.Summary = "Request permissions"
		if r := field("reason"); r != "" {
			ev.Summary += ": " + hookkit.OneLine(r, 120)
		}
	default:
		if server, name, ok := hookkit.MCPName(ev.Tool); ok {
			ev.ToolKind = api.ToolMCP
			ev.Summary = "MCP " + server + ": " + name
			return
		}
		ev.ToolKind = api.ToolOther
		ev.Summary = "Use " + ev.Tool
		for _, f := range []string{"description", "message", "prompt"} {
			if v := strings.TrimSpace(field(f)); v != "" {
				ev.Summary = ev.Tool + ": " + hookkit.OneLine(v, 120)
				break
			}
		}
	}
}

// patchPaths lists the files an apply_patch envelope touches, absolute
// (patch paths are relative to the working directory), in patch order.
// Grammar: codex-rs/apply-patch ("*** Add File: ", "*** Update File: ",
// "*** Delete File: ", "*** Move to: ").
func patchPaths(patch, cwd string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			p, ok := strings.CutPrefix(line, prefix)
			if !ok || strings.TrimSpace(p) == "" {
				continue
			}
			p = strings.TrimSpace(p)
			if !filepath.IsAbs(p) && cwd != "" {
				p = filepath.Join(cwd, p)
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func editSummary(files []string, cwd string) string {
	if len(files) == 0 {
		return "Edit files"
	}
	var names []string
	for _, f := range files[:min(len(files), 3)] {
		names = append(names, hookkit.RelPath(cwd, f))
	}
	s := "Edit: " + strings.Join(names, ", ")
	if len(files) > 3 {
		s += fmt.Sprintf(" (+%d more)", len(files)-3)
	}
	return s
}
