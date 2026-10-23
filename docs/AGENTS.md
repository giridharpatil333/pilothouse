# Agents: what Askgate can and cannot do with each one

Askgate talks to coding agents through their **hooks**: the agent runs
`askgate hook <agent>` at fixed points, pipes a JSON payload to it, and reads
the answer from its stdout. The shim forwards the payload to the daemon and
prints the daemon's answer; if the daemon is down or slow it prints nothing and
exits 0, so the agent behaves as if Askgate were not installed (fail open).

This page records what each agent really supports, with sources, as of
2026-10-22. It is the reference for `internal/adapters/claude` and
`internal/adapters/codex`.

## At a glance

| | Claude Code | Codex CLI |
|---|---|---|
| Hook config Askgate edits | `~/.claude/settings.json` | `~/.codex/hooks.json` + trust entries in `~/.codex/config.toml` |
| State detection | Full: session start/end, prompt, every tool call, permission prompts, notifications (idle, permission waiting), stop | Full except notifications: session start/end, prompt, tool calls, permission prompts, stop, **interrupt** |
| Policy `deny` (before every tool call) | Yes (`PreToolUse` → `permissionDecision: "deny"`) | Yes (`PreToolUse` → `permissionDecision: "deny"` + reason) |
| Policy `ask` (force the agent's prompt) | Yes (`permissionDecision: "ask"`) | **No.** Codex rejects `ask` as unsupported; its own `approval_policy` decides |
| Policy / grant `allow` (skip the prompt) | Yes (`permissionDecision: "allow"`; the agent's own deny/ask rules still win) | **No** at `PreToolUse` (Codex rejects `allow` without `updatedInput`); see Caveats |
| Remote approvals (phone answers the prompt) | Yes (`PermissionRequest` → `decision.behavior` allow/deny), held up to 600 s | Yes (`PermissionRequest` → `decision.behavior` allow/deny), held up to 600 s |
| Resume a session | `claude --resume <session_id>` | `codex resume <session_id>` |
| OSC 7501 status (wrap mode, L1) | Emits it (v2.1.295+) when it detects terminal support | Emits it whenever stdout is a terminal |

"Remote approvals" means: when the person is away, the hook holds the agent's
permission request while Askgate pushes it to a paired device, and the answer
goes back through the hook. When the person is at the desk the hook returns
no opinion immediately and the agent's own dialog appears (Askgate notifies
if it waits longer than `presence.notify_after`).

## Claude Code

Source: <https://code.claude.com/docs/en/hooks> (hooks reference) and
<https://code.claude.com/docs/en/hooks-guide>. Payloads in
`internal/adapters/claude/testdata/hooks/` were captured from Claude Code
**2.1.309** headless runs (files named `doc_*` are copied from the reference
for events a headless run cannot trigger). An end-to-end run of 2.1.309
through the real shim confirmed that a `PreToolUse` deny blocks the call with
Askgate's reason and that a `PermissionRequest` allow lets a `Write` through
that `claude -p` would otherwise deny.

### What `askgate hooks install` writes

One matcher group per event, appended after the person's own groups, each
with a single handler `{"type": "command", "command": "<askgate> hook claude"}`:

| Event | Matcher | Timeout | Askgate kind |
|---|---|---|---|
| `SessionStart` | (all) | default | `session_start` |
| `SessionEnd` | (all) | default (Claude's whole SessionEnd budget is 1.5 s; the shim gives up after 1 s) | `session_end` |
| `UserPromptSubmit` | n/a | default (30 s) | `prompt_submit` |
| `PreToolUse` | `*` | **30 s** | `pre_tool` |
| `PermissionRequest` | `*` | **600 s** | `permission` |
| `PostToolUse` | `*` | default | `post_tool` |
| `PostToolUseFailure` | `*` | default | `post_tool` (the tool ran, so the prompt was answered) |
| `Notification` | (all) | default | `notification` |
| `Stop` | n/a | default | `stop` |
| `SubagentStop` | (all) | default | `subagent_stop` |

Our handlers are recognized by a command of the form `…/askgate* hook claude`.
Install is idempotent, keeps every other key and hook (key order and
indentation too), writes atomically, follows a symlinked `settings.json`, backs
the file up to `settings.json.askgate-bak-<unix>` before changing it, and
refuses to touch a file that is not valid JSON. Uninstall removes exactly our
handlers (and any group, event list or `hooks` key left empty by that).

### Payload fields used

Common: `session_id`, `transcript_path`, `cwd`, `hook_event_name`,
`permission_mode`. Tool events: `tool_name`, `tool_input`, `tool_use_id`
(absent on `PermissionRequest`). Also `prompt` (UserPromptSubmit), `message` +
`notification_type` (Notification), `last_assistant_message` (Stop,
SubagentStop), `source` (SessionStart), `reason` (SessionEnd).

Tool classification: `Bash`, `PowerShell` → shell (`tool_input.command`);
`Edit`, `MultiEdit`, `Write` (`file_path`), `NotebookEdit` (`notebook_path`)
→ edit; `Read` (`file_path`), `Glob`, `Grep`, `LS` (`path`) → read;
`WebFetch`, `WebSearch` → web; `mcp__<server>__<tool>` → mcp; anything else →
other. File paths arrive absolute.

### Responses

Everything exits 0; decisions are JSON on stdout.

- `PreToolUse`: `{"hookSpecificOutput": {"hookEventName": "PreToolUse",
  "permissionDecision": "allow"|"deny"|"ask", "permissionDecisionReason": "…"}}`.
  For deny the reason is shown to Claude; for ask, in the prompt. An `allow`
  does not override the person's own deny/ask permission rules.
- `PermissionRequest`: `{"hookSpecificOutput": {"hookEventName":
  "PermissionRequest", "decision": {"behavior": "allow"}}}` or
  `{"behavior": "deny", "message": "…"}`. No output = the native dialog shows.
- `SessionStart` / `UserPromptSubmit`: optional `additionalContext`.
- Others: no output. (`Stop` with `decision: "block"` would keep Claude
  working, which Askgate never wants.)

### Caveats

- Hooks run without a controlling terminal, so the shim cannot report a TTY.
  Inside **tmux** this does not matter for hooks: approvals travel through the
  hook, and the shim forwards `$TMUX`/`$TERM_PROGRAM` so the daemon can say
  where the session lives. (Inside tmux, Claude Code asks tmux rather than the
  outer terminal whether OSC 7501 is supported; see the terminal docs.)
- `"disableAllHooks": true` turns Askgate off; `askgate hooks status` reports it.
  Managed settings with `allowManagedHooksOnly` block user hooks entirely.
- `PermissionRequest` does not fire for a sandboxed command's network request;
  only the `Notification` `permission_prompt` does (after ~6 s without typing),
  so those prompts show as blocked but cannot be answered remotely.
- The `Notification` `permission_prompt` / `idle_prompt` types fire only after
  ~6 s / ~60 s without keystrokes.
- Anyone who can edit a settings file can add a hook that approves everything;
  `ProtectedPaths()` lists the user, project and local settings files so the
  built-in `askgate-self-protection` rule forces a person to approve such edits.
  Hooks can also come from plugins, skills and subagent frontmatter, which
  Askgate does not police in v0.1.
- Settings edits are normally picked up by running sessions; restart a session
  if the hooks do not appear.

## Codex CLI

Source: `github.com/openai/codex` at commit `8531184` (2026-10-23), mostly
`codex-rs/hooks`. Codex is not installed on the machine this was built on, so
the fixtures in `internal/adapters/codex/testdata/hooks/` are built from
Codex's generated input schemas (`codex-rs/hooks/schema/generated/`) and its
payload builders; the trust hash is checked against vectors from Codex's own
test suite (`codex-rs/core/tests/suite/hooks.rs`).

### Hooks exist, are stable and on by default

Codex implements Claude-style lifecycle hooks: the feature `hooks` (legacy key
`codex_hooks`) is `Stage::Stable`, `default_enabled: true`
(`codex-rs/features/src/lib.rs`). Events (`codex-rs/hooks/src/lib.rs`):
`PreModelRequest`, `PostModelRequest`, `PreToolUse`, `PermissionRequest`,
`PostToolUse`, `PreCompact`, `PostCompact`, `SessionStart`, `SessionEnd`,
`UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `Stop`, `Interrupt`.
There is **no Notification event**.

Hooks are read from `hooks.json` next to each config layer (`~/.codex/hooks.json`
for the user; `<repo>/.codex/hooks.json` for projects) or from `[hooks]` tables
in `config.toml`, in the same JSON shape as Claude Code
(`codex-rs/hooks/src/engine/discovery.rs`). Askgate writes `hooks.json` (Codex
warns if a layer uses both forms). Command hooks run through
`$SHELL -lc "<command>"` in a new session, with stdin JSON and stdout JSON
(`codex-rs/hooks/src/engine/command_runner.rs`). Default timeout 600 s;
`SessionEnd` and `Interrupt` default to 1 s and are clamped to 3 s.

### Trust: why install also edits config.toml

Codex runs a non-managed hook only if it is **trusted**: `config.toml` must
contain `[hooks.state."<hooks.json path>:<event label>:<group>:<handler>"]`
with `trusted_hash = "sha256:…"`, a SHA-256 over the normalized declaration
(event label, matcher, `{type, command, timeout, async}`). Untrusted hooks are
listed as "new — review required" and skipped (`hook_trust_status`,
`hook_hash` in `discovery.rs`; `hook_key` in `hooks/src/lib.rs`;
`version_for_toml` in `codex-rs/config/src/fingerprint.rs`).

`askgate hooks install --agent codex` computes those hashes and adds one table
per Askgate handler, each preceded by an `# askgate-managed` comment, using a
small line-based TOML editor that keeps every comment and unrelated line.
Uninstall removes them and renames the trust entries of the person's own hooks
whose position in `hooks.json` shifted, so their trust survives. If
`config.toml` declares hook state with inline tables or dotted keys, Askgate
leaves it alone, installs the hooks untrusted and says so; Codex then asks the
person to review them at startup (or in `/hooks`). `askgate hooks status`
reports untrusted or modified Askgate hooks, and `[features] hooks = false`.

`~/.codex` is `$CODEX_HOME` (canonicalized) when set, as in
`codex-rs/utils/home-dir`; the trust key embeds that path.

### What `askgate hooks install --agent codex` writes

| Event | Matcher | Timeout | Askgate kind |
|---|---|---|---|
| `SessionStart` | (all) | default | `session_start` |
| `SessionEnd` | (all) | 3 s | `session_end` |
| `UserPromptSubmit` | n/a | default | `prompt_submit` |
| `PreToolUse` | `*` | 30 s | `pre_tool` |
| `PermissionRequest` | `*` | 600 s | `permission` |
| `PostToolUse` | `*` | default | `post_tool` |
| `Stop` | n/a | default | `stop` |
| `SubagentStop` | (all) | default | `subagent_stop` |
| `Interrupt` | n/a | 3 s | `stop` (the person pressed Esc; the session now waits) |

### Payloads

Common: `session_id`, `turn_id`, `transcript_path` (the rollout file
`~/.codex/sessions/YYYY/MM/DD/rollout-…-<uuid>.jsonl`; may be `null`), `cwd`,
`hook_event_name`, `model`, `permission_mode` (`default` or
`bypassPermissions`, derived from `approval_policy`).

Tool names (`codex-rs/core/src/tools/hook_names.rs` and handlers):
`Bash` with `{"command": "<string>"}` for `exec_command` (and for network-access
approvals, with `description: "network-access <host>"`); `apply_patch` with
`{"command": "<patch>"}` (Askgate extracts the file list from the `*** Add/
Update/Delete File:` and `*** Move to:` lines); `mcp__<server>__<tool>` with
the raw arguments; `write_stdin` (`chars` typed into a running command,
classified as shell); `spawn_agent`, `request_permissions` and extension tools.
`PermissionRequest` has no `tool_use_id`.

### Responses (`codex-rs/hooks/src/engine/output_parser.rs`)

- `PreToolUse`: only `permissionDecision: "deny"` with a non-empty
  `permissionDecisionReason` is honored. `allow` without `updatedInput` and
  `ask` are reported as unsupported and the hook counts as failed, so Askgate
  prints nothing for them. `continue: false`, `stopReason` and
  `suppressOutput` are rejected too.
- `PermissionRequest`: `decision.behavior` `allow` | `deny` (+ `message`).
  Codex fails closed on `updatedInput`, `updatedPermissions` and
  `interrupt: true`, so Askgate never sends them. Any deny wins over allows
  from other hooks.
- `SessionStart` / `UserPromptSubmit`: optional `additionalContext`.

### Caveats

- Policy `ask`/`allow` cannot skip or force Codex's prompt at `PreToolUse`.
  "Allow for session" grants are applied at `pre_tool` (BUILD-SPEC §5), where
  Codex ignores `allow`; for them to take effect on Codex the daemon must also
  apply a matching grant at the `permission` checkpoint (answer `allow`
  without routing to a person). The adapter encodes that answer correctly.
- No `Notification` event: "blocked" comes from `PermissionRequest` (it fires
  before Codex shows its prompt), "waiting" from `Stop`/`Interrupt`.
- Hooks run in a **login shell** (`$SHELL -lc`): anything a profile prints to
  stdout lands in front of Askgate's JSON and Codex rejects the output (fail
  open). Keep shell startup files quiet for non-interactive shells.
- Hooks need `[features] hooks` on (the default) and are ignored when the
  admin sets `allow_managed_hooks_only = true` in `requirements.toml`.
- tmux: hooks do not depend on the terminal; Codex's OSC 7501 output is emitted
  whenever stdout is a terminal (including inside tmux).
- `notify` (`notify = ["program", …]` in config.toml) is a legacy fire-and-forget
  program run after each turn with one JSON argument
  (`codex-rs/hooks/src/legacy_notify.rs`, marked for removal). It cannot block
  or answer anything and hooks cover it, so Askgate does not install it.

## OSC 7501 (for wrap mode)

- **Codex** (`codex-rs/tui/src/terminal_program_status.rs`) writes
  `ESC ] 7501 ; state=<idle|working|blocked|done|error>:app=codex[:kind=<permission|question|auth>] ESC \`
  whenever stdout is a terminal, and `ESC ] 7501 ; state=clear ESC \` on exit.
  Note the **colon** between fields, and the extra `clear` state.
- **Claude Code** (v2.1.295+) reports working / waiting / done through OSC 7501
  only after it detects that the terminal supports the protocol (inside tmux it
  asks tmux, not the outer terminal), and not when
  `CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1`
  (<https://code.claude.com/docs/en/terminal-config>). The detection handshake
  is not documented.
