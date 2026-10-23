# Workstreams (v0.1)

| ID | Owner of | CLI file |
|---|---|---|
| A — Daemon core | internal/store, internal/state, internal/daemon | internal/cli/daemon.go (`daemon`, `start`, `stop`, `status`) |
| B — Adapters & hooks | internal/adapters/claude, internal/adapters/codex, internal/proc, docs/AGENTS.md | internal/cli/hooks.go (`hooks install|uninstall|status`, hidden `hook`) |
| C — Policy | internal/policy, docs/POLICY.md | internal/cli/policy.go (`policy …`) |
| D — Wrap & tasks | internal/wrap, internal/task | internal/cli/run.go (`run`, `attach`, `__holder`, `task …`) |
| E — Web, push, notify, remote | internal/web (incl. static PWA), internal/notify | internal/cli/remote.go (`pair`, `devices`, `remote`, `open`) |
| F — TUI, CLI, presence, service | internal/tui, internal/presence, internal/service | internal/cli/inbox.go (`setup`, `doctor`, `inbox`, `ls`, `approve`, `deny`, `log`, `explain`, `here/away/auto`, `service`, `notify`, `version`, default) |
| H — Marketing site | ../site | — |
| Lead | internal/api, internal/core, internal/client, internal/paths, internal/config, internal/adapters/adapter.go, internal/adapters/all, internal/cli/root.go, docs/BUILD-SPEC.md | root.go |

Stubs exist for every cross-package API (policy, web, notify, proc, presence, adapters). Keep their exported signatures; add to them freely.
