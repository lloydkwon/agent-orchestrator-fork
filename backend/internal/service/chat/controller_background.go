package chat

// LADO fork (2026-11-27): a session idle on its own background work. The
// provider reports its live background tasks (background shells, Monitors,
// subagents) when they change, and the controller reads them once when it
// starts consuming, since a daemon restart leaves the provider running while
// it misses the notices. They go to lado_session_background; the sidebar
// shows 💻 from them and the LADO stall watch leaves such a worker alone.

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// BackgroundTaskWriter records a session's live background tasks; a nil list
// means AO does not know them (*sqlite.Store).
type BackgroundTaskWriter interface {
	SetLadoSessionBackground(ctx context.Context, id domain.SessionID, tasks []domain.BackgroundTask, at time.Time) error
}

const backgroundTaskReadTimeout = 5 * time.Second

// applyBackgroundTasks records a pushed background-task list.
func (c *Controller) applyBackgroundTasks(ctx context.Context, tasks []domain.BackgroundTask, now time.Time) {
	if tasks == nil {
		tasks = []domain.BackgroundTask{}
	}
	c.backgroundMu.Lock()
	defer c.backgroundMu.Unlock()
	c.backgroundNotices.Add(1)
	c.writeBackgroundTasks(ctx, tasks, now)
}

// readBackgroundTasks reads the provider's live background tasks once, off
// the critical path. A provider that cannot tell forgets what AO knew.
func (c *Controller) readBackgroundTasks() {
	reader, ok := c.conv.(ports.ChatBackgroundTaskReader)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), backgroundTaskReadTimeout)
	defer cancel()
	before := c.backgroundNotices.Load()
	tasks, known, err := reader.BackgroundTasks(ctx)
	if err != nil {
		c.log.Debug("chat background task read failed", "session", c.sessionID, "error", err)
		return
	}
	if !known {
		tasks = nil
	} else if tasks == nil {
		tasks = []domain.BackgroundTask{}
	}
	// The check and the write hold the notices' lock: a notice cannot slip
	// in between and be overwritten by this older read.
	c.backgroundMu.Lock()
	defer c.backgroundMu.Unlock()
	if c.backgroundNotices.Load() != before {
		return // a notice arrived meanwhile and is at least as new
	}
	c.writeBackgroundTasks(ctx, tasks, c.now())
}

func (c *Controller) writeBackgroundTasks(ctx context.Context, tasks []domain.BackgroundTask, now time.Time) {
	if c.sessionID == "" || c.reviewID != "" {
		return // a reviewer's conversation is not its worker's background
	}
	writer, ok := c.store.(BackgroundTaskWriter)
	if !ok {
		return
	}
	if err := writer.SetLadoSessionBackground(ctx, c.sessionID, tasks, now); err != nil {
		c.log.Debug("failed to record chat background tasks", "session", c.sessionID, "error", err)
	}
}
