package ladophone

// What phone approval reads and approves through, injected by the daemon.
// Each interface is the slice of one LADO service the phone needs, with that
// service's own method signatures, so the daemon passes the service itself.
// The phone never approves anything those services would not approve for
// the desktop app: it only hands them a proof a passkey made.

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladopolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

// LadoProjects lists the projects in LADO mode (*store.Store).
type LadoProjects interface {
	ListLadoModeProjectIDs(ctx context.Context) ([]domain.ProjectID, error)
}

// Tasks reads a project's tasks and a plan revision, and approves a plan
// (*ladotask.Service).
type Tasks interface {
	List(ctx context.Context, projectID domain.ProjectID, includeCancelled bool) ([]domain.LadoTask, error)
	Plan(ctx context.Context, revisionID domain.LadoPlanRevisionID) (ladotask.PlanView, error)
	ApprovePlan(ctx context.Context, revisionID domain.LadoPlanRevisionID, sha256, policySHA256 string, proof domain.LadoPresenceProof) (domain.LadoPlanApproval, error)
}

// Rules resolves the rules a task's approval would fix, as
// GET /api/v1/lado/effective?taskId= does (*ladopolicy.Service).
type Rules interface {
	Effective(ctx context.Context, in ladopolicy.EffectiveInput) (ladopolicy.EffectiveView, error)
}

// Executions reads an execution with its task (*ladoqueue.Service).
type Executions interface {
	Execution(ctx context.Context, sessionID domain.SessionID) (ladoqueue.ExecutionView, error)
}

// Integrations approves an integration, or previews it with DryRun
// (*ladoverify.Service).
type Integrations interface {
	ApproveIntegration(ctx context.Context, in ladoverify.ApproveInput) (ladoverify.ApproveResult, error)
}

// Kinds of the alerts phone approval raises on the desktop.
const (
	// AlertSignatureFailed: an enrolled passkey's assertion did not verify.
	AlertSignatureFailed = "phone_passkey.signature_failed"
	// AlertCloneWarning: an enrolled passkey's sign count went backwards, so
	// the passkey may have been copied; it is refused from now on.
	AlertCloneWarning = "phone_passkey.clone_warning"
	// AlertLoginLocked: too many failed passkey checks; logins rest a while.
	AlertLoginLocked = "phone_approval.login_locked"
)

// Notifier raises an alert on the developer's desktop. kind is one of the
// Alert* constants and detail a short English sentence for the log or the
// notification's body. It must not block for long; a nil Notifier only logs.
type Notifier interface {
	NotifyPhoneAlert(ctx context.Context, kind, detail string)
}
