package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/xasdta/askgate/internal/adapters"
	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/core"
	"github.com/xasdta/askgate/internal/policy"
	"github.com/xasdta/askgate/internal/state"
	"github.com/xasdta/askgate/internal/store"
)

const (
	// holdMargin keeps our hold shorter than the agent's hook timeout, so
	// we answer before the agent kills the shim.
	holdMargin = 15 * time.Second
	// maxLoggedInput caps the tool input kept in a hook event. Policy replay
	// rarely needs more, and a 10 MB file write shouldn't bloat the log.
	maxLoggedInput = 256 << 10
	maxLastMessage = 500
)

// hookCall is one hook being routed, with what the daemon worked out about
// it before opening the write transaction.
type hookCall struct {
	ad       adapters.Adapter
	env      adapters.HookEnvelope
	ev       adapters.HookEvent
	cwd      string
	root     string
	branch   string
	agentPID int
	mode     api.Mode
}

// permissionPlan is a permission request about to be opened.
type permissionPlan struct {
	req       api.Request
	toolUseID string
	hold      time.Duration
	wait      <-chan api.Request // nil when routed to the terminal
}

// Hook routes one hook call (BUILD-SPEC §5) and returns what the shim must
// print. It never fails: on internal errors it answers like the shim would
// with the daemon down (fail open unless approvals.fail_closed).
func (d *Daemon) Hook(ctx context.Context, env adapters.HookEnvelope) adapters.HookResponse {
	ad := d.adapters[env.Agent]
	if ad == nil {
		return adapters.HookResponse{} // unknown agent: no opinion
	}
	ev, err := ad.ParseHook(env)
	if err != nil {
		d.log.Debug("unparseable hook", "agent", env.Agent, "event", env.Event, "err", err)
		return ad.FailResponse(adapters.HookUnknown, false)
	}
	resp, err := d.routeHook(ctx, ad, env, ev)
	if err != nil {
		d.log.Error("hook failed", "agent", env.Agent, "kind", ev.Kind, "err", err)
		return ad.FailResponse(ev.Kind, d.cfg.Approvals.FailClosed)
	}
	return resp
}

func (d *Daemon) routeHook(ctx context.Context, ad adapters.Adapter, env adapters.HookEnvelope, ev adapters.HookEvent) (adapters.HookResponse, error) {
	h := &hookCall{ad: ad, env: env, ev: ev, cwd: firstNonEmpty(ev.Cwd, env.Cwd), mode: api.ModeObserved}
	if env.AskgateSessionID != "" {
		h.mode = api.ModeWrapped
	}
	h.root, h.branch = d.git.lookup(ctx, h.cwd)
	h.agentPID = agentPID(ad, env.Ancestors)

	// Policy is evaluated before the write transaction so the (pure, CPU
	// bound) rule evaluation doesn't hold the database write lock.
	var pol policy.Result
	if ev.Kind == adapters.HookPreTool || ev.Kind == adapters.HookPermission {
		pol = d.engine().Evaluate(policy.Input{
			Agent: env.Agent, Tool: ev.Tool, ToolKind: ev.ToolKind, Command: ev.Command, Paths: ev.Paths,
			Input: ev.Input, Cwd: h.cwd, Repo: h.root, Branch: h.branch, Mode: h.mode,
			Checkpoint: checkpointOf(ev.Kind), Now: d.opts.Now(),
		})
	}
	var plan *permissionPlan
	if ev.Kind == adapters.HookPermission {
		// The waiter is registered before the request is committed, so a
		// decision that lands immediately after the commit can't be missed.
		plan = d.planPermission(h, pol)
	}

	var outcome adapters.HookOutcome
	var sess api.Session
	err := d.db.Write(ctx, func(tx *store.Tx) error {
		s, err := d.sessionForHook(ctx, tx, h)
		if err != nil {
			return err
		}
		before := s
		applyHook(&s, h, tx.Now())
		if _, err := tx.Emit(ctx, api.EventHook, s.ID, "", loggedEvent(ev)); err != nil {
			return err
		}
		if err := addHookEvidence(ctx, tx, s.ID, ev); err != nil {
			return err
		}
		if outcome, err = d.applyHookEffects(ctx, tx, s, h, pol, plan); err != nil {
			return err
		}
		sess, err = d.saveSession(ctx, tx, &before, s)
		return err
	})
	if err != nil {
		if plan != nil && plan.wait != nil {
			d.waiters.remove(plan.req.ID)
		}
		return adapters.HookResponse{}, err
	}

	switch ev.Kind {
	case adapters.HookPermission:
		outcome = d.holdPermission(ctx, plan, sess)
	case adapters.HookStop:
		if d.Presence().State == api.PresenceAway {
			d.notify(core.Notification{Kind: core.NotifyWaiting, Title: sess.Title + " is waiting for you",
				Body: firstNonEmpty(sess.LastMessage, "Finished its turn"), Session: &sess, Remote: true})
		}
	}
	return ad.EncodeResponse(ev, outcome), nil
}

