package acp

// LADO fork (2026-11-27): the Claude ACP bridge's live background tasks. AO's
// bridge patch (frontend/scripts/build-acp-runtime-helpers.mjs,
// patchClaudeBackgroundTaskReport) sends BackgroundTasksMethod as a
// notification whenever its task list changes and answers it as a request
// with the list it has now.

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// BackgroundTasksMethod is the bridge's background-task notice and request.
const BackgroundTasksMethod = "_ao/session/background_tasks"

// BridgeBackgroundTask is one task as the bridge reports it. StartedAt is in
// Unix milliseconds; 0 when the bridge did not see the task start.
type BridgeBackgroundTask struct {
	TaskID      string `json:"taskId"`
	Type        string `json:"type"`
	Description string `json:"description"`
	StartedAt   int64  `json:"startedAt"`
}

// BridgeBackgroundTasks decodes the bridge's task list into AO's: always a
// non-nil list, as the bridge only reports what it knows.
func BridgeBackgroundTasks(raw []BridgeBackgroundTask) []domain.BackgroundTask {
	out := make([]domain.BackgroundTask, 0, len(raw))
	for _, t := range raw {
		task := domain.BackgroundTask{Type: t.Type, Description: t.Description}
		if t.StartedAt > 0 {
			task.StartedAt = time.UnixMilli(t.StartedAt).UTC()
		}
		out = append(out, task)
	}
	return domain.NormalizeBackgroundTasks(out)
}

func (c *conversation) handleBackgroundTasks(params json.RawMessage) error {
	var notice struct {
		SessionID string                 `json:"sessionId"`
		Tasks     []BridgeBackgroundTask `json:"tasks"`
	}
	if err := json.Unmarshal(params, &notice); err != nil {
		return err
	}
	c.mu.Lock()
	id := c.sessionID
	c.mu.Unlock()
	if notice.SessionID == "" || (id != "" && notice.SessionID != id) {
		return errors.New("background-task notice names another session")
	}
	c.emit(ports.ChatEvent{Kind: ports.ChatEventBackgroundTasks, BackgroundTasks: BridgeBackgroundTasks(notice.Tasks)})
	return nil
}
