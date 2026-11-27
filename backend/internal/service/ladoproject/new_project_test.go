package ladoproject_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A newly registered project starts with LADO mode on and the new project
// cap (#3), and the verification loop is told as when the mode is turned on.
func TestApplyNewProjectDefaultsTurnsLadoModeOn(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	svc := newService(store)
	var told []domain.ProjectID
	svc.SetOnLadoModeOn(func(id domain.ProjectID) { told = append(told, id) })

	if err := svc.ApplyNewProjectDefaults(ctx, domain.ProjectRecord{ID: "mer"}); err != nil {
		t.Fatalf("ApplyNewProjectDefaults: %v", err)
	}
	got, err := svc.Settings(ctx, "mer")
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if !got.LadoMode || got.MaxConcurrent != domain.LadoNewProjectMaxConcurrent || !got.UpdatedAt.Equal(now) {
		t.Fatalf("settings = %+v, want LADO mode on and cap %d at %s", got, domain.LadoNewProjectMaxConcurrent, now)
	}
	if !slices.Equal(told, []domain.ProjectID{"mer"}) {
		t.Fatalf("told %v, want mer once", told)
	}
}

// A project registered with tracker intake on keeps LADO mode off, the way
// turning it on is refused, and gets the new project cap.
func TestApplyNewProjectDefaultsKeepsTrackerIntakeProjectsOff(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	svc := newService(store)
	var told []domain.ProjectID
	svc.SetOnLadoModeOn(func(id domain.ProjectID) { told = append(told, id) })
	intake := domain.ProjectRecord{ID: "intake", Config: domain.ProjectConfig{TrackerIntake: domain.TrackerIntakeConfig{Enabled: true}}}

	if err := svc.ApplyNewProjectDefaults(ctx, intake); err != nil {
		t.Fatalf("ApplyNewProjectDefaults: %v", err)
	}
	got, err := svc.Settings(ctx, "intake")
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if got.LadoMode || got.MaxConcurrent != domain.LadoNewProjectMaxConcurrent {
		t.Fatalf("settings = %+v, want LADO mode off and cap %d", got, domain.LadoNewProjectMaxConcurrent)
	}
	if len(told) != 0 {
		t.Fatalf("told %v, want nobody for a project left off", told)
	}
}

// Settings a project already has stay as they are, and the verification loop
// is not told.
func TestApplyNewProjectDefaultsKeepsExistingSettings(t *testing.T) {
	ctx := context.Background()
	existing := domain.LadoProjectSettings{ProjectID: "mer", LadoMode: false, MaxConcurrent: 5, CheckCommands: []domain.LadoCheckCommand{}, CheckTimeoutMinutes: domain.LadoCheckTimeoutDefault}
	store := &fakeStore{settings: map[domain.ProjectID]domain.LadoProjectSettings{"mer": existing}}
	svc := newService(store)
	var told []domain.ProjectID
	svc.SetOnLadoModeOn(func(id domain.ProjectID) { told = append(told, id) })

	if err := svc.ApplyNewProjectDefaults(ctx, domain.ProjectRecord{ID: "mer"}); err != nil {
		t.Fatalf("ApplyNewProjectDefaults: %v", err)
	}
	got, err := svc.Settings(ctx, "mer")
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if got.LadoMode || got.MaxConcurrent != 5 {
		t.Fatalf("settings = %+v, want the existing mode off and cap 5", got)
	}
	if len(told) != 0 {
		t.Fatalf("told %v, want nobody for settings that did not change", told)
	}
}

// A store failure comes back to the caller, and nobody is told.
func TestApplyNewProjectDefaultsReportsStoreFailures(t *testing.T) {
	failure := errors.New("disk full")
	store := &fakeStore{writeErr: failure}
	svc := newService(store)
	var told []domain.ProjectID
	svc.SetOnLadoModeOn(func(id domain.ProjectID) { told = append(told, id) })

	err := svc.ApplyNewProjectDefaults(context.Background(), domain.ProjectRecord{ID: "mer"})
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}
	if len(told) != 0 {
		t.Fatalf("told %v, want nobody after a failure", told)
	}
}
