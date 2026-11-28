package ladophone

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladopolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

// fakeLado stands in for LADO's task, rule, queue and verification services
// and the store's LADO mode list. Its approvals check what the real ones
// check that the phone's tests depend on: the plan's hash and latest
// revision, the rules' hash, the integration's presence subject, record
// hashes and confirm codes, and Allow before anything is recorded.
type fakeLado struct {
	mu sync.Mutex

	projects []domain.ProjectID
	tasks    map[domain.ProjectID][]domain.LadoTask
	plans    map[domain.LadoPlanRevisionID]ladotask.PlanView
	rules    map[domain.LadoTaskID]ladopolicy.EffectiveView
	execs    map[domain.SessionID]ladoqueue.ExecutionView
	// records are what verifying each open PR's head gives now, by session,
	// in merge order.
	records map[domain.SessionID][]ladoverify.ApprovePRResult

	usedNonces   map[string]bool
	planCalls    []planCall
	planApproved []planCall
	dryRuns      []ladoverify.ApproveInput
	approveCalls []ladoverify.ApproveInput
	allowAsked   int
	integrated   []integrationApproved
}

type planCall struct {
	revision             domain.LadoPlanRevisionID
	sha256, policySHA256 string
	proof                domain.LadoPresenceProof
}

type integrationApproved struct {
	session domain.SessionID
	heads   []string
	proof   domain.LadoPresenceProof
}

func newFakeLado() *fakeLado {
	return &fakeLado{
		tasks:      map[domain.ProjectID][]domain.LadoTask{},
		plans:      map[domain.LadoPlanRevisionID]ladotask.PlanView{},
		rules:      map[domain.LadoTaskID]ladopolicy.EffectiveView{},
		execs:      map[domain.SessionID]ladoqueue.ExecutionView{},
		records:    map[domain.SessionID][]ladoverify.ApprovePRResult{},
		usedNonces: map[string]bool{},
	}
}

func (f *fakeLado) set(edit func(f *fakeLado)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f)
}

func (f *fakeLado) read(look func(f *fakeLado)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	look(f)
}

func (f *fakeLado) ListLadoModeProjectIDs(context.Context) ([]domain.ProjectID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.projects), nil
}

func (f *fakeLado) List(_ context.Context, projectID domain.ProjectID, _ bool) ([]domain.LadoTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tasks[projectID]), nil
}

func (f *fakeLado) Plan(_ context.Context, id domain.LadoPlanRevisionID) (ladotask.PlanView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	view, ok := f.plans[id]
	if !ok {
		return ladotask.PlanView{}, apierr.NotFound("LADO_PLAN_NOT_FOUND", "no such plan")
	}
	return view, nil
}

func (f *fakeLado) Effective(_ context.Context, in ladopolicy.EffectiveInput) (ladopolicy.EffectiveView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	view, ok := f.rules[in.TaskID]
	if !ok {
		return ladopolicy.EffectiveView{}, apierr.NotFound("LADO_TASK_NOT_FOUND", "no such task")
	}
	return view, nil
}

func (f *fakeLado) ApprovePlan(_ context.Context, id domain.LadoPlanRevisionID, sha256, policySHA256 string, proof domain.LadoPresenceProof) (domain.LadoPlanApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := planCall{revision: id, sha256: sha256, policySHA256: policySHA256, proof: proof}
	f.planCalls = append(f.planCalls, call)
	view, ok := f.plans[id]
	switch {
	case !ok:
		return domain.LadoPlanApproval{}, apierr.NotFound("LADO_PLAN_NOT_FOUND", "no such plan")
	case sha256 != view.Revision.SHA256:
		return domain.LadoPlanApproval{}, apierr.Conflict("LADO_PLAN_CHANGED", "This is not the plan you reviewed.", nil)
	case !view.Latest:
		return domain.LadoPlanApproval{}, apierr.Conflict("LADO_PLAN_SUPERSEDED", "A newer version of this plan exists.", nil)
	case policySHA256 != f.rules[view.Task.ID].SHA256:
		return domain.LadoPlanApproval{}, apierr.Conflict("LADO_RULES_CHANGED", "The rules changed.", nil)
	case f.usedNonces[proof.Nonce]:
		return domain.LadoPlanApproval{}, apierr.Conflict("LADO_PRESENCE_REPLAYED", "used", nil)
	}
	f.usedNonces[proof.Nonce] = true
	f.planApproved = append(f.planApproved, call)
	view.Approved = true
	f.plans[id] = view
	return domain.LadoPlanApproval{ID: "lado-approval-1", RevisionID: id, SHA256: sha256, ProofNonce: proof.Nonce}, nil
}

func (f *fakeLado) Execution(_ context.Context, id domain.SessionID) (ladoqueue.ExecutionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	view, ok := f.execs[id]
	if !ok {
		return ladoqueue.ExecutionView{}, apierr.NotFound("LADO_EXECUTION_NOT_FOUND", "This session is not a LADO execution")
	}
	return view, nil
}

