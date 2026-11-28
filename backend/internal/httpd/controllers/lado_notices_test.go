package controllers_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
)

// LADO fork: one orchestrator notice reads by its id, for the desktop
// notification its change event asks for; an unknown id is a 404, and the
// route answers 501 until the daemon wires it.
func TestLadoAPI_Notice(t *testing.T) {
	s := newLadoWatchStore(t)
	ctx := context.Background()
	n := domain.LadoOrchestratorNotice{ProjectID: "mer", Key: "revision:lado-task-1:2", Text: "LADO 알림: #42 계획 리비전 2 — 칸반에서 확인",
		OccurredAt: ladoWatchTime, CreatedAt: ladoWatchTime.Add(time.Second)}
	if added, err := s.AddLadoOrchestratorNotice(ctx, n); err != nil || !added {
		t.Fatalf("add notice = %v, %v", added, err)
	}
	all, err := s.ListLadoOrchestratorNotices(ctx, "mer")
	if err != nil || len(all) != 1 {
		t.Fatalf("notices = %+v, %v", all, err)
	}
	srv := newLadoTestServer(t, httpd.APIDeps{LadoNotices: s})

	body, status, _ := doRequest(t, srv, "GET", fmt.Sprintf("/api/v1/lado/notices/%d", all[0].ID), "")
	if status != http.StatusOK {
		t.Fatalf("GET = %d %s", status, body)
	}
	got := decodeLadoJSON[controllers.LadoNoticeResponse](t, body)
	if got.ID != all[0].ID || got.ProjectID != "mer" || got.Key != n.Key || got.Text != n.Text ||
		!got.OccurredAt.Equal(n.OccurredAt) || !got.CreatedAt.Equal(n.CreatedAt) {
		t.Fatalf("notice = %+v", got)
	}
	for _, bad := range []string{"0", "-1", "x", fmt.Sprint(all[0].ID + 1)} {
		body, status, _ := doRequest(t, srv, "GET", "/api/v1/lado/notices/"+bad, "")
		wantLadoErrorCode(t, body, status, http.StatusNotFound, "LADO_NOTICE_NOT_FOUND")
	}

	unwired := newLadoTestServer(t, httpd.APIDeps{})
	if body, status, _ := doRequest(t, unwired, "GET", fmt.Sprintf("/api/v1/lado/notices/%d", all[0].ID), ""); status != http.StatusNotImplemented {
		t.Fatalf("unwired GET = %d %s, want 501", status, body)
	}
}
