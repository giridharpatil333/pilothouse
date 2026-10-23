# Lead integration checklist (from workstream reports)

- [ ] Daemon: record `X-Askgate-Client` header as Decision.By (F says not read yet; A's spec said yes — verify).
- [ ] Daemon: apply session grants at the permission checkpoint too (Codex PreToolUse only honours deny; Claude benefits too).
- [ ] Codex pre_tool: policy `ask` can't be expressed → map to no-opinion (permission checkpoint still routes); `allow` → no-opinion. Verify adapter encodes this.
- [ ] Policy: ProtectedPaths include `**/` globs — C must glob-match them.
- [ ] Wrap OSC parser: Codex separates fields with `:` (`state=…:app=codex:kind=…`) and sends `state=clear`.
- [ ] Notify: Notification.URL empty — build deep link from BaseURL().
- [ ] Web: treat mid-stream close of Subscription.C as resync.
- [ ] `go mod tidy` (x/ansi, termenv direct).
- [ ] F ran real `launchctl print` in a smoke test (read-only) — make sure no test path calls it.
- [ ] Rename product to the user's chosen name (pending answer).
- [ ] Policy wiring (C): daemon uses `policy.Load(policy.DefaultOptions(protected...))`; reload → `e.Reload()`; Input gets Repo+Branch; policy.decision event data = `policy.DecisionEvent` incl. tool_use_id (A emits `api.PolicyDecision` — reconcile so replay pairs); grants after deny/ask.
- [ ] D: bubbletea/lipgloss terminal query at startup may delay every command up to 5s in non-replying terminals and leak into `attach` input — measure (`time askgate version` under `script`), lazy-load TUI styles or set lipgloss.SetHasDarkBackground explicitly.
- [ ] F: `askgate ls` mark attachable with wrap.Alive / wrap.ListHolders.
- [ ] A: accept PutTask before session registers (task new order).
