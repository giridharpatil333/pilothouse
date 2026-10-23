package codex

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xasdta/pilothouse/internal/adapters"
	"github.com/xasdta/pilothouse/internal/adapters/hookkit"
)

var owned = hookkit.OwnedBy("codex")

// wiredEvents are the Codex events Pilothouse listens to. Codex has no
// Notification event; PermissionRequest is the "needs a person" signal.
// SessionEnd and Interrupt get 3 s (Codex's maximum for them; default 1 s)
// so the shim's own 1–2 s budget decides, not Codex's kill.
var wiredEvents = []struct {
	event, matcher string
	timeout        int
}{
	{"SessionStart", "", 0},
	{"SessionEnd", "", shortTimeout},
	{"UserPromptSubmit", "", 0},
	{"PreToolUse", "*", PreToolTimeout},
	{"PermissionRequest", "*", PermissionTimeout},
	{"PostToolUse", "*", 0},
	{"Stop", "", 0},
	{"SubagentStop", "", 0},
	{"Interrupt", "", shortTimeout},
}

func specs(shimPath string) []hookkit.Spec {
	cmd := hookkit.ShimCommand(shimPath, "codex")
	out := make([]hookkit.Spec, len(wiredEvents))
	for i, w := range wiredEvents {
		out[i] = hookkit.Spec{Event: w.event, Matcher: w.matcher, Command: cmd, Timeout: w.timeout}
	}
	return out
}

// plan is the two-file edit: hooks.json, then trust in config.toml.
type plan struct {
	hooks     hookkit.Plan
	config    hookkit.File
	newConfig string
	trustErr  error // config.toml could not be edited; hooks stay untrusted
}

func makePlan(sp []hookkit.Spec) (plan, error) {
	hp, err := hookkit.PlanJSON(HooksPath(), sp, owned)
	if err != nil {
		return plan{}, err
	}
	p := plan{hooks: hp}
	if p.config, err = hookkit.ReadFile(ConfigPath()); err != nil {
		return plan{}, err
	}
	edit := trustEdit{stale: map[string]bool{}, rename: map[string]string{}, want: map[string]string{}}
	path := HooksPath()
	for _, f := range hp.Result.Found {
		edit.stale[trustKey(path, f.Ref)] = true
	}
	for from, to := range hp.Result.Moved {
		edit.rename[trustKey(path, from)] = trustKey(path, to)
	}
	for _, s := range sp {
		edit.want[trustKey(path, hp.Result.Placed[s.Event])] = trustHash(s)
	}
	p.newConfig, p.trustErr = applyTrust(string(p.config.Data), edit)
	if p.trustErr != nil {
		p.newConfig = string(p.config.Data)
	}
	return p, nil
}

func (p plan) configChanged() bool { return p.newConfig != string(p.config.Data) }

func (p plan) change(summary string) adapters.Change {
	ch := p.hooks.Change(summary)
	ch.Changed = ch.Changed || p.configChanged()
	if p.configChanged() {
		ch.More = []adapters.Change{{
			Path:    p.config.Path,
			Changed: true,
			Before:  string(p.config.Data),
			After:   p.newConfig,
			Summary: "Record Codex hook trust for Pilothouse's hooks",
		}}
	}
	return ch
}

// commit writes hooks.json first: if config.toml then fails, Codex sees
// untrusted hooks and asks the person, which is safe; the reverse order
// could leave trust for hooks that do not exist.
func (p plan) commit(ch *adapters.Change) error {
	now := time.Now()
	if err := p.hooks.Commit(ch, now); err != nil {
		return err
	}
	if !p.configChanged() {
		return nil
	}
	backup, err := hookkit.Commit(p.config, []byte(p.newConfig), now)
	ch.More[0].Backup = backup
	if err != nil {
		return fmt.Errorf("hooks are in %s but trusting them in %s failed: %w", HooksPath(), ConfigPath(), err)
	}
	return nil
}

