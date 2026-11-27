package ladowatch

// LADO fork: a worker that waits on its owner (2026-11-27). A worker that
// ends its turn with a question rests idle, so AO's own needs_input
// notification, which follows the waiting_input and blocked activity, never
// rings, and a needs-input report goes only to the orchestrator. The watch
// tells the owner instead, through AO's needs_input notification, so its
// toast, taskbar flash, sound, sidebar mark and phone pushes follow.
//
// A worker AO shows waiting_input or blocked is left to AO: it raised and
// resolves that notification itself.

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// AwaitSourceKeyPrefix starts the source key of the notifications the watch
// raises; restart reconciliation leaves such a row of an idle session open
// (queries/notifications.sql).
const AwaitSourceKeyPrefix = "lado-await:"

// Notifier raises and resolves AO notifications (*notify.Manager).
type Notifier interface {
	Notify(ctx context.Context, intent ports.NotificationIntent) error
	Resolve(ctx context.Context, res ports.NotificationResolution) error
}

// reportedThisTurn reports whether the worker's last report was made in its
// newest turn: a report of an earlier turn was answered already.
func (w worker) reportedThisTurn() bool {
	return w.report != "" && w.hasTurn && !w.reportAt.Before(w.turn.RequestedAt.UTC())
}

// awaitingTurn is the turn the worker waits on its owner after, and whether
// it does: it rests after a turn the owner did not stop, AO does not show it
// waiting already and it is not at its usage limit; and it either asked in
// this turn (a needs-input or stuck report) or stayed quiet, with no report
// this turn, for the project's await time. A report of another kind this
// turn says what the worker does: done, or a checkpoint while it waits on a
// long job of its own (2026-11-27: a weather drill worker waited eight hours
// on its runner after a checkpoint).
func awaitingTurn(w worker, now time.Time) (string, bool) {
	if w.session.Activity.State.NeedsInput() || w.watch.LimitState == domain.LadoLimitLimited ||
		!stallWatched(w.exec.Role) || !w.resting() || w.ownerStopped() {
		return "", false
	}
	if w.reportedThisTurn() {
		if w.asked() {
			return w.turn.ID, true
		}
		return "", false
	}
	wait := w.settings.Await()
	if wait <= 0 || now.Sub(quietSince(w)) < wait {
		return "", false
	}
	return w.turn.ID, true
}

// asked reports whether the worker's last report asks its owner.
func (w worker) asked() bool {
	return w.report == domain.ReportNeedsInput || w.report == domain.ReportStuck
}

// watchAwaiting tells the owner once per turn that the worker waits on them,
// and resolves what it told once the worker no longer does. The first look
// at a worker after the daemon starts resolves a wait an earlier run told of
// and that is over. Without a notifier it does nothing.
func (s *Service) watchAwaiting(ctx context.Context, w *worker, now time.Time) {
	if s.deps.Notifier == nil {
		return
	}
	turn, awaiting := awaitingTurn(*w, now)
	id := w.session.ID
	s.mu.Lock()
	told, had := s.awaiting[id]
	first := !s.visited[id]
	s.visited[id] = true
	switch {
	case awaiting && had && told == turn:
		s.mu.Unlock()
		return
	case awaiting:
		s.awaiting[id] = turn
	default:
		delete(s.awaiting, id)
	}
	s.mu.Unlock()

	if !awaiting {
		// AO owns the notification of a worker it shows waiting.
		if (had || first) && !w.session.Activity.State.NeedsInput() {
			s.resolveAwaiting(ctx, id, now)
		}
		return
	}
	err := s.deps.Notifier.Notify(context.WithoutCancel(ctx), ports.NotificationIntent{
		Type:               domain.NotificationNeedsInput,
		SessionID:          id,
		ProjectID:          w.session.ProjectID,
		CreatedAt:          now,
		SourceKey:          AwaitSourceKeyPrefix + string(id) + ":" + turn,
		SessionDisplayName: w.session.DisplayName,
	})
	if err != nil {
		// Told nothing: the next tick tries again.
		s.mu.Lock()
		delete(s.awaiting, id)
		s.mu.Unlock()
		s.deps.Log.Warn("LADO could not tell the owner a worker waits on them", "task", w.task.ID, "session", id, "err", err)
		return
	}
	reason := "quiet"
	if w.reportedThisTurn() && w.asked() {
		reason = "report"
	}
	s.audit(ctx, "lado.awaiting_owner", id, now, map[string]any{"task": w.task.ID, "turn": turn, "reason": reason})
}

// forgetAwaiting drops a session that ended, and resolves the wait the
// watch told of. AO's lifecycle resolves it too, but not when the session
// started working and ended between two ticks.
func (s *Service) forgetAwaiting(ctx context.Context, id domain.SessionID, now time.Time) {
	s.mu.Lock()
	_, had := s.awaiting[id]
	delete(s.awaiting, id)
	delete(s.visited, id)
	s.mu.Unlock()
	if had && s.deps.Notifier != nil {
		s.resolveAwaiting(ctx, id, now)
	}
}

func (s *Service) resolveAwaiting(ctx context.Context, id domain.SessionID, now time.Time) {
	if err := s.deps.Notifier.Resolve(context.WithoutCancel(ctx), ports.NotificationResolution{
		Type: domain.NotificationNeedsInput, SessionID: id, ResolvedAt: now,
	}); err != nil {
		s.deps.Log.Warn("LADO could not resolve a worker's wait on its owner", "session", id, "err", err)
	}
}
