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
// starts then either; that leaves out sessions on the tmux runtime, whose
// probe answers "unavailable" once its server is gone. The worker gate and
// LADO are asked first, so an execution LADO stopped, or is about to stop,
// stays stopped, and an agent whose install runs is not started under it.

import (
	"context"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LadoStartupRestore is how the daemon tells the session manager whether to
// start missing agents again at startup, and hears which it started.
type LadoStartupRestore struct {
	// Enabled reports whether the developer's setting is on. It is read once
	// for each missing agent, so a change takes effect at the next start. Nil
	// keeps upstream behavior.
	Enabled func(ctx context.Context) bool
	// Refuse names why LADO keeps a session stopped, "" when it does not. It
	// covers what the worker gate cannot see yet, such as an execution past
	// its maximum run time that LADO has not stopped. Nil refuses nothing.
	Refuse func(ctx context.Context, id domain.SessionID) string
	// Restored is told each session started again. interrupted says the
	// session was in the middle of a turn when the daemon last saw it, so its
	// work was cut off. Nil hears nothing.
	Restored func(id domain.SessionID, interrupted bool)
}

// ladoStartupRestoreState is the startup restore with what the reaper's hold
// reads of it.
type ladoStartupRestoreState struct {
	LadoStartupRestore
	// holdOnce reads the setting once for the hold; holds is its answer.
	holdOnce sync.Once
	holds    bool
}

// SetLadoStartupRestore installs the startup restore. The daemon calls it
// before the startup health pass and before the reaper reads
// AwaitingStartupRestore; the zero value keeps upstream behavior.
func (m *Manager) SetLadoStartupRestore(r LadoStartupRestore) {
	m.ladoStartupRestore = &ladoStartupRestoreState{LadoStartupRestore: r}
}

// AwaitingStartupRestore reports whether the startup health pass may yet
// start agents again: the startup restore is installed, the developer's
// setting was on when the reaper first asked, and the pass has not finished.
// The reaper waits that long before it takes a dead runtime for a reason to
// terminate a session (lifecycle/lado_startup_hold.go); without the wait, a
// slow start would let it terminate the sessions the pass is about to start
// again. With the setting off nothing waits, as upstream.
func (m *Manager) AwaitingStartupRestore() bool {
	state := m.ladoStartupRestore
	if state == nil || state.Enabled == nil {
		return false
	}
	select {
	case <-m.startupBackgroundReconcileDone:
		return false
	default:
	}
	state.holdOnce.Do(func() {
		state.holds = state.Enabled(context.Background())
	})
	return state.holds
}

// agentMissingAtStartup settles a session the startup health pass found
// without its agent: it starts the agent again when the setting, the worker
// gate and LADO allow, and records the agent exited otherwise, which is
// upstream's behavior. The pass read stale before it fenced the session, so
// the row is read again here: a session ended meanwhile stays ended.
//
// A start that fails leaves the session exited (reconcileLive). A Chat
// session then stays resumable; a terminal session's runtime is gone, so the
// reaper terminates it next, and a person restores it from the archive.
func (m *Manager) agentMissingAtStartup(ctx context.Context, stale domain.SessionRecord) error {
	rec, found, err := m.store.GetSession(ctx, stale.ID)
	if err != nil {
		return err
	}
	if !found || !rec.CreatedAt.Equal(stale.CreatedAt) || rec.IsTerminated {
		return nil
	}
	if !m.restoresAtStartup(ctx, rec) {
		return m.recordAgentExited(ctx, rec)
	}
	// The same interlock Resume and Restore keep: no agent starts while its
	// install or update runs.
	releaseHarness, err := m.beginHarnessUse(rec.Harness)
	if err != nil {
		m.logger.Info("startup restore: the agent's install is running; the session stays stopped", "sessionID", rec.ID, "harness", rec.Harness)
		return m.recordAgentExited(ctx, rec)
	}
	defer releaseHarness()
	if err := m.reconcileLive(ctx, rec); err != nil {
		return err
	}
	// reconcileLive ends some sessions without an error (a legacy Scratch
	// terminal session is one-shot): only one that runs was started again.
	after, found, err := m.store.GetSession(ctx, rec.ID)
	if err != nil || !found || after.IsTerminated || after.Activity.State == domain.ActivityExited {
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
	state := m.ladoStartupRestore
	if state == nil || state.Enabled == nil || rec.IsTerminated || rec.HibernatedAt != nil || rec.Activity.State == domain.ActivityExited {
		return false
	}
	if !state.Enabled(ctx) {
		return false
	}
	if err := m.authorizeLadoRestore(ctx, rec.ID); err != nil {
		m.logger.Info("startup restore: the worker gate keeps a session stopped", "sessionID", rec.ID, "reason", err)
		return false
	}
	if state.Refuse != nil {
		if reason := state.Refuse(ctx, rec.ID); reason != "" {
			m.logger.Info("startup restore: LADO keeps a session stopped", "sessionID", rec.ID, "reason", reason)
			return false
		}
	}
	return true
}

// turnWasRunning reports whether a session in state was working in a turn.
// A session blocked on an approval or a question waited for a person, and one
// idle or at its prompt had finished its turn: none of them is told to go on,
// because LADO never answers for the owner.
func turnWasRunning(state domain.ActivityState) bool {
	return state == domain.ActivityActive
}
