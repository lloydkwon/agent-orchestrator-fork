package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestLadoProjectSettingsDefaultToModeOff(t *testing.T) {
	s := newTestStore(t)

	got, err := s.LadoProjectSettings(context.Background(), "mer")
	if err != nil {
		t.Fatalf("LadoProjectSettings: %v", err)
	}
	if got.ProjectID != "mer" || got.LadoMode || got.MaxConcurrent != 1 || !got.UpdatedAt.IsZero() {
		t.Fatalf("settings = %+v, want project mer with LADO mode off, cap 1 and no update time", got)
	}
}

func TestSetLadoModeStoresTheLatestChoice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	turnedOn := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

	stored, err := s.SetLadoMode(ctx, "mer", true, turnedOn)
	if err != nil {
		t.Fatalf("SetLadoMode on: %v", err)
	}
	if !stored.LadoMode || !stored.UpdatedAt.Equal(turnedOn) {
		t.Fatalf("stored = %+v, want LADO mode on at %s", stored, turnedOn)
	}

	turnedOff := turnedOn.Add(time.Hour)
	if _, err := s.SetLadoMode(ctx, "mer", false, turnedOff); err != nil {
		t.Fatalf("SetLadoMode off: %v", err)
	}
	got, err := s.LadoProjectSettings(ctx, "mer")
	if err != nil {
		t.Fatalf("LadoProjectSettings: %v", err)
	}
	if got.LadoMode || !got.UpdatedAt.Equal(turnedOff) {
		t.Fatalf("settings = %+v, want LADO mode off at %s", got, turnedOff)
	}

	other, err := s.LadoProjectSettings(ctx, "other")
	if err != nil {
		t.Fatalf("LadoProjectSettings other: %v", err)
	}
	if other.LadoMode {
		t.Fatalf("another project's settings = %+v, want LADO mode off", other)
	}
}

// #101: a new project does not wait on overlap. Every path that creates a
// settings row stores overlap_wait 0, and a row that has it on keeps it.
func TestLadoOverlapWaitDefaultsOffForNewProjects(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	event := func(project, action string) domain.LadoAuditEvent {
		return domain.LadoAuditEvent{At: at, Actor: "human", Action: action, SubjectKind: "project", SubjectID: project}
	}
	nonce := 0
	// paths are the six writes that create a settings row for a project
	// without one.
	paths := []struct {
		name  string
		write func(t *testing.T, s *sqlite.Store, project string)
	}{
		{"SetLadoMode", func(t *testing.T, s *sqlite.Store, project string) {
			if _, err := s.SetLadoMode(ctx, domain.ProjectID(project), true, at); err != nil {
				t.Fatalf("SetLadoMode: %v", err)
			}
		}},
		{"SetLadoModeWithProof", func(t *testing.T, s *sqlite.Store, project string) {
			nonce++
			proof := domain.LadoPresenceProof{Nonce: fmt.Sprintf("nonce-overlap-%d", nonce), Action: domain.LadoPresenceModeOff, SubjectID: project, ExpiresAt: at.Add(time.Minute), UsedAt: at}
			if _, err := s.SetLadoModeWithProof(ctx, domain.ProjectID(project), false, at, proof, event(project, "project.lado_mode_off")); err != nil {
				t.Fatalf("SetLadoModeWithProof: %v", err)
			}
		}},
		{"SetLadoProjectCheckCommands", func(t *testing.T, s *sqlite.Store, project string) {
			nonce++
			proof := domain.LadoPresenceProof{Nonce: fmt.Sprintf("nonce-overlap-%d", nonce), Action: domain.LadoPresenceCheckCommands, SubjectID: project, ExpiresAt: at.Add(time.Minute), UsedAt: at}
			commands := []domain.LadoCheckCommand{{Name: "test", Command: "go test ./..."}}
			if err := s.SetLadoProjectCheckCommands(ctx, domain.ProjectID(project), domain.LadoCheckCommandsSHA256(nil), commands, &proof, event(project, "project.check_commands_changed")); err != nil {
				t.Fatalf("SetLadoProjectCheckCommands: %v", err)
			}
		}},
		{"SetLadoProjectCheckTimeout", func(t *testing.T, s *sqlite.Store, project string) {
			if err := s.SetLadoProjectCheckTimeout(ctx, domain.ProjectID(project), domain.LadoCheckTimeoutDefault+5, event(project, "project.check_timeout_changed")); err != nil {
				t.Fatalf("SetLadoProjectCheckTimeout: %v", err)
			}
		}},
		{"SetLadoProjectMaxConcurrent", func(t *testing.T, s *sqlite.Store, project string) {
			if err := s.SetLadoProjectMaxConcurrent(ctx, domain.ProjectID(project), 3, event(project, "project.max_concurrent_changed")); err != nil {
				t.Fatalf("SetLadoProjectMaxConcurrent: %v", err)
			}
		}},
		{"CreateLadoNewProjectSettings", func(t *testing.T, s *sqlite.Store, project string) {
			if _, err := s.CreateLadoNewProjectSettings(ctx, domain.ProjectID(project), true, domain.LadoNewProjectMaxConcurrent, at); err != nil {
				t.Fatalf("CreateLadoNewProjectSettings: %v", err)
			}
		}},
	}
	s, raw := openLadoQueueStore(t)
	expect := func(step, project string, want bool) {
		t.Helper()
		settings, err := s.LadoProjectSettings(ctx, domain.ProjectID(project))
		if err != nil || settings.OverlapWait != want {
			t.Fatalf("%s: LadoProjectSettings %s = %+v (%v), want overlap wait %v", step, project, settings, err, want)
		}
	}
	column := func(step, project string, want int) {
		t.Helper()
		var got int
		if err := raw.QueryRowContext(ctx, `SELECT overlap_wait FROM lado_project_settings WHERE project_id = ?`, project).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: overlap_wait column of %s = %d (%v), want %d", step, project, got, err, want)
		}
	}

	// (a) A project without a row reads overlap wait off.
	expect("no row", "fresh", false)

	for i, path := range paths {
		// (b) A row the path creates stores overlap_wait 0.
		created := fmt.Sprintf("new-%d", i)
		path.write(t, s, created)
		expect(path.name+" new row", created, false)
		column(path.name+" new row", created, 0)

		// (c) A row with overlap wait on keeps it through the path: one the
		// setting turned on, and one from before #101 that took the column
		// default 1.
		turnedOn := fmt.Sprintf("on-%d", i)
		if err := s.SetLadoProjectOverlap(ctx, domain.ProjectID(turnedOn), true, nil, event(turnedOn, "project.overlap_changed")); err != nil {
			t.Fatalf("SetLadoProjectOverlap %s: %v", turnedOn, err)
		}
		legacy := fmt.Sprintf("legacy-%d", i)
		if _, err := raw.ExecContext(ctx, `INSERT INTO lado_project_settings (project_id, lado_mode, updated_at) VALUES (?, 0, ?)`, legacy, at); err != nil {
			t.Fatalf("insert legacy row %s: %v", legacy, err)
		}
		column("legacy row", legacy, 1)
		for _, project := range []string{turnedOn, legacy} {
			path.write(t, s, project)
			expect(path.name+" kept", project, true)
			column(path.name+" kept", project, 1)
		}
	}

	// (d) Turning it on for a new project reads on.
	if err := s.SetLadoProjectOverlap(ctx, "fresh", true, nil, event("fresh", "project.overlap_changed")); err != nil {
		t.Fatalf("SetLadoProjectOverlap fresh: %v", err)
	}
	expect("turned on", "fresh", true)
}

