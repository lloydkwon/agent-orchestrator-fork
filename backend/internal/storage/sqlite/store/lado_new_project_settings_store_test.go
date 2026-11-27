package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A newly registered project gets a settings row with LADO mode on and the
// new project cap (#3); one registered with tracker intake on gets LADO mode
// off. A project that has a row keeps it whole, and one without a row, such
// as a project registered before #3, keeps reading the old defaults.
func TestCreateLadoNewProjectSettings(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	s, raw := openLadoQueueStore(t)
	read := func(project string) domain.LadoProjectSettings {
		t.Helper()
		got, err := s.LadoProjectSettings(ctx, domain.ProjectID(project))
		if err != nil {
			t.Fatalf("LadoProjectSettings %s: %v", project, err)
		}
		return got
	}

	// A project without a row reads LADO mode off and cap 1.
	if got := read("old"); got.LadoMode || got.MaxConcurrent != domain.LadoDefaultProjectMaxConcurrent || !got.UpdatedAt.IsZero() {
		t.Fatalf("project without a row = %+v, want LADO mode off and cap %d", got, domain.LadoDefaultProjectMaxConcurrent)
	}

	// A new project gets LADO mode on and cap 3.
	created, err := s.CreateLadoNewProjectSettings(ctx, "new", true, domain.LadoNewProjectMaxConcurrent, at)
	if err != nil || !created {
		t.Fatalf("CreateLadoNewProjectSettings new = %v, %v, want created", created, err)
	}
	if got := read("new"); !got.LadoMode || got.MaxConcurrent != domain.LadoNewProjectMaxConcurrent || got.OverlapWait || !got.UpdatedAt.Equal(at) {
		t.Fatalf("new project = %+v, want LADO mode on, cap %d, overlap wait off, updated at %s", got, domain.LadoNewProjectMaxConcurrent, at)
	}

	// A tracker intake project gets LADO mode off and cap 3.
	created, err = s.CreateLadoNewProjectSettings(ctx, "intake", false, domain.LadoNewProjectMaxConcurrent, at)
	if err != nil || !created {
		t.Fatalf("CreateLadoNewProjectSettings intake = %v, %v, want created", created, err)
	}
	if got := read("intake"); got.LadoMode || got.MaxConcurrent != domain.LadoNewProjectMaxConcurrent {
		t.Fatalf("intake project = %+v, want LADO mode off and cap %d", got, domain.LadoNewProjectMaxConcurrent)
	}

	// A row that exists stays whole: one set through the settings, and one
	// stored without a cap, which took the column default 1.
	if _, err := s.SetLadoMode(ctx, "kept", false, at); err != nil {
		t.Fatalf("SetLadoMode kept: %v", err)
	}
	event := domain.LadoAuditEvent{At: at, Actor: "human", Action: "project.max_concurrent_changed", SubjectKind: "project", SubjectID: "kept"}
	if err := s.SetLadoProjectMaxConcurrent(ctx, "kept", 5, event); err != nil {
		t.Fatalf("SetLadoProjectMaxConcurrent kept: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO lado_project_settings (project_id, lado_mode, updated_at) VALUES ('legacy', 0, ?)`, at); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	later := at.Add(time.Hour)
	for project, wantCap := range map[string]int{"kept": 5, "legacy": domain.LadoDefaultProjectMaxConcurrent} {
		before := read(project)
		created, err := s.CreateLadoNewProjectSettings(ctx, domain.ProjectID(project), true, domain.LadoNewProjectMaxConcurrent, later)
		if err != nil || created {
			t.Fatalf("CreateLadoNewProjectSettings %s = %v, %v, want nothing created", project, created, err)
		}
		got := read(project)
		if got.LadoMode || got.MaxConcurrent != wantCap || !got.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("%s = %+v, want LADO mode off, cap %d and the update time %s kept", project, got, wantCap, before.UpdatedAt)
		}
	}

	// The cap stays within 1 to domain.LadoMaxConcurrent.
	if domain.LadoNewProjectMaxConcurrent < 1 || domain.LadoNewProjectMaxConcurrent > domain.LadoMaxConcurrent {
		t.Fatalf("LadoNewProjectMaxConcurrent = %d, want 1 to %d", domain.LadoNewProjectMaxConcurrent, domain.LadoMaxConcurrent)
	}
	if _, err := s.CreateLadoNewProjectSettings(ctx, "too-many", true, domain.LadoMaxConcurrent+1, at); err == nil {
		t.Fatalf("CreateLadoNewProjectSettings with cap %d stored, want the table to refuse it", domain.LadoMaxConcurrent+1)
	}
}