// applyHookEffects does the per-kind work of §5 inside the hook transaction.
func (d *Daemon) applyHookEffects(ctx context.Context, tx *store.Tx, s api.Session, h *hookCall, pol policy.Result, plan *permissionPlan) (adapters.HookOutcome, error) {
	ev := h.ev
	switch ev.Kind {
	case adapters.HookPreTool:
		return d.decidePreTool(ctx, tx, s, ev, pol)
	case adapters.HookPermission:
		plan.req.SessionID = s.ID
		if err := tx.InsertRequest(ctx, plan.req, plan.toolUseID); err != nil {
			return adapters.HookOutcome{}, err
		}
		_, err := tx.Emit(ctx, api.EventRequestOpened, s.ID, plan.req.ID, plan.req)
		return adapters.HookOutcome{}, err
	case adapters.HookPostTool:
		// The person answered the agent's own dialog: the tool ran.
		req, err := tx.MatchSteppedAside(ctx, s.ID, ev.ToolUseID, ev.Tool, ev.Summary)
		if errors.Is(err, store.ErrNotFound) {
			return adapters.HookOutcome{}, nil
		}
		if err != nil {
			return adapters.HookOutcome{}, err
		}
		return adapters.HookOutcome{}, closeInTx(ctx, tx, req.ID, api.RequestAnsweredLocally)
	case adapters.HookPromptSubmit, adapters.HookStop, adapters.HookSessionEnd:
		// The turn moved on, so whatever the dialog showed was answered.
		reqs, err := tx.Requests(ctx, store.RequestFilter{SessionID: s.ID,
			Statuses: []api.RequestStatus{api.RequestSteppedAside}})
		if err != nil {
			return adapters.HookOutcome{}, err
		}
		for _, r := range reqs {
			if err := closeInTx(ctx, tx, r.ID, api.RequestAnsweredLocally); err != nil {
				return adapters.HookOutcome{}, err
			}
		}
	}
	return adapters.HookOutcome{}, nil
}

// decidePreTool combines policy and grants: deny > ask > grant allow >
// policy allow > no opinion. Grants are applied after deny/ask rules so a
// deny rule always wins over "allow for this session".
func (d *Daemon) decidePreTool(ctx context.Context, tx *store.Tx, s api.Session, ev adapters.HookEvent, pol policy.Result) (adapters.HookOutcome, error) {
	pd := api.PolicyDecision{Tool: ev.Tool, Summary: ev.Summary, Rule: pol.Rule, File: pol.File, Message: pol.Message}
	for _, m := range pol.Matched {
		pd.Matched = append(pd.Matched, m.Rule+" ("+string(m.Action)+")")
	}
	var out adapters.HookOutcome
	switch pol.Action {
	case api.BehaviorDeny:
		out = adapters.HookOutcome{Behavior: api.BehaviorDeny,
			Message: firstNonEmpty(pol.Message, "Blocked by Askgate policy rule "+pol.Rule+".")}
	case api.BehaviorAsk:
		out = adapters.HookOutcome{Behavior: api.BehaviorAsk, Message: pol.Message}
	default:
		grants, err := tx.Grants(ctx, s.ID)
		if err != nil {
			return out, err
		}
		if g, ok := matchGrant(grants, ev.Tool, ev.ToolKind, ev.Command); ok {
			out = adapters.HookOutcome{Behavior: api.BehaviorAllow, Message: "Allowed for this session in Askgate."}
			pd.Rule, pd.File, pd.Message, pd.GrantID = "", "", out.Message, g.ID
		} else if pol.Action == api.BehaviorAllow {
			out = adapters.HookOutcome{Behavior: api.BehaviorAllow, Message: pol.Message}
		}
	}
	if out.Behavior == "" {
		return out, nil
	}
	pd.Behavior = out.Behavior
	_, err := tx.Emit(ctx, api.EventPolicyDecision, s.ID, "", pd)
	return out, err
}