// Run without plan review is on for every project until it turns it off: a
// project without a row reads on, every row a write creates stores 1, and a
// project that turned it off keeps it off through the other writes. A row
// created by turning it off has LADO mode and the overlap wait off (#101).
func TestLadoRunDirectlyDefaultsOnAndStaysPerProject(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	event := func(project, action string) domain.LadoAuditEvent {
		return domain.LadoAuditEvent{At: at, Actor: "api", Action: action, SubjectKind: "project", SubjectID: project}
	}
	s, raw := openLadoQueueStore(t)
	expect := func(step, project string, want bool) {
		t.Helper()
		settings, err := s.LadoProjectSettings(ctx, domain.ProjectID(project))
		if err != nil || settings.RunDirectly != want {
			t.Fatalf("%s: LadoProjectSettings %s = %+v (%v), want run directly %v", step, project, settings, err, want)
		}
	}

	expect("no row", "fresh", true)
	if _, err := s.SetLadoMode(ctx, "fresh", true, at); err != nil {
		t.Fatal(err)
	}
	expect("row from SetLadoMode", "fresh", true)

	if err := s.SetLadoProjectRunDirectly(ctx, "fresh", false, event("fresh", "project.run_directly_changed")); err != nil {
		t.Fatalf("SetLadoProjectRunDirectly: %v", err)
	}
	expect("turned off", "fresh", false)
	if _, err := s.SetLadoMode(ctx, "fresh", false, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLadoProjectOverlap(ctx, "fresh", true, nil, event("fresh", "project.overlap_changed")); err != nil {
		t.Fatal(err)
	}
	expect("kept off through other writes", "fresh", false)

	if err := s.SetLadoProjectRunDirectly(ctx, "other", false, event("other", "project.run_directly_changed")); err != nil {
		t.Fatal(err)
	}
	settings, err := s.LadoProjectSettings(ctx, "other")
	if err != nil || settings.RunDirectly || settings.LadoMode || settings.OverlapWait {
		t.Fatalf("row created by turning it off = %+v (%v), want run directly, LADO mode and overlap wait off", settings, err)
	}
	var events int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM lado_audit_events WHERE action = 'project.run_directly_changed'`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("run directly audit events = %d (%v), want 2", events, err)
	}
	if err := s.SetLadoProjectRunDirectly(ctx, "other", true, event("other", "project.run_directly_changed")); err != nil {
		t.Fatal(err)
	}
	expect("turned on again", "other", true)
}
