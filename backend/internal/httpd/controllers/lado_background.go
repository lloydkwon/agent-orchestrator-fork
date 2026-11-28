package controllers

// LADO fork (2026-11-27): the sessions idle on their own background work
// (background shells, Monitors, subagents), which the sidebar shows 💻. Like
// the rest of /api/v1/lado, it serves the loopback listener only.

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// LadoBackgroundService lists what AO knows of the live sessions' background
// tasks (*sqlite.Store).
type LadoBackgroundService interface {
	ListLiveLadoSessionBackground(ctx context.Context) ([]domain.SessionBackgroundTasks, error)
}

const ladoBackgroundPath = "/api/v1/lado/background-tasks"

// LadoBackgroundTaskResponse is one live background task.
type LadoBackgroundTaskResponse struct {
	Type        string     `json:"type" description:"shell, subagent, monitor or workflow, as the agent names it."`
	Description string     `json:"description" description:"The command's first line or the task's description, at most 80 characters."`
	StartedAt   *time.Time `json:"startedAt,omitempty" description:"When the task started; omitted when unknown."`
}

// LadoSessionBackgroundResponse is a session's live background tasks; an
// empty list means none run.
type LadoSessionBackgroundResponse struct {
	SessionID string                       `json:"sessionId"`
	Tasks     []LadoBackgroundTaskResponse `json:"tasks"`
	Source    string                       `json:"source" enum:"bridge,transcript" description:"bridge: the agent bridge's own report; transcript: read from the conversation file and the process table."`
	UpdatedAt time.Time                    `json:"updatedAt"`
}

// LadoBackgroundListResponse lists the sessions AO knows the background
// tasks of; a session absent from it is not known.
type LadoBackgroundListResponse struct {
	Sessions []LadoSessionBackgroundResponse `json:"sessions"`
}

func (c *LadoController) registerBackground(r chi.Router) {
	r.Get("/lado/background-tasks", c.listBackground)
}

func (c *LadoController) listBackground(w http.ResponseWriter, r *http.Request) {
	if c.Background == nil {
		apispec.NotImplemented(w, r, "GET", ladoBackgroundPath)
		return
	}
	list, err := c.Background.ListLiveLadoSessionBackground(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	resp := LadoBackgroundListResponse{Sessions: make([]LadoSessionBackgroundResponse, 0, len(list))}
	for _, b := range list {
		tasks := make([]LadoBackgroundTaskResponse, 0, len(b.Tasks))
		for _, t := range b.Tasks {
			task := LadoBackgroundTaskResponse{Type: t.Type, Description: t.Description}
			if !t.StartedAt.IsZero() {
				at := t.StartedAt.UTC()
				task.StartedAt = &at
			}
			tasks = append(tasks, task)
		}
		resp.Sessions = append(resp.Sessions, LadoSessionBackgroundResponse{
			SessionID: string(b.SessionID), Tasks: tasks, Source: b.Source, UpdatedAt: b.UpdatedAt.UTC(),
		})
	}
	envelope.WriteJSON(w, http.StatusOK, resp)
}
