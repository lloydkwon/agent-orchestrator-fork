package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	reportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/report"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// listedReport is the part of a GET /api/v1/reports item these tests read.
type listedReport struct {
	SessionID string `json:"sessionId"`
	ProjectID string `json:"projectId"`
	State     string `json:"state"`
	Outputs   []struct {
		Reference string `json:"reference"`
	} `json:"outputs"`
}

// The whole POST /api/v1/reports path, with the real service and store, for
// a session without a project (GitHub issue #8): ao report --checkpoint,
// --done with an artifact, --needs-input and an output-only report are all
// stored and listed by session, a project session reports as before, and a
// report with invalid content is a 400 naming the broken rule.
func TestReportsAPI_StandaloneSessionReports(t *testing.T) {
	ctx := context.Background()
	st := sqlitetest.MustOpen(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.UpsertProject(ctx, domain.ProjectRecord{ID: "ao", Path: "/tmp/ao", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	newWorker := func(project domain.ProjectID) domain.SessionRecord {
		session, err := st.CreateSession(ctx, domain.SessionRecord{
			ProjectID: project, Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
			Activity:  domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	worker := newWorker("ao")
	standalone := newWorker("")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil,
		httpd.APIDeps{Reports: reportsvc.New(reportsvc.Deps{Store: st})}, httpd.ControlDeps{}))
	defer srv.Close()

	post := func(body string) ([]byte, int) {
		t.Helper()
		got, status, _ := doRequest(t, srv, "POST", "/api/v1/reports", body)
		return got, status
	}
	for _, body := range []string{
		`{"sessionId":"` + string(standalone.ID) + `","state":"checkpoint","note":"halfway"}`,
		`{"sessionId":"` + string(standalone.ID) + `","state":"done","note":"finished","outputs":[{"kind":"artifact","reference":"evidence.md"}]}`,
		`{"sessionId":"` + string(standalone.ID) + `","state":"needs_input","note":"which option?"}`,
		`{"sessionId":"` + string(standalone.ID) + `","outputs":[{"kind":"artifact","reference":"report.html"}]}`,
	} {
		if got, status := post(body); status != http.StatusCreated {
			t.Fatalf("POST %s: status=%d body=%s", body, status, got)
		}
	}
	if got, status := post(`{"sessionId":"` + string(worker.ID) + `","state":"checkpoint","note":"project work"}`); status != http.StatusCreated {
		t.Fatalf("project report: status=%d body=%s", status, got)
	}

	list := func(query string) []listedReport {
		t.Helper()
		body, status, _ := doRequest(t, srv, "GET", "/api/v1/reports?"+query, "")
		if status != http.StatusOK {
			t.Fatalf("GET ?%s: status=%d body=%s", query, status, body)
		}
		var response struct {
			Reports []listedReport `json:"reports"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("GET ?%s: decode %s: %v", query, body, err)
		}
		return response.Reports
	}
	reports := list("sessionId=" + string(standalone.ID))
	if len(reports) != 4 {
		t.Fatalf("standalone session lists %d reports, want 4: %+v", len(reports), reports)
	}
	wantStates := []string{"checkpoint", "done", "needs_input", ""}
	for i, report := range reports {
		if report.SessionID != string(standalone.ID) || report.ProjectID != "" || report.State != wantStates[i] {
			t.Fatalf("report %d = %+v, want state %q of %s without a project", i, report, wantStates[i], standalone.ID)
		}
	}
	if len(reports[1].Outputs) != 1 || reports[1].Outputs[0].Reference != "evidence.md" {
		t.Fatalf("done report outputs = %+v", reports[1].Outputs)
	}
	if project := list("projectId=ao"); len(project) != 1 || project[0].SessionID != string(worker.ID) {
		t.Fatalf("project reports = %+v, want only %s's", project, worker.ID)
	}

	for _, tc := range []struct {
		method, path, body string
		code, detail       string
	}{
		{"POST", "/api/v1/reports", `{"sessionId":"` + string(standalone.ID) + `","state":"checkpoint","note":" "}`, "INVALID_REPORT", `state \"checkpoint\" needs a note`},
		{"POST", "/api/v1/reports", `{"sessionId":"` + string(worker.ID) + `","outputs":[{"kind":"pr_created","reference":"https://example.com/x"}]}`, "INVALID_REPORT", "output 1: pr_created needs a pull request or merge request URL"},
		{"GET", "/api/v1/reports?projectId=ao&sessionId=" + string(standalone.ID), "", "REPORT_SCOPE_CONFLICT", "not both"},
		{"GET", "/api/v1/reports", "", "REPORT_PROJECT_REQUIRED", "session id is required"},
	} {
		body, status, _ := doRequest(t, srv, tc.method, tc.path, tc.body)
		if status != http.StatusBadRequest || !reportContainsAll(string(body), tc.code, tc.detail) {
			t.Fatalf("%s %s %s: status=%d body=%s, want 400 %s", tc.method, tc.path, tc.body, status, body, tc.code)
		}
	}
}
