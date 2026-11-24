package ladophone

// What the phone reads: the approvals waiting in LADO-mode projects and one
// of them in detail, with the binding an approval of it would sign. Each
// read needs a session; none of them approves anything.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladopolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

// approvalItem is one approval waiting, as the phone's list shows it.
type approvalItem struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Issue   string `json:"issue"`
	Line    string `json:"line"`
}

type approvalList struct {
	Items []approvalItem `json:"items"`
}

// approvalDetail is one approval as the phone shows it before approving:
// what Go made of the facts, the text an agent wrote kept apart, whether
// the phone may approve it and the binding an approval signs.
type approvalDetail struct {
	Kind  string   `json:"kind"`
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	Next  string   `json:"next,omitempty"`
	// AgentText is text an agent wrote (the plan), shown folded apart.
	AgentText  string `json:"agentText,omitempty"`
	Approvable bool   `json:"approvable"`
	// DesktopOnly says only the desktop app may approve it.
	DesktopOnly bool   `json:"desktopOnly,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Binding     any    `json:"binding,omitempty"`
}

// apiError is a refusal of an /api route.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writePhoneError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Code: code, Message: message})
}

// writeServiceError answers a LADO service's error with its status and code.
func (s *Service) writeServiceError(w http.ResponseWriter, what string, err error) {
	var e *apierr.Error
	if !errors.As(err, &e) {
		s.log.Error("lado phone: "+what, "error", err)
		writePhoneError(w, http.StatusInternalServerError, "LADO_PHONE_FAILED", "LADO could not do this.")
		return
	}
	status := map[apierr.Kind]int{
		apierr.KindInvalid: http.StatusBadRequest, apierr.KindNotFound: http.StatusNotFound,
		apierr.KindConflict: http.StatusConflict, apierr.KindForbidden: http.StatusForbidden,
		apierr.KindTooManyRequests: http.StatusTooManyRequests, apierr.KindNotImplemented: http.StatusNotImplemented,
		apierr.KindUnavailable: http.StatusServiceUnavailable,
	}[e.Kind]
	if status == 0 {
		status = http.StatusInternalServerError
		s.log.Error("lado phone: "+what, "code", e.Code, "error", err)
	}
	writePhoneError(w, status, e.Code, e.Message)
}

func loginRequired(w http.ResponseWriter) {
	writePhoneError(w, http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED", "Log in with the phone's passkey first.")
}

// readSession reports whether the request may read, with its session's
// key: its session lives, phone approval is on and the session's passkey
// can still log in. Otherwise it answers 401 and ends the session.
func (s *Service) readSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	sess, ok := s.sessions.use(sessionToken(r), r.Header.Get(headerLogin), s.now())
	if !ok {
		loginRequired(w)
		return "", false
	}
	settings, err := s.store.LadoPhoneSettings(r.Context())
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		writePhoneError(w, http.StatusInternalServerError, "LADO_PHONE_FAILED", "LADO could not read its settings.")
		return "", false
	}
	if _, active := s.activePasskey(r.Context(), settings, sess.credentialID); !settings.Enabled || !active {
		s.sessions.dropPasskey(sess.passkeyID)
		loginRequired(w)
		return "", false
	}
	return sess.key, true
}

// activePasskey returns the enrolled passkey of a credential when it can
// still sign: enrolled for the relying party, not revoked and never
// suspected of being copied.
func (s *Service) activePasskey(ctx context.Context, settings domain.LadoPhoneSettings, credentialID []byte) (domain.LadoPhonePasskey, bool) {
	pk, found, err := s.store.LadoPhonePasskeyByCredentialID(ctx, credentialID)
	if err != nil {
		s.log.Error("lado phone: read a passkey", "error", err)
		return domain.LadoPhonePasskey{}, false
	}
	if !found || pk.Revoked() || pk.CloneWarning || pk.RPID != settings.RPID {
		return domain.LadoPhonePasskey{}, false
	}
	return pk, true
}

func (s *Service) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.readSession(w, r); !ok {
		return
	}
	items, err := s.pendingApprovals(r.Context())
	if err != nil {
		s.writeServiceError(w, "list approvals", err)
		return
	}
	writePhoneJSON(w, approvalList{Items: items})
}

// pendingApprovals lists the plans and integrations waiting for approval in
// the LADO-mode projects: a task in a state planWaits names whose latest
// revision is valid, with a line saying why it waits again when it was
// approved before, and a task whose run's PRs are verified and wait for the
// integration approval.
func (s *Service) pendingApprovals(ctx context.Context) ([]approvalItem, error) {
	items := []approvalItem{}
	if s.projects == nil || s.tasks == nil {
		return items, nil
	}
	projects, err := s.projects.ListLadoModeProjectIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list LADO projects: %w", err)
	}
	now := s.now()
	for _, project := range projects {
		tasks, err := s.tasks.List(ctx, project, false)
		if err != nil {
			s.log.Warn("lado phone: list a project's tasks", "project", project, "error", err)
			continue
		}
		for _, task := range tasks {
			item := approvalItem{Title: oneLine(task.Issue.Title), Project: string(project), Issue: oneLine(string(task.IssueRef))}
			switch state := task.StateAt(now); {
			case planWaits(state):
				if !task.Plan.Latest.Valid() {
					continue
				}
				item.Kind, item.ID = kindPlan, string(task.Plan.Latest.ID)
				item.Line = fmt.Sprintf("계획 리비전 %d", task.Plan.Latest.Number)
				if why := planStateLine(task, state); why != "" {
					item.Line += " · " + why
				}
			case state == domain.LadoTaskIntegrationPending:
				open := openPRs(task.Execution)
				if len(open) == 0 {
					continue
				}
				item.Kind, item.ID = kindIntegration, string(task.Execution.SessionID)
				item.Line = fmt.Sprintf("통합 PR #%d → %s", open[0].PR.Number, excerpt(open[0].PR.TargetBranch, maxExampleChars))
				if len(open) > 1 {
					item.Line = fmt.Sprintf("통합 PR %d개", len(open))
				}
			default:
				continue
			}
			items = append(items, item)
		}
	}
	return items, nil
}

// openPRs are an execution's tracked PRs that are open, in merge order.
func openPRs(exec domain.LadoExecution) []domain.LadoExecutionTrackedPR {
	return slices.DeleteFunc(exec.TrackedPRs(), func(pr domain.LadoExecutionTrackedPR) bool {
		return pr.PR.URL == "" || pr.PR.Merged || pr.PR.Closed
	})
}

func approvalsUnavailable() error {
	return apierr.Unavailable("LADO_PHONE_APPROVALS_UNAVAILABLE", "LADO's approvals are not connected to phone approval.")
}

// ruleFacts are what the desktop's confirmation says about a plan's rules.
type ruleFacts struct {
	sha256   string
	on       int
	ceiling  domain.PermissionMode
	blocking int
}

// planFacts are a plan revision as it is now and the binding an approval of
// it would sign.
type planFacts struct {
	view       ladotask.PlanView
	rules      ruleFacts
	binding    planBinding
	approvable bool
	reason     string
	// stateLine says why the plan waits again after an approval.
	stateLine string
}

func (s *Service) planFacts(ctx context.Context, id domain.LadoPlanRevisionID) (planFacts, error) {
	if s.tasks == nil || s.rules == nil {
		return planFacts{}, approvalsUnavailable()
	}
	view, err := s.tasks.Plan(ctx, id)
	if err != nil {
		return planFacts{}, err
	}
	eff, err := s.rules.Effective(ctx, ladopolicy.EffectiveInput{TaskID: view.Task.ID})
	if err != nil {
		return planFacts{}, err
	}
	rules := ruleFacts{sha256: eff.SHA256, ceiling: eff.Ceiling}
	for _, e := range eff.Entries {
		if e.On {
			rules.on++
		}
	}
	for _, p := range eff.Problems {
		if p.Severity == domain.LadoSeverityBlocking {
			rules.blocking++
		}
	}
	f := planFacts{
		view: view, rules: rules,
		binding: planBinding{RevisionID: string(view.Revision.ID), SHA256: view.Revision.SHA256, PolicySHA256: eff.SHA256},
	}
	f.approvable, f.reason = s.planApprovable(view, rules)
	f.stateLine = planStateLine(view.Task, view.Task.StateAt(s.now()))
	return f, nil
}

// planWaits reports whether a task in state waits for its plan's approval,
// as the desktop's plan lanes show it: planned, or approved before and
// needing review, blocked by its rules or failed. The phone lists these.
func planWaits(state domain.LadoTaskState) bool {
	switch state {
	case domain.LadoTaskPlanned, domain.LadoTaskReviewRequired, domain.LadoTaskBlocked, domain.LadoTaskFailed:
		return true
	}
	return false
}

// planStateLine says why a task's plan waits again after an approval, empty
// for a plan that waits for its first one.
func planStateLine(task domain.LadoTask, state domain.LadoTaskState) string {
	switch state {
	case domain.LadoTaskReviewRequired:
		switch task.StateReason() {
		case domain.LadoReviewRulesChanged:
			return "규칙이 바뀌어 다시 승인 필요"
		case domain.LadoReviewIssueEdited:
			return "이슈가 바뀌어 다시 승인 필요"
		}
		return "다시 승인 필요"
	case domain.LadoTaskBlocked:
		return "막는 규칙 있음"
	case domain.LadoTaskFailed:
		return "실패 — 다시 승인 가능"
	}
	return ""
}

// planApprovable is the phone's own check before it asks for a signature;
// the task service checks again when it approves. As on the desktop, an
// approved plan is approved again when it needs review or its run failed;
// a blocked one is not, since approving it again signs the same rules.
func (s *Service) planApprovable(view ladotask.PlanView, rules ruleFacts) (bool, string) {
	state := view.Task.StateAt(s.now())
	switch {
	case !view.Latest:
		return false, "더 새 계획이 있습니다. 최신 계획을 열어 다시 보세요."
	case !view.Revision.Valid():
		return false, "이 계획에는 문제가 있어 승인할 수 없습니다. 먼저 고쳐 달라고 하세요."
	case !planWaits(state):
		if view.Approved {
			return false, "이미 승인한 계획입니다."
		}
		return false, "지금은 이 작업의 계획을 승인할 수 없습니다 (상태: " + string(state) + ")."
	case view.Approved && state == domain.LadoTaskBlocked:
		return false, "막는 규칙이 있어 다시 승인해도 같은 규칙에 서명합니다. 데스크톱에서 규칙이나 계획을 고친 뒤 승인하세요."
	case view.Approved && state == domain.LadoTaskPlanned:
		return false, "이미 승인한 계획입니다."
	case rules.sha256 == "":
		return false, "LADO 가 이 작업의 규칙을 읽지 못해 승인할 수 없습니다."
	}
	return true, ""
}

func (f planFacts) detail() approvalDetail {
	sum := planSummary(f.view, f.rules)
	if f.stateLine != "" {
		sum.Lines = append([]string{f.stateLine}, sum.Lines...)
	}
	return approvalDetail{
		Kind: kindPlan, ID: f.binding.RevisionID, Title: sum.Title, Lines: sum.Lines, AgentText: sum.AgentText,
		Approvable: f.approvable, Reason: f.reason, Binding: f.binding,
	}
}

func (s *Service) handlePlanDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.readSession(w, r); !ok {
		return
	}
	f, err := s.planFacts(r.Context(), domain.LadoPlanRevisionID(r.PathValue("id")))
	if err != nil {
		s.writeServiceError(w, "read a plan", err)
		return
	}
	writePhoneJSON(w, f.detail())
}

// integrationFacts are an execution's open PRs, the dry run of approving
// them, and the binding an approval of it would sign.
type integrationFacts struct {
	view        ladoqueue.ExecutionView
	prs         []domain.LadoExecutionTrackedPR
	results     []ladoverify.ApprovePRResult
	binding     integrationBinding
	approvable  bool
	desktopOnly bool
	reason      string
}

// integrationFacts reads the execution and, when its open PRs are verified
// and not all approved, runs one dry run of approving them all, unless the
// phone's session (phone, its key) ran too many lately (slowDown).
func (s *Service) integrationFacts(ctx context.Context, phone string, session domain.SessionID) (integrationFacts, error) {
	if s.executions == nil || s.integrations == nil {
		return integrationFacts{}, approvalsUnavailable()
	}
	view, err := s.executions.Execution(ctx, session)
	if err != nil {
		return integrationFacts{}, err
	}
	f := integrationFacts{view: view, prs: openPRs(view.Execution), binding: integrationBinding{SessionID: string(session), PRs: []integrationPRBinding{}}}
	switch {
	case len(f.prs) == 0:
		f.reason = "이 실행에는 통합할 열린 PR 이 없습니다."
		return f, nil
	case slices.ContainsFunc(f.prs, func(pr domain.LadoExecutionTrackedPR) bool { return pr.PR.HeadSHA == "" }):
		f.reason = "LADO 가 아직 PR 의 head 를 검증하지 않았습니다."
		return f, nil
	case !slices.ContainsFunc(f.prs, func(pr domain.LadoExecutionTrackedPR) bool { return !pr.Integration.HeadApproved }):
		f.reason = "이 head 의 통합은 이미 승인되었습니다."
		return f, nil
	}
	reqs := make([]ladoverify.ApprovePR, 0, len(f.prs))
	for _, pr := range f.prs {
		reqs = append(reqs, ladoverify.ApprovePR{PRURL: pr.PR.URL, HeadSHA: strings.ToLower(pr.PR.HeadSHA)})
	}
	if !s.dryRuns.allow(phone, s.now()) {
		return integrationFacts{}, slowDown()
	}
	dry, err := s.integrations.ApproveIntegration(ctx, ladoverify.ApproveInput{SessionID: session, PRs: reqs, DryRun: true})
	if err != nil {
		return integrationFacts{}, err
	}
	if len(dry.PRs) != len(f.prs) {
		s.log.Error("lado phone: a dry run answered other PRs than it was asked", "session", session, "asked", len(f.prs), "answered", len(dry.PRs))
		return integrationFacts{}, apierr.Internal("LADO_PHONE_FAILED", "LADO's dry run of this approval could not be read.")
	}
	f.results = dry.PRs
	notReady := false
	for _, result := range f.results {
		confirm := slices.Clone(result.Confirm)
		if confirm == nil {
			confirm = []string{}
		}
		f.binding.PRs = append(f.binding.PRs, integrationPRBinding{
			URL: result.PRURL, Head: strings.ToLower(result.Verification.HeadSHA), VerificationSHA256: result.SHA256, Confirm: confirm,
		})
		if outcome, _ := result.Verification.Overall(); outcome == domain.LadoVerifyWait || outcome == domain.LadoVerifyFail {
			notReady = true
		}
		if hasDBSQL(result.Verification) {
			f.desktopOnly = true
		}
	}
	switch {
	case f.desktopOnly:
		f.reason = "운영 DB SQL 이 있어 데스크톱에서만 승인합니다."
	case slices.ContainsFunc(f.binding.PRs, func(pr integrationPRBinding) bool { return !validPRURL(pr.URL) }):
		f.reason = "LADO 가 PR 주소를 읽지 못해 폰에서 승인할 수 없습니다. 데스크톱에서 승인하세요."
	case notReady:
		f.reason = "LADO 가 아직 검증 중이거나 검사가 실패했습니다. 통과한 뒤 승인하세요."
	default:
		f.approvable = true
	}
	return f, nil
}

// hasDBSQL reports whether a head's record carries production database SQL:
// files to apply, or applied files the head changes. An unreadable
// db_changes item counts as carrying it.
func hasDBSQL(v domain.LadoVerification) bool {
	d, ok, err := v.DBChanges()
	if err != nil {
		return true
	}
	return ok && (len(d.Files) > 0 || len(d.Modified) > 0)
}

func (f integrationFacts) detail() approvalDetail {
	sum := integrationSummary(f.view, f.prs, f.results)
	return approvalDetail{
		Kind: kindIntegration, ID: f.binding.SessionID, Title: sum.Title, Lines: sum.Lines, Next: sum.Next,
		Approvable: f.approvable, DesktopOnly: f.desktopOnly, Reason: f.reason, Binding: f.binding,
	}
}

func (s *Service) handleIntegrationDetail(w http.ResponseWriter, r *http.Request) {
	key, ok := s.readSession(w, r)
	if !ok {
		return
	}
	f, err := s.integrationFacts(r.Context(), key, domain.SessionID(r.PathValue("id")))
	if err != nil {
		s.writeServiceError(w, "read an integration", err)
		return
	}
	writePhoneJSON(w, f.detail())
}
