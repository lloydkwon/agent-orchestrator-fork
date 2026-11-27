package project

// LADO fork: a newly registered project starts with LADO's own settings, LADO
// mode on and three executions at once (#3, docs/LJY-PRD/TDD.md §4.2).

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LadoProjectDefaults stores the LADO settings a newly registered project
// starts with. ladoproject.Service implements it.
type LadoProjectDefaults interface {
	ApplyNewProjectDefaults(ctx context.Context, project domain.ProjectRecord) error
}

// applyLadoDefaults gives a project Add has just stored its LADO settings. The
// registration is committed, so the write outlives a request that ends now. A
// failure leaves the project registered with the settings a project without
// stored ones reads, LADO mode off, and is logged.
func (m *Service) applyLadoDefaults(ctx context.Context, row domain.ProjectRecord) {
	if m.ladoDefaults == nil {
		return
	}
	if err := m.ladoDefaults.ApplyNewProjectDefaults(context.WithoutCancel(ctx), row); err != nil {
		m.logger.Warn("project: could not apply the LADO defaults to the new project", "projectId", row.ID, "error", err)
	}
}
