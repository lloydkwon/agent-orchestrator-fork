package daemon

// LADO fork (#4): a project's live orchestrators take the standing
// instructions of its new LADO mode.

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// ladoInstructionRefreshTick is how often queued orchestrators are tried
// again while they are busy.
const ladoInstructionRefreshTick = 2 * time.Second

// ladoInstructionRefresher is the session manager: it queues a project's live
// orchestrators and relaunches each one in its own session once it is quiet.
type ladoInstructionRefresher interface {
	NoteLadoModeChanged(domain.ProjectID)
	RefreshPendingInstructions(context.Context) (int, error)
}

// The daemon hands wireLadoInstructionRefresh the session manager behind an
// interface; a signature that drifts would turn the refresh off silently.
var _ ladoInstructionRefresher = (*sessionmanager.Manager)(nil)

// ladoModeChangeHook is LADO's project settings service, which tells who is
// interested that a project's LADO mode changed.
type ladoModeChangeHook interface {
	SetOnLadoModeChanged(func(domain.ProjectID, bool))
}

// wireLadoInstructionRefresh queues a project's live orchestrators whenever
// its LADO mode changes, and returns the loop that relaunches them, which the
// daemon starts once it serves. It returns nil when the session manager
// cannot relaunch orchestrators.
func wireLadoInstructionRefresh(projects ladoModeChangeHook, sessions any, log *slog.Logger) func(context.Context) {
	refresher, ok := sessions.(ladoInstructionRefresher)
	if !ok {
		return nil
	}
	kick := make(chan struct{}, 1)
	projects.SetOnLadoModeChanged(func(projectID domain.ProjectID, _ bool) {
		refresher.NoteLadoModeChanged(projectID)
		select {
		case kick <- struct{}{}:
		default:
		}
	})
	return func(ctx context.Context) {
		runLadoInstructionRefresh(ctx, refresher, kick, ladoInstructionRefreshTick, log)
	}
}

// runLadoInstructionRefresh tries the queued orchestrators when a mode
// changes and on every tick, until ctx ends.
func runLadoInstructionRefresh(ctx context.Context, refresher ladoInstructionRefresher, kick <-chan struct{}, tick time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
		case <-ticker.C:
		}
		if _, err := refresher.RefreshPendingInstructions(ctx); err != nil && ctx.Err() == nil {
			log.Warn("LADO orchestrator instruction refresh failed", "err", err)
		}
	}
}
