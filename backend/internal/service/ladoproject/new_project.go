package ladoproject

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ApplyNewProjectDefaults stores the settings a newly registered project
// starts with (#3): LADO mode on and domain.LadoNewProjectMaxConcurrent. A
// project registered with tracker intake on keeps LADO mode off, because
// LADO projects take issues by import (see setMode). A project that already
// has settings, such as one kept from an earlier registration of the same id,
// keeps them. Projects registered before #3 have no settings row and keep
// reading the old defaults, LADO mode off and
// domain.LadoDefaultProjectMaxConcurrent.
func (s *Service) ApplyNewProjectDefaults(ctx context.Context, project domain.ProjectRecord) error {
	projectID := domain.ProjectID(project.ID)
	on := !project.Config.TrackerIntake.Enabled
	created, err := s.deps.Store.CreateLadoNewProjectSettings(ctx, projectID, on, domain.LadoNewProjectMaxConcurrent, s.deps.Clock())
	if err != nil {
		return fmt.Errorf("apply LADO defaults to new project %s: %w", projectID, err)
	}
	if created && on {
		if fn := s.onLadoModeOn.Load(); fn != nil {
			(*fn)(projectID)
		}
	}
	return nil
}
