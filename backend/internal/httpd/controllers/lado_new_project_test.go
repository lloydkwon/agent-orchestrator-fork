package controllers_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoproject"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// A project registered through the API reads LADO mode on and three
// executions at once from the LADO settings API (#3); one registered with
// tracker intake on reads LADO mode off, and one registered before, without
// stored settings, keeps LADO mode off and one execution.
func TestLadoAPI_NewProjectsStartInLadoMode(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	store := sqlitetest.MustOpen(t)
	lado := ladoproject.New(ladoproject.Deps{Store: store, Projects: store})
	srv := newLadoTestServer(t, httpd.APIDeps{
		Projects:     projectsvc.NewWithDeps(projectsvc.Deps{Store: store, LadoDefaults: lado}),
		LadoProjects: lado,
	})
	if err := store.UpsertProject(context.Background(), domain.ProjectRecord{ID: "old", Path: gitRepo(t, "old"), DisplayName: "old", RegisteredAt: time.Now().UTC().Truncate(time.Second)}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	register := func(body string) {
		t.Helper()
		resp, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", body)
		if status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("POST /api/v1/projects %s: status = %d, body %s", body, status, resp)
		}
	}
	settings := func(id string) controllers.LadoProjectSettingsResponse {
		t.Helper()
		body, status, _ := doRequest(t, srv, "GET", "/api/v1/lado/projects/"+id+"/settings", "")
		if status != http.StatusOK {
			t.Fatalf("GET settings of %s: status = %d, body %s", id, status, body)
		}
		return decodeLadoSettings(t, body)
	}

	register(`{"path":` + quote(gitRepo(t, "mer")) + `,"projectId":"mer"}`)
	register(`{"path":` + quote(gitRepo(t, "intake")) + `,"projectId":"intake","config":{"trackerIntake":{"enabled":true,"assignee":"alice"}}}`)

	for _, tc := range []struct {
		id       string
		ladoMode bool
		cap      int
	}{
		{"mer", true, domain.LadoNewProjectMaxConcurrent},
		{"intake", false, domain.LadoNewProjectMaxConcurrent},
		{"old", false, domain.LadoDefaultProjectMaxConcurrent},
	} {
		if got := settings(tc.id); got.LadoMode != tc.ladoMode || got.MaxConcurrent != tc.cap {
			t.Fatalf("%s settings = %+v, want LADO mode %v and cap %d", tc.id, got, tc.ladoMode, tc.cap)
		}
	}
}