// InstallHooks writes our handlers into ~/.codex/hooks.json (merged,
// backed up, idempotent) and trusts exactly those handlers in
// ~/.codex/config.toml, preserving every comment and unrelated line.
// If config.toml uses a layout we do not rewrite, the hooks are installed
// untrusted and Codex asks the person to review them at startup.
func (Adapter) InstallHooks(opts adapters.InstallOptions) (adapters.Change, error) {
	if opts.ShimPath == "" {
		return adapters.Change{}, errors.New("install: no pilothouse binary path")
	}
	p, err := makePlan(specs(opts.ShimPath))
	if err != nil {
		return adapters.Change{}, err
	}
	summary := "Codex hooks are up to date in " + HooksPath()
	if p.hooks.Result.Changed || p.configChanged() {
		summary = fmt.Sprintf("Wire %d Codex hook events to %s", len(wiredEvents), hookkit.ShimCommand(opts.ShimPath, "codex"))
	}
	if p.trustErr != nil {
		summary += fmt.Sprintf(" (not trusted automatically: %v)", p.trustErr)
	}
	ch := p.change(summary)
	if opts.DryRun {
		return ch, nil
	}
	return ch, p.commit(&ch)
}

// UninstallHooks removes our handlers and their trust entries, and keeps
// the trust of the person's own hooks pointing at the right positions.
func (Adapter) UninstallHooks(opts adapters.InstallOptions) (adapters.Change, error) {
	p, err := makePlan(nil)
	if err != nil {
		return adapters.Change{}, err
	}
	summary := "No Pilothouse hooks in " + HooksPath()
	if p.hooks.Result.Changed || p.configChanged() {
		summary = fmt.Sprintf("Remove %d Pilothouse hook handlers from %s", len(p.hooks.Result.Found), HooksPath())
	}
	if p.trustErr != nil {
		summary += fmt.Sprintf(" (trust entries in config.toml left as they are: %v)", p.trustErr)
	}
	ch := p.change(summary)
	if opts.DryRun {
		return ch, nil
	}
	return ch, p.commit(&ch)
}

// Status reports wired events, whether they match what InstallHooks would
// write, and anything that stops Codex from running them.
func (Adapter) Status(opts adapters.InstallOptions) (adapters.HookStatus, error) {
	st := adapters.HookStatus{Path: HooksPath()}
	p, err := makePlan(specs(opts.ShimPath))
	if err != nil {
		st.Problems = append(st.Problems, err.Error())
		return st, nil
	}
	have := map[string]bool{}
	for _, f := range p.hooks.Result.Found {
		if !have[f.Event] {
			st.Events = append(st.Events, f.Event)
		}
		have[f.Event] = true
	}
	st.Installed = len(st.Events) > 0
	st.Problems = hookkit.Problems(p.hooks.Result.Found)
	var missing []string
	for _, w := range wiredEvents {
		if !have[w.event] {
			missing = append(missing, w.event)
		}
	}
	if st.Installed && len(missing) > 0 {
		st.Problems = append(st.Problems, "not wired: "+strings.Join(missing, ", ")+"; run `pilothouse hooks install --agent codex`")
	}
	doc, perr := parseTOML(string(p.config.Data))
	switch {
	case perr != nil:
		st.Problems = append(st.Problems, fmt.Sprintf("%s: %v", ConfigPath(), perr))
	case hooksFeatureOff(doc):
		st.Problems = append(st.Problems, "[features] hooks = false in config.toml turns Codex hooks off")
	case st.Installed && opts.ShimPath != "" && !p.hooks.Result.Changed:
		if n := untrusted(doc, p.hooks.Result.Placed, specs(opts.ShimPath)); n > 0 {
			st.Problems = append(st.Problems, fmt.Sprintf("Codex will skip %d Pilothouse hooks until they are trusted: run `pilothouse hooks install --agent codex`, or approve them in Codex (/hooks)", n))
		}
	}
	current := !p.hooks.Result.Changed && !p.configChanged()
	if opts.ShimPath == "" {
		current = len(missing) == 0
	}
	st.Current = st.Installed && current && len(st.Problems) == 0
	return st, nil
}

func untrusted(doc *tomlDoc, placed map[string]hookkit.Ref, sp []hookkit.Spec) int {
	n := 0
	for _, s := range sp {
		ref, ok := placed[s.Event]
		if !ok {
			continue
		}
		if h, _ := trustState(doc, trustKey(HooksPath(), ref)); h != trustHash(s) {
			n++
		}
	}
	return n
}
