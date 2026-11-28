package controllers

// LADO fork: one orchestrator notice by its id (docs/LJY-PRD/TDD.md §5.1).
// A notice's change event names it only, so the desktop app reads its text
// here, on loopback, before it shows the notice as a notification.

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// LadoNoticeService reads one orchestrator notice (*sqlite.Store).
type LadoNoticeService interface {
	LadoOrchestratorNotice(ctx context.Context, id int64) (domain.LadoOrchestratorNotice, bool, error)
}

const ladoNoticePath = "/api/v1/lado/notices/{noticeId}"

// LadoNoticeIDParam names one orchestrator notice.
type LadoNoticeIDParam struct {
	NoticeID string `path:"noticeId" description:"LADO orchestrator notice id."`
}

// LadoNoticeResponse is one line LADO told, or holds to tell, a project's
// orchestrator.
type LadoNoticeResponse struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"projectId"`
	Key       string `json:"key" description:"What the notice tells of, as kind:subject; the kind before the first colon sorts it (revision, integration, failed, started, ...)."`
	Text      string `json:"text" description:"The line, starting with \"LADO 알림: \"; it never carries plan text."`
	// OccurredAt is when the event happened, CreatedAt when LADO queued it.
	OccurredAt time.Time `json:"occurredAt"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (c *LadoController) registerNotices(r chi.Router) {
	r.Get("/lado/notices/{noticeId}", c.getNotice)
}

func (c *LadoController) getNotice(w http.ResponseWriter, r *http.Request) {
	if c.Notices == nil {
		apispec.NotImplemented(w, r, http.MethodGet, ladoNoticePath)
		return
	}
	notFound := func() {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "LADO_NOTICE_NOT_FOUND", "Unknown notice", nil)
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "noticeId"), 10, 64)
	if err != nil || id < 1 {
		notFound()
		return
	}
	n, ok, err := c.Notices.LadoOrchestratorNotice(r.Context(), id)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if !ok {
		notFound()
		return
	}
	envelope.WriteJSON(w, http.StatusOK, LadoNoticeResponse{
		ID: n.ID, ProjectID: string(n.ProjectID), Key: n.Key, Text: n.Text,
		OccurredAt: n.OccurredAt.UTC(), CreatedAt: n.CreatedAt.UTC(),
	})
}
