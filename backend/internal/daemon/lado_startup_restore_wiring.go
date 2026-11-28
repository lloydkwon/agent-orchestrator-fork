package daemon

// LADO fork: the startup restore in the daemon. With the developer's setting
// on (Settings → Runs, stored with the dispatch settings), the session
// manager's startup health pass starts again the agents a reboot stopped
// (session_manager/lado_startup_restore.go). The worker watch hears which it
// started, so it tells a LADO worker whose turn was cut off to go on
// (ladowatch/restart.go), and the reaper waits for the pass before it takes a
// dead runtime for a reason to terminate a session
// (lifecycle/lado_startup_hold.go).

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladowatch"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// ladoStartupHoldMax is the longest the reaper waits for the startup health
// pass. The pass normally ends within seconds of the listener; if a step
// before it hangs, the reaper must not wait for the daemon's whole life.
const ladoStartupHoldMax = 2 * time.Minute

// ladoStartupHold is the reaper's wait for the startup restore. The reaper
// starts with the lifecycle, long before the session manager exists, and
// ticks every five seconds: the hold is installed with it and waits from the
// first tick, and wireLadoStartupRestore binds it to the session manager's
// answer once there is one.
type ladoStartupHold struct {
	clock   func() time.Time
	started time.Time
	// awaiting is the session manager's AwaitingStartupRestore, nil until
	// the wiring bound it. The reaper's goroutine reads it.
	awaiting atomic.Pointer[func() bool]
}

func newLadoStartupHold(clock func() time.Time) *ladoStartupHold {
	return &ladoStartupHold{clock: clock, started: clock()}
}

// bind hands the hold the session manager's answer.
func (h *ladoStartupHold) bind(awaiting func() bool) {
	h.awaiting.Store(&awaiting)
}

// held reports whether an observed runtime death waits: until
// ladoStartupHoldMax passed, while nothing is bound yet or the session
// manager says the startup health pass may still start agents again.
func (h *ladoStartupHold) held() bool {
	if h.clock().Sub(h.started) > ladoStartupHoldMax {
		return false
	}
	if awaiting := h.awaiting.Load(); awaiting != nil {
		return (*awaiting)()
	}
	return true
}

// ladoStartupRestoreSessions is the session manager's side of the startup
// restore.
type ladoStartupRestoreSessions interface {
	SetLadoStartupRestore(sessionmanager.LadoStartupRestore)
	AwaitingStartupRestore() bool
}

// ladoStartupRestoreSettings reads the dispatch settings, which hold the
// switch (*ladoqueue.Service).
type ladoStartupRestoreSettings interface {
	Settings(ctx context.Context) (domain.LadoDispatchSettings, error)
}

// ladoStartupRestoreListener hears which sessions started again
// (*ladowatch.Service).
type ladoStartupRestoreListener interface {
	SessionRestored(id domain.SessionID, interrupted bool)
}

// ladoStartupRestoreExecutions reads a session's LADO execution
// (*sqlite.Store).
type ladoStartupRestoreExecutions interface {
	LadoExecution(ctx context.Context, sessionID domain.SessionID) (domain.LadoExecution, bool, error)
}

// ladoRuntimeDeathHolder is the lifecycle manager, whose reaper waits for the
// startup health pass.
type ladoRuntimeDeathHolder interface {
	SetLadoRuntimeDeathHold(hold func() bool)
}

// A signature that drifts fails the build rather than leaving the restore
// out at run time.
var (
	_ ladoStartupRestoreSessions   = (*sessionmanager.Manager)(nil)
	_ ladoStartupRestoreSettings   = (*ladoqueue.Service)(nil)
	_ ladoStartupRestoreListener   = (*ladowatch.Service)(nil)
	_ ladoStartupRestoreExecutions = (*sqlite.Store)(nil)
	_ ladoRuntimeDeathHolder       = (*lifecycle.Manager)(nil)
)

// installLadoStartupHold makes the reaper wait for the startup restore from
// its first tick. The daemon calls it right after the lifecycle starts.
func installLadoStartupHold(reaper ladoRuntimeDeathHolder, clock func() time.Time) *ladoStartupHold {
	hold := newLadoStartupHold(clock)
	reaper.SetLadoRuntimeDeathHold(hold.held)
	return hold
}

// wireLadoStartupRestore installs the startup restore: the switch, LADO's
// refusals and the listener in the session manager first, the reaper's hold
// after, because the reaper already runs and reads the session manager
// through the hold. A session manager that takes no restore, as in tests of
// other parts, holds nothing. Settings or an execution that cannot be read
// start nothing, which is upstream behavior.
func wireLadoStartupRestore(sessions any, settings ladoStartupRestoreSettings, listener ladoStartupRestoreListener,
	executions ladoStartupRestoreExecutions, hold *ladoStartupHold, log *slog.Logger) {
	manager, ok := sessions.(ladoStartupRestoreSessions)
	if !ok || sessions == nil {
		if sessions != nil {
			log.Error("LADO cannot start sessions again after a reboot: the session manager takes no startup restore")
		}
		hold.bind(func() bool { return false })
		return
	}
	manager.SetLadoStartupRestore(sessionmanager.LadoStartupRestore{
		Enabled: func(ctx context.Context) bool {
			current, err := settings.Settings(ctx)
			if err != nil {
				log.Warn("LADO could not read whether to start sessions again after a reboot; they stay stopped", "err", err)
				return false
			}
			return current.RestoreOnStart.On()
		},
		Refuse: func(ctx context.Context, id domain.SessionID) string {
			return ladoStartupRefusal(ctx, executions, id, hold.clock().UTC(), log)
		},
		Restored: func(id domain.SessionID, interrupted bool) {
			if listener != nil {
				listener.SessionRestored(id, interrupted)
			}
		},
	})
	hold.bind(manager.AwaitingStartupRestore)
}

// ladoStartupRefusal names why LADO keeps a session stopped at startup, ""
// when it does not: an execution past its maximum run time without a pull
// request is one the dispatcher stops on its next pass, and while the daemon
// was down nothing marked it timed out, so the worker gate does not refuse it
// yet. A session that is no execution is not LADO's to refuse.
func ladoStartupRefusal(ctx context.Context, executions ladoStartupRestoreExecutions, id domain.SessionID, now time.Time, log *slog.Logger) string {
	if executions == nil {
		return ""
	}
	exec, ok, err := executions.LadoExecution(ctx, id)
	if err != nil {
		log.Warn("LADO could not read a session's execution at startup; the session stays stopped", "session", id, "err", err)
		return "its LADO execution could not be read"
	}
	if !ok {
		return ""
	}
	if !exec.DeadlineAt.IsZero() && !now.Before(exec.DeadlineAt) && exec.PR.URL == "" {
		return "its LADO execution is past its maximum run time"
	}
	return ""
}