// planPermission builds the request for a permission hook and picks its
// route: devices when the person is away (or always_hold), else the
// terminal's own dialog.
func (d *Daemon) planPermission(h *hookCall, pol policy.Result) *permissionPlan {
	now := d.now()
	hold := d.cfg.Approvals.HoldLimit.Duration
	if max := h.ad.MaxHold(adapters.HookPermission) - holdMargin; max < hold {
		hold = max
	}
	ev := h.ev
	p := &permissionPlan{toolUseID: ev.ToolUseID, hold: hold}
	p.req = api.Request{
		ID: newID("r_"), Agent: h.env.Agent, Checkpoint: api.CheckpointPermission,
		Tool: ev.Tool, ToolKind: ev.ToolKind, Command: ev.Command, Paths: ev.Paths, Input: ev.Input,
		Summary: firstNonEmpty(ev.Summary, ev.Tool), Cwd: h.cwd,
		Route: api.RouteTerminal, Status: api.RequestSteppedAside,
		CreatedAt: now, ExpiresAt: now.Add(max(hold, 0)),
	}
	if pol.Action != "" {
		p.req.PolicyRule = pol.Rule
	}
	away := d.Presence().State == api.PresenceAway
	if hold > 0 && (d.cfg.Approvals.AlwaysHold || away) {
		if ch, ok := d.waiters.add(p.req.ID); ok {
			p.wait = ch
			p.req.Route, p.req.Status = api.RouteDevices, api.RequestOpen
		}
	}
	return p
}

// holdPermission waits for the outcome of a permission request routed to
// devices; terminal-routed requests return no opinion at once so the
// agent shows its own dialog.
func (d *Daemon) holdPermission(ctx context.Context, p *permissionPlan, sess api.Session) adapters.HookOutcome {
	req := p.req
	if p.wait == nil {
		d.scheduleBlockedNotice(req, sess)
		return adapters.HookOutcome{}
	}
	defer d.waiters.remove(req.ID)
	d.notify(core.Notification{Kind: core.NotifyRequest, Title: sess.Title + " needs permission",
		Body: req.Summary, Request: &req, Session: &sess, Remote: true})

	timer := time.NewTimer(p.hold)
	defer timer.Stop()
	var final api.Request
	select {
	case final = <-p.wait:
	case <-timer.C:
		final = d.endHold(p, api.RequestExpired, "hold limit reached")
	case <-ctx.Done():
		final = d.endHold(p, api.RequestExpired, "hook shim disconnected")
	case <-d.ctx.Done():
		// Shutdown releases every held request, but one that registered
		// during the release pass releases itself here.
		final = d.endHold(p, api.RequestSteppedAside, "the askgate daemon is stopping")
	}

	switch final.Status {
	case api.RequestDecided:
		if dec := final.Decision; dec != nil {
			return adapters.HookOutcome{Behavior: dec.Behavior, Message: decisionMessage(dec)}
		}
	case api.RequestExpired:
		d.notify(core.Notification{Kind: core.NotifyBlocked, Title: sess.Title + ": answer in the terminal",
			Body: "No answer in time for: " + final.Summary, Request: &final, Session: &sess, Remote: true})
	}
	return adapters.HookOutcome{}
}

// endHold closes a held request (expired, or stepped aside on shutdown).
// If someone else closed it first, their result is returned instead.
func (d *Daemon) endHold(p *permissionPlan, to api.RequestStatus, reason string) api.Request {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := d.closeRequest(ctx, p.req.ID, []api.RequestStatus{api.RequestOpen}, to)
	if err == nil {
		d.log.Info("hold ended", "request", p.req.ID, "status", to, "reason", reason)
		return r
	}
	if errors.Is(err, store.ErrAlreadyClosed) {
		// The winner resolves our waiter right after its commit.
		select {
		case r := <-p.wait:
			return r
		case <-time.After(2 * time.Second):
		}
		if r, err := d.db.Request(ctx, p.req.ID); err == nil {
			return r
		}
	}
	d.log.Error("end hold", "request", p.req.ID, "err", err)
	return p.req
}

// scheduleBlockedNotice sends a desktop notification if a native dialog is
// still unanswered after presence.notify_after.
func (d *Daemon) scheduleBlockedNotice(req api.Request, sess api.Session) {
	after := d.cfg.Presence.NotifyAfter.Duration
	if after <= 0 {
		return
	}
	d.goBackground(func() {
		t := time.NewTimer(after)
		defer t.Stop()
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
		}
		r, err := d.db.Request(d.ctx, req.ID)
		if err != nil || r.Status != api.RequestSteppedAside {
			return
		}
		d.notify(core.Notification{Kind: core.NotifyBlocked, Title: sess.Title + " is waiting for permission",
			Body: r.Summary, Request: &r, Session: &sess, Remote: false})
	})
}