// ApproveIntegration answers a dry run with the records of the PRs asked
// for, and approves as the real service does where the phone depends on it:
// the presence subject names the heads in merge order, then the hashes and
// codes must match, then Allow may refuse, and only then is it recorded.
func (f *fakeLado) ApproveIntegration(ctx context.Context, in ladoverify.ApproveInput) (ladoverify.ApproveResult, error) {
	f.mu.Lock()
	view, ok := f.execs[in.SessionID]
	records := slices.Clone(f.records[in.SessionID])
	if in.DryRun {
		f.dryRuns = append(f.dryRuns, in)
	} else {
		f.approveCalls = append(f.approveCalls, in)
	}
	f.mu.Unlock()
	if !ok {
		return ladoverify.ApproveResult{}, apierr.NotFound("LADO_EXECUTION_NOT_FOUND", "This session is not a LADO execution")
	}
	// In merge order, as the execution tracks them.
	reqs := slices.Clone(in.PRs)
	order := view.Execution.TrackedPRs()
	rank := func(url string) int {
		return slices.IndexFunc(order, func(pr domain.LadoExecutionTrackedPR) bool { return pr.PR.URL == url })
	}
	slices.SortStableFunc(reqs, func(a, b ladoverify.ApprovePR) int { return rank(a.PRURL) - rank(b.PRURL) })
	var proof domain.LadoPresenceProof
	if !in.DryRun {
		heads := make([]string, 0, len(reqs))
		for _, pr := range reqs {
			heads = append(heads, strings.ToLower(pr.HeadSHA))
		}
		if in.Presence == nil {
			return ladoverify.ApproveResult{}, apierr.Forbidden("LADO_PRESENCE_REQUIRED", "no presence")
		}
		var err error
		if proof, err = in.Presence(domain.LadoPresenceIntegrationApprove, string(in.SessionID)+"@"+strings.Join(heads, ",")); err != nil {
			return ladoverify.ApproveResult{}, err
		}
		f.mu.Lock()
		replayed := f.usedNonces[proof.Nonce]
		f.mu.Unlock()
		if replayed {
			return ladoverify.ApproveResult{}, apierr.Conflict("LADO_PRESENCE_REPLAYED", "used", nil)
		}
	}
	results := make([]ladoverify.ApprovePRResult, 0, len(reqs))
	for _, req := range reqs {
		at := slices.IndexFunc(records, func(r ladoverify.ApprovePRResult) bool { return r.PRURL == req.PRURL })
		if at < 0 || !strings.EqualFold(records[at].Verification.HeadSHA, req.HeadSHA) {
			return ladoverify.ApproveResult{}, apierr.Conflict("LADO_INTEGRATION_HEAD_CHANGED", "head changed", nil)
		}
		results = append(results, records[at])
	}
	if len(results) == 0 {
		return ladoverify.ApproveResult{}, apierr.Conflict("LADO_EXECUTION_NO_PR", "no PR", nil)
	}
	result := ladoverify.ApproveResult{Verification: results[0].Verification, SHA256: results[0].SHA256, Confirm: results[0].Confirm, PRs: results}
	if in.DryRun {
		return result, nil
	}
	for i, req := range reqs {
		if req.VerificationSHA256 != results[i].SHA256 {
			return ladoverify.ApproveResult{}, apierr.Conflict("LADO_INTEGRATION_VERIFICATION_CHANGED", "changed", nil)
		}
		if !slices.Equal(req.Acknowledge, results[i].Confirm) {
			return ladoverify.ApproveResult{}, apierr.Invalid("LADO_INTEGRATION_ACK_MISMATCH", "ack", nil)
		}
	}
	if in.Allow != nil {
		f.mu.Lock()
		f.allowAsked++
		f.mu.Unlock()
		if err := in.Allow(ctx, view.Execution, slices.Clone(results)); err != nil {
			return ladoverify.ApproveResult{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usedNonces[proof.Nonce] = true
	heads := make([]string, 0, len(reqs))
	for _, req := range reqs {
		heads = append(heads, req.HeadSHA)
	}
	f.integrated = append(f.integrated, integrationApproved{session: in.SessionID, heads: heads, proof: proof})
	for i := range results {
		results[i].Approval = &domain.LadoIntegrationApproval{ProofNonce: proof.Nonce}
	}
	result.PRs, result.Approval = results, results[0].Approval
	return result, nil
}

// approvalsMade counts the plan and integration approvals recorded.
func (f *fakeLado) approvalsMade() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.planApproved) + len(f.integrated)
}

// approveAttempts counts the calls that could have approved.
func (f *fakeLado) approveAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.planCalls) + len(f.approveCalls)
}

// fakeNotifier records the alerts raised.
type fakeNotifier struct {
	mu     sync.Mutex
	alerts []string
}

func (n *fakeNotifier) NotifyPhoneAlert(_ context.Context, kind, _ string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.alerts = append(n.alerts, kind)
}

func (n *fakeNotifier) kinds() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.alerts)
}

// auditSpy records the audit events phone approval appends itself.
type auditSpy struct {
	Store
	mu     sync.Mutex
	events []domain.LadoAuditEvent
}

func (a *auditSpy) AppendLadoAuditEvent(ctx context.Context, ev domain.LadoAuditEvent) (domain.LadoAuditEvent, error) {
	a.mu.Lock()
	a.events = append(a.events, ev)
	a.mu.Unlock()
	return a.Store.AppendLadoAuditEvent(ctx, ev)
}

func (a *auditSpy) last(t *testing.T, action string) domain.LadoAuditEvent {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.events) - 1; i >= 0; i-- {
		if a.events[i].Action == action {
			return a.events[i]
		}
	}
	t.Fatalf("no audit event %s among %d", action, len(a.events))
	return domain.LadoAuditEvent{}
}
