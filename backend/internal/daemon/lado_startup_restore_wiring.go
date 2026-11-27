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

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladowatch"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

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

// ladoRuntimeDeathHolder is the lifecycle manager, whose reaper waits for the
// startup health pass.
type ladoRuntimeDeathHolder interface {
	SetLadoRuntimeDeathHold(hold func() bool)
}

// A signature that drifts fails the build rather than leaving the restore
// out at run time.
var (
	_ ladoStartupRestoreSessions = (*sessionmanager.Manager)(nil)
	_ ladoStartupRestoreSettings = (*ladoqueue.Service)(nil)
	_ ladoStartupRestoreListener = (*ladowatch.Service)(nil)
	_ ladoRuntimeDeathHolder     = (*lifecycle.Manager)(nil)
)

// wireLadoStartupRestore installs the startup restore: the switch and the
// listener in the session manager first, the reaper's hold after, because the
// reaper already runs and reads the session manager through the hold. A
// session manager that takes no restore, as in tests of other parts, installs
// nothing. Settings that cannot be read start nothing, which is upstream
// behavior.
func wireLadoStartupRestore(sessions any, settings ladoStartupRestoreSettings, listener ladoStartupRestoreListener, reaper ladoRuntimeDeathHolder, log *slog.Logger) {
	manager, ok := sessions.(ladoStartupRestoreSessions)
	if !ok || sessions == nil {
		if sessions != nil {
			log.Error("LADO cannot start sessions again after a reboot: the session manager takes no startup restore")
		}
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
		Restored: func(id domain.SessionID, interrupted bool) {
			if listener != nil {
				listener.SessionRestored(id, interrupted)
			}
		},
	})
	if reaper != nil {
		reaper.SetLadoRuntimeDeathHold(manager.AwaitingStartupRestore)
	}
}
