package project_test

// LADO fork: a newly registered project starts with LADO mode on and three
// executions at once (#3).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoproject"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// newLadoManager builds a Manager over a real store whose new projects get
// their LADO settings from ladoproject.Service, as the daemon wires them.
func newLadoManager(t *testing.T) (project.Manager, *sqlite.Store) {
	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lado := ladoproject.New(ladoproject.Deps{Store: store, Projects: store})
	return project.NewWithDeps(project.Deps{Store: store, LadoDefaults: lado}), store
}

func ladoSettings(t *testing.T, store *sqlite.Store, id string) domain.LadoProjectSettings {
	t.Helper()
	got, err := store.LadoProjectSettings(context.Background(), domain.ProjectID(id))
	if err != nil {
		t.Fatalf("LadoProjectSettings(%s): %v", id, err)
	}
	return got
}

func TestManager_AddStartsNewProjectsInLadoMode(t *testing.T) {
	configureCommitter(t)
	ctx := context.Background()
	m, store := newLadoManager(t)

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("single")}); err != nil {
		t.Fatalf("Add single repo: %v", err)
	}
	parent := t.TempDir()
	gitRepoWithCommit(t, filepath.Join(parent, "api"))
	if _, err := m.Add(ctx, project.AddInput{Path: parent, ProjectID: ptr("ws"), AsWorkspace: true}); err != nil {
		t.Fatalf("Add workspace: %v", err)
	}

	for _, id := range []string{"single", "ws"} {
		got := ladoSettings(t, store, id)
		if !got.LadoMode || got.MaxConcurrent != domain.LadoNewProjectMaxConcurrent || got.OverlapWait {
			t.Fatalf("%s settings = %+v, want LADO mode on, cap %d and the overlap wait off", id, got, domain.LadoNewProjectMaxConcurrent)
		}
	}
}

// A project registered with tracker intake on keeps LADO mode off: LADO
// projects take issues by import.
func TestManager_AddKeepsTrackerIntakeProjectsOutOfLadoMode(t *testing.T) {
	ctx := context.Background()
	m, store := newLadoManager(t)
	config := domain.ProjectConfig{TrackerIntake: domain.TrackerIntakeConfig{Enabled: true, Assignee: "alice"}}

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("intake"), Config: &config}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := ladoSettings(t, store, "intake"); got.LadoMode || got.MaxConcurrent != domain.LadoNewProjectMaxConcurrent {
		t.Fatalf("settings = %+v, want LADO mode off and cap %d", got, domain.LadoNewProjectMaxConcurrent)
	}
}

// Registering a project id again keeps the settings its earlier registration
// left, and a project registered before #3, without stored settings, keeps
// reading LADO mode off and one execution at once.
func TestManager_AddKeepsExistingLadoSettings(t *testing.T) {
	ctx := context.Background()
	m, store := newLadoManager(t)

	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "old", Path: gitRepo(t), DisplayName: "old", RegisteredAt: time.Now().UTC(), Kind: domain.ProjectKindSingleRepo}); err != nil {
		t.Fatalf("seed project without settings: %v", err)
	}
	if got := ladoSettings(t, store, "old"); got.LadoMode || got.MaxConcurrent != domain.LadoDefaultProjectMaxConcurrent {
		t.Fatalf("project without settings = %+v, want LADO mode off and cap %d", got, domain.LadoDefaultProjectMaxConcurrent)
	}

	if _, err := store.SetLadoMode(ctx, "kept", false, time.Now()); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := store.SetLadoProjectMaxConcurrent(ctx, "kept", 5, domain.LadoAuditEvent{At: time.Now().UTC(), Actor: "api", Action: "project.max_concurrent", SubjectKind: "project", SubjectID: "kept", Payload: []byte(`{"from":1,"to":5}`)}); err != nil {
		t.Fatalf("seed cap: %v", err)
	}
	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("kept")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := ladoSettings(t, store, "kept"); got.LadoMode || got.MaxConcurrent != 5 {
		t.Fatalf("settings = %+v, want the kept LADO mode off and cap 5", got)
	}
}

// Without LADO defaults wired, a new project has no stored settings.
func TestManager_AddWithoutLadoDefaultsStoresNoSettings(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := project.New(store)

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("plain")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := ladoSettings(t, store, "plain"); got.LadoMode || got.MaxConcurrent != domain.LadoDefaultProjectMaxConcurrent {
		t.Fatalf("settings = %+v, want LADO mode off and cap %d", got, domain.LadoDefaultProjectMaxConcurrent)
	}
}

type failingLadoDefaults struct{ calls int }

func (f *failingLadoDefaults) ApplyNewProjectDefaults(context.Context, domain.ProjectRecord) error {
	f.calls++
	return errors.New("disk full")
}

// A failure to store the LADO settings leaves the project registered.
func TestManager_AddSucceedsWhenLadoDefaultsFail(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	defaults := &failingLadoDefaults{}
	m := project.NewWithDeps(project.Deps{Store: store, LadoDefaults: defaults})

	if _, err := m.Add(ctx, project.AddInput{Path: gitRepo(t), ProjectID: ptr("mer")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if defaults.calls != 1 {
		t.Fatalf("LADO defaults applied %d times, want once", defaults.calls)
	}
	if _, ok, err := store.GetProject(ctx, "mer"); err != nil || !ok {
		t.Fatalf("GetProject = %v, %v, want the project registered", ok, err)
	}
}
