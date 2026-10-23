package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/xasdta/askgate/internal/api"
	"github.com/xasdta/askgate/internal/policy"
	"github.com/xasdta/askgate/internal/state"
	"github.com/xasdta/askgate/internal/store"
)

const (
	retentionInterval  = time.Hour
	staleSweepInterval = time.Minute
)

// startLoops starts the periodic background work.
func (d *Daemon) startLoops() {
	poll := d.opts.PollInterval
	d.every(poll, func(ctx context.Context) {
		if err := d.updatePresence(ctx, nil); err != nil && ctx.Err() == nil {
			d.log.Warn("presence update", "err", err)
		}
	})
	d.every(poll, d.checkLiveness)
	d.every(poll, func(context.Context) {
		if d.engine().Changed() {
			d.log.Info("policy files changed; reloading")
			d.reloadPolicy()
		}
	})
	d.every(staleSweepInterval, d.sweepStale)
	d.goBackground(func() {
		d.prune(d.ctx)
		t := time.NewTicker(retentionInterval)
		defer t.Stop()
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-t.C:
				d.prune(d.ctx)
			}
		}
	})
	d.startUpdateCheck()
}

// ---- policy ----

func (d *Daemon) engine() PolicyEngine { return d.policy.Load().PolicyEngine }

// reloadPolicy rebuilds the policy engine. policy.Load never fails: broken
// files keep their last good rules and report errors in the status.
func (d *Daemon) reloadPolicy() api.PolicyStatus {
	var e PolicyEngine
	if d.opts.LoadPolicy != nil {
		e = d.opts.LoadPolicy()
	} else {
		e = d.loadDefaultPolicy()
	}
	d.policy.Store(&policyHolder{e})
	st := e.Status()
	for _, msg := range st.Errors {
		d.log.Warn("policy problem", "err", msg)
	}
	return st
}

func (d *Daemon) loadDefaultPolicy() PolicyEngine {
	var protected []string
	for _, a := range d.adapters {
		protected = append(protected, a.ProtectedPaths()...)
	}
	// DefaultOptions always protects the Askgate home and socket directory.
	e, _ := policy.Load(policy.DefaultOptions(protected...))
	return e
}

// PolicyStatus reports the loaded policy files.
func (d *Daemon) PolicyStatus() api.PolicyStatus { return d.engine().Status() }

// ReloadPolicy re-reads every policy file now.
func (d *Daemon) ReloadPolicy() api.PolicyStatus { return d.reloadPolicy() }

// ---- liveness (layer L0) ----

// checkLiveness marks sessions whose agent process is gone as exited.
func (d *Daemon) checkLiveness(ctx context.Context) {
	sessions, err := d.db.LiveSessions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Warn("liveness check", "err", err)
		}
		return
	}
	for _, s := range sessions {
		if s.PID > 0 && !d.alive(s.PID) {
			if err := d.markExited(ctx, s.ID, "Process exited", nil); err != nil && ctx.Err() == nil {
				d.log.Warn("mark session exited", "session", s.ID, "err", err)
			}
		}
	}
}

// markExited records that a session's process ended. Requests it still had
// pending can no longer be answered, so they expire.
func (d *Daemon) markExited(ctx context.Context, id, detail string, code *int) error {
	var closed []api.Request
	err := d.db.Write(ctx, func(tx *store.Tx) error {
		s, err := tx.Session(ctx, id)
		if err != nil {
			return err
		}
		if s.ExitedAt != nil {
			return nil
		}
		before := s
		now := tx.Now()
		s.ExitedAt, s.ExitCode = &now, code
		if err := tx.AddEvidence(ctx, id, api.StateEvidence{At: now, Layer: api.LayerProcess,
			State: api.StateExited, Detail: detail}); err != nil {
			return err
		}
		pending, err := tx.Requests(ctx, store.RequestFilter{SessionID: id, Statuses: store.PendingStatuses})
		if err != nil {
			return err
		}
		for _, r := range pending {
			cr, err := tx.CloseRequest(ctx, r.ID, api.RequestExpired, nil, now)
			if errors.Is(err, store.ErrAlreadyClosed) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.Emit(ctx, api.EventRequestClosed, id, r.ID, cr); err != nil {
				return err
			}
			closed = append(closed, cr)
		}
		_, err = d.saveSession(ctx, tx, &before, s)
		return err
	})
	for _, r := range closed {
		d.waiters.resolve(r.ID, r)
	}
	return err
}

// sweepStale re-resolves sessions that have looked "working" for longer
// than state.StaleWorking, so silence turns into "unknown" without waiting
// for the next hook.
func (d *Daemon) sweepStale(ctx context.Context) {
	sessions, err := d.db.LiveSessions(ctx)
	if err != nil {
		return
	}
	cutoff := d.now().Add(-state.StaleWorking)
	for _, s := range sessions {
		if s.State != api.StateWorking || s.UpdatedAt.After(cutoff) {
			continue
		}
		if err := d.db.Write(ctx, func(tx *store.Tx) error { return d.touchSession(ctx, tx, s.ID) }); err != nil && ctx.Err() == nil {
			d.log.Warn("stale sweep", "session", s.ID, "err", err)
		}
	}
}

// ---- retention ----

func (d *Daemon) prune(ctx context.Context) {
	keep := d.cfg.Retention.Duration
	if keep <= 0 {
		return
	}
	var res store.PruneResult
	err := d.db.Write(ctx, func(tx *store.Tx) error {
		var err error
		res, err = tx.Prune(ctx, tx.Now().Add(-keep))
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			d.log.Warn("retention prune", "err", err)
		}
		return
	}
	if res != (store.PruneResult{}) {
		d.log.Info("pruned old records", "requests", res.Requests, "events", res.Events, "evidence", res.Evidence)
	}
}
