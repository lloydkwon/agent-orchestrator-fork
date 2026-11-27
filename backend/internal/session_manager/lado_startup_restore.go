package sessionmanager

// LADO fork: sessions whose agent a reboot stopped start again when the
// daemon starts. Upstream leaves a missing agent stopped at startup
// (startup_health.go) and waits for a person to press Restore. Meanwhile the
// reaper marks the session terminated, the app finds no live orchestrator and
// makes a new one, and the board looks empty. With the developer's setting on
// (Settings → Runs), the startup health pass starts the missing agent again in
// its worktree and its own conversation instead, as Resume would.
//
// Only an agent that was running when the daemon last saw it is started: one
// that had already exited, a terminated session and a hibernated Chat stay as
// they are. A probe that failed is not proof the agent is gone, so nothing
// starts then either. The worker gate is asked first, so an execution LADO
// stopped for good stays stopped.

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LadoStartupRestore is how the daemon tells the session manager whether to
// start missing agents again at startup, and hears which it started.
type LadoStartupRestore struct {
	// Enabled reports whether the developer's setting is on. It is read once
	// for each missing agent, so a change takes effect at the next start. Nil
	// keeps upstream behavior.
	Enabled func(ctx context.Context) bool
	// Restored is told each session started again. interrupted says the
	// session was in the middle of a turn when the daemon last saw it, so its
	// work was cut off. Nil hears nothing.
	Restored func(id domain.SessionID, interrupted bool)
}

// SetLadoStartupRestore installs the startup restore. The daemon calls it
// before the startup health pass; the zero value keeps upstream behavior.
func (m *Manager) SetLadoStartupRestore(r LadoStartupRestore) {
	m.ladoStartupRestore = r
}

// AwaitingStartupRestore reports whether the startup health pass may yet
// start agents again: the startup restore is installed and the pass has not
// finished. The reaper waits that long before it takes a dead runtime for a
// reason to terminate a session (lifecycle/lado_startup_hold.go); without the
// wait, a slow start would let it terminate the sessions the pass is about to
// start again.
func (m *Manager) AwaitingStartupRestore() bool {
	if m.ladoStartupRestore.Enabled == nil {
		return false
	}
	select {
	case <-m.startupBackgroundReconcileDone:
		return false
	default:
		return true
	}
}

// agentMissingAtStartup settles a session the startup health pass found
// without its agent: it starts the agent again when the setting allows, and
// records the agent exited otherwise, which is upstream's behavior. rec is the
// row as the daemon found it at startup. A start that fails leaves the session
// exited and resumable (reconcileLive), never terminated.
func (m *Manager) agentMissingAtStartup(ctx context.Context, rec domain.SessionRecord) error {
	if !m.restoresAtStartup(ctx, rec) {
		return m.recordAgentExited(ctx, rec)
	}
	if err := m.reconcileLive(ctx, rec); err != nil {
		return err
	}
	m.logger.Info("startup restore: started a session's agent again", "sessionID", rec.ID, "kind", rec.Kind)
	if notify := m.ladoStartupRestore.Restored; notify != nil {
		notify(rec.ID, turnWasRunning(rec.Activity.State))
	}
	return nil
}

// restoresAtStartup reports whether the missing agent of rec starts again.
func (m *Manager) restoresAtStartup(ctx context.Context, rec domain.SessionRecord) bool {
	enabled := m.ladoStartupRestore.Enabled
	if enabled == nil || rec.IsTerminated || rec.HibernatedAt != nil || rec.Activity.State == domain.ActivityExited {
		return false
	}
	if !enabled(ctx) {
		return false
	}
	if err := m.authorizeLadoRestore(ctx, rec.ID); err != nil {
		m.logger.Info("startup restore: the worker gate keeps a session stopped", "sessionID", rec.ID, "reason", err)
		return false
	}
	return true
}

// turnWasRunning reports whether a session in state was in the middle of a
// turn: working, or blocked on an approval or a question inside the turn. An
// idle session, or one waiting at its prompt, had finished its turn.
func turnWasRunning(state domain.ActivityState) bool {
	return state == domain.ActivityActive || state == domain.ActivityBlocked
}