// sessionForHook finds the hook's session or creates it: by Askgate id for
// wrapped sessions, else by the agent's session id, else by agent PID.
func (d *Daemon) sessionForHook(ctx context.Context, tx *store.Tx, h *hookCall) (api.Session, error) {
	err := store.ErrNotFound
	var s api.Session
	switch id := h.env.AskgateSessionID; {
	case id != "" && validSessionID(id):
		s, err = tx.Session(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			// The hook beat the holder's registration; the session is
			// completed when the holder registers.
			return d.createSession(ctx, tx, h, id)
		}
	case h.ev.AgentSessionID != "":
		s, err = tx.SessionByAgentID(ctx, h.env.Agent, h.ev.AgentSessionID)
	case h.agentPID > 0:
		s, err = tx.LiveSessionByPID(ctx, h.env.Agent, h.agentPID)
	}
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return s, err
	}
	return d.createSession(ctx, tx, h, newID("s_"))
}

func (d *Daemon) createSession(ctx context.Context, tx *store.Tx, h *hookCall, id string) (api.Session, error) {
	now := tx.Now()
	s := api.Session{
		ID: id, Agent: h.env.Agent, Mode: h.mode, Machine: d.machine,
		Title: titleFor(h.root, h.cwd, h.env.Agent), Cwd: h.cwd, RepoRoot: h.root, Branch: h.branch,
		PID: h.agentPID, AgentSessionID: h.ev.AgentSessionID, TranscriptPath: h.ev.TranscriptPath,
		TaskID: h.env.Env["ASKGATE_TASK_ID"], State: api.StateStarting, StartedAt: now, UpdatedAt: now,
	}
	return d.saveSession(ctx, tx, nil, s)
}

// applyHook folds what a hook tells us about the session into it.
func applyHook(s *api.Session, h *hookCall, now time.Time) {
	ev := h.ev
	if ev.AgentSessionID != "" {
		s.AgentSessionID = ev.AgentSessionID
	}
	if ev.TranscriptPath != "" {
		s.TranscriptPath = ev.TranscriptPath
	}
	if s.Cwd == "" {
		s.Cwd = h.cwd
	}
	if h.root != "" && (s.RepoRoot == "" || s.RepoRoot == h.root) {
		s.RepoRoot, s.Branch = h.root, h.branch
	}
	if s.TaskID == "" {
		s.TaskID = h.env.Env["ASKGATE_TASK_ID"]
	}
	if s.ExitedAt != nil && ev.Kind != adapters.HookSessionEnd {
		// A resumed conversation (same agent session id) is alive again.
		s.ExitedAt, s.ExitCode, s.PID = nil, nil, h.agentPID
	}
	if s.PID == 0 {
		s.PID = h.agentPID
	}
	switch ev.Kind {
	case adapters.HookStop:
		if msg := strings.TrimSpace(ev.Message); msg != "" {
			s.LastMessage = truncate(msg, maxLastMessage)
		}
	case adapters.HookSessionEnd:
		if s.ExitedAt == nil {
			t := now
			s.ExitedAt = &t
		}
	}
}

// addHookEvidence records the state a hook is evidence of. Permission hooks
// add none: the pending request itself keeps the session blocked (resolver
// rule 2), and once it closes the session should fall back to the hook
// before it, not stay "blocked".
func addHookEvidence(ctx context.Context, tx *store.Tx, sessionID string, ev adapters.HookEvent) error {
	if ev.Kind == adapters.HookPermission {
		return nil
	}
	st, kind, detail, ok := state.FromHook(ev)
	if !ok {
		return nil
	}
	return tx.AddEvidence(ctx, sessionID, api.StateEvidence{At: tx.Now(), Layer: api.LayerHook,
		State: st, Kind: kind, Detail: truncate(detail, 200)})
}

// loggedEvent is the hook as kept in the event log: normalized, with very
// large tool input dropped.
func loggedEvent(ev adapters.HookEvent) adapters.HookEvent {
	if len(ev.Input) > maxLoggedInput {
		ev.Input = nil
	}
	return ev
}

// agentPID is the nearest ancestor of the hook shim that is the agent.
func agentPID(ad adapters.Adapter, ancestors []adapters.Proc) int {
	names := ad.ProcessNames()
	for _, p := range ancestors {
		base := filepath.Base(p.Comm)
		for _, n := range names {
			if base == n {
				return p.PID
			}
		}
	}
	return 0
}

func checkpointOf(k adapters.HookKind) api.Checkpoint {
	if k == adapters.HookPermission {
		return api.CheckpointPermission
	}
	return api.CheckpointPreTool
}

func decisionMessage(dec *api.Decision) string {
	if dec.Message != "" {
		return dec.Message
	}
	if dec.Behavior == api.BehaviorDeny {
		return "Denied in Askgate (" + dec.By + ")."
	}
	return "Allowed in Askgate (" + dec.By + ")."
}

func titleFor(root, cwd string, agent api.AgentKind) string {
	switch {
	case root != "":
		return filepath.Base(root)
	case cwd != "":
		return filepath.Base(cwd)
	}
	return string(agent)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
