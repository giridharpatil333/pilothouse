# Contributing

Thanks for helping. Bug reports with exact steps are as valuable as code.

## Develop

```sh
go build -o pilothouse ./cmd/pilothouse   # Go 1.27+, no cgo
go test -race ./...
gofmt -l . && go vet ./... && staticcheck ./...
```

Tests must never touch your real `~/.claude`, `~/.codex` or `~/.pilothouse`:
set `PILOTHOUSE_HOME`, `PILOTHOUSE_USER_HOME` and a short `PILOTHOUSE_RUN_DIR`
(`/tmp/...`, Unix socket paths are limited to ~104 bytes) with `t.Setenv`.

To try a change against a real agent without touching your setup, run a daemon
with those variables pointing at a temp dir, `pilothouse hooks install` into the
temp user home, and start Claude Code with `claude --settings <temp>/.claude/settings.json`.

## Layout

`docs/BUILD-SPEC.md` describes the architecture; `docs/AGENTS.md` what each
agent supports; `docs/POLICY.md` the rules engine.

## Pull requests

- One change per PR, with tests. Keep the hook path fast: policy evaluation
  must stay well under a millisecond.
- Explain *why* in comments, not *what*.
- Fail open: if Pilothouse breaks, agents must behave as if it weren't installed
  (unless the user set `approvals.fail_closed`).
