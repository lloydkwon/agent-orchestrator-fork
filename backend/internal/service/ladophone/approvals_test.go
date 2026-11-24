package ladophone

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladopolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
)

func TestApprovalsListShowsWhatWaits(t *testing.T) {
	h := newApprovalHarness(t)
	// A planned task whose latest revision has problems waits for nothing
	// the phone can approve; a project outside LADO mode is not read.
	h.lado.set(func(f *fakeLado) {
		bad := domain.LadoPlanRevision{ID: "rev-bad", TaskID: "task-bad", Number: 1, SHA256: testPlanSHA, Problems: []domain.LadoPlanProblem{{Code: "x"}}}
		f.tasks[testProject] = append(f.tasks[testProject], domain.LadoTask{ID: "task-bad", ProjectID: testProject, IssueRef: "acme/app#14",
			Issue: domain.LadoObservedIssue{Title: "bad"}, Plan: domain.LadoTaskPlan{Latest: bad}})
		f.tasks["other"] = []domain.LadoTask{{ID: "task-other", ProjectID: "other", Plan: domain.LadoTaskPlan{Latest: domain.LadoPlanRevision{ID: "rev-o"}}}}
	})
	cookie := h.login()
	w := h.send(http.MethodGet, "/api/approvals", nil, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	list := decodeInto[listView](t, w)
	if len(list.Items) != 2 {
		t.Fatalf("items = %+v", list.Items)
	}
	plan, integration := list.Items[0], list.Items[1]
	if plan.Kind != kindPlan || plan.ID != "rev-2" || plan.Title != "로그인 화면을 더한다" || plan.Project != "proj" || plan.Issue != "acme/app#12" || plan.Line != "계획 리비전 2" {
		t.Fatalf("plan item = %+v", plan)
	}
	if integration.Kind != kindIntegration || integration.ID != "sess-int" || integration.Issue != "acme/app#13" || integration.Line != "통합 PR #7 → main" {
		t.Fatalf("integration item = %+v", integration)
	}
}

func TestDetailsSayWhenThePhoneCannotApprove(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	for name, edit := range map[string]func(f *fakeLado){
		"superseded": func(f *fakeLado) { v := f.plans[testRevision]; v.Latest = false; f.plans[testRevision] = v },
		"invalid": func(f *fakeLado) {
			v := f.plans[testRevision]
			v.Revision.Problems = []domain.LadoPlanProblem{{Code: "LADO_PLAN_SECTION_MISSING"}}
			f.plans[testRevision] = v
		},
		"approved": func(f *fakeLado) { v := f.plans[testRevision]; v.Approved = true; f.plans[testRevision] = v },
		"running": func(f *fakeLado) {
			v := f.plans[testRevision]
			v.Task.Execution = domain.LadoExecution{SessionID: "sess-run", TaskID: testPlanTask}
			f.plans[testRevision] = v
		},
	} {
		h.seedPlan(testPlanSHA, true)
		h.lado.set(edit)
		d := h.detail(cookie, kindPlan, string(testRevision))
		if d.Approvable || d.Reason == "" {
			t.Errorf("%s: detail = %+v, want not approvable with a reason", name, d)
			continue
		}
		wantError(t, name+": begin", h.approveBegin(cookie, kindPlan, d.Binding), http.StatusConflict, "LADO_PHONE_NOT_APPROVABLE")
	}

	// An integration still verifying, approved already, or without a PR.
	waiting := mustItem(t, domain.LadoVerifyRequiredChecks, domain.LadoVerifyWait, domain.LadoCodeChecksPending, nil)
	h.seedIntegration(testSession, testIntTask, testPRURL, testHead, 7, waiting)
	d := h.detail(cookie, kindIntegration, string(testSession))
	if d.Approvable || d.DesktopOnly || !strings.Contains(d.Reason, "검증") {
		t.Fatalf("waiting integration = %+v", d)
	}
	wantError(t, "waiting integration begin", h.approveBegin(cookie, kindIntegration, d.Binding), http.StatusConflict, "LADO_PHONE_NOT_APPROVABLE")
	for name, edit := range map[string]func(e *domain.LadoExecution){
		"approved": func(e *domain.LadoExecution) { e.Integration.HeadApproved = true },
		"no head":  func(e *domain.LadoExecution) { e.PR.HeadSHA = "" },
		"no PR":    func(e *domain.LadoExecution) { e.PR = domain.LadoExecutionPR{} },
		"merged":   func(e *domain.LadoExecution) { e.PR.Merged = true },
	} {
		h.seedIntegration(testSession, testIntTask, testPRURL, testHead, 7, h.cleanItems()...)
		h.lado.set(func(f *fakeLado) {
			v := f.execs[testSession]
			edit(&v.Execution)
			f.execs[testSession] = ladoqueue.ExecutionView{Execution: v.Execution, Task: v.Task}
		})
		if d := h.detail(cookie, kindIntegration, string(testSession)); d.Approvable || d.Reason == "" {
			t.Errorf("%s: integration detail = %+v", name, d)
		}
	}
	noApprovals(t, h)
}

func TestApprovalsWithoutLADOServices(t *testing.T) {
	h := newApprovalHarness(t)
	h.s.projects, h.s.tasks, h.s.rules, h.s.executions, h.s.integrations = nil, nil, nil, nil, nil
	cookie := h.login()
	w := h.send(http.MethodGet, "/api/approvals", nil, cookie)
	if list := decodeInto[listView](t, w); w.Code != http.StatusOK || len(list.Items) != 0 {
		t.Fatalf("list without LADO = %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/approvals/plan/rev-2", "/api/approvals/integration/sess-int"} {
		wantError(t, path, h.send(http.MethodGet, path, nil, cookie), http.StatusServiceUnavailable, "LADO_PHONE_APPROVALS_UNAVAILABLE")
	}
	// An unknown revision or session answers as LADO does.
	h2 := newApprovalHarness(t)
	cookie = h2.login()
	wantError(t, "unknown plan", h2.send(http.MethodGet, "/api/approvals/plan/rev-9", nil, cookie), http.StatusNotFound, "LADO_PLAN_NOT_FOUND")
	wantError(t, "unknown run", h2.send(http.MethodGet, "/api/approvals/integration/sess-9", nil, cookie), http.StatusNotFound, "LADO_EXECUTION_NOT_FOUND")
	wantError(t, "a malformed id", h2.send(http.MethodGet, "/api/approvals/plan/..", nil, cookie), http.StatusNotFound, "")
}

func TestApproveRefusedWhileLocked(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	d := h.detail(cookie, kindPlan, string(testRevision))
	b := h.beginOK(cookie, d)
	stranger := newSoftKey(t)
	for i := 0; i < loginMaxFailures; i++ {
		lb := h.loginBegin()
		h.loginFinish(lb.Ceremony, stranger.get(t, lb.asBegin(), iPhoneFlags))
	}
	wantError(t, "finish while locked", h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)), http.StatusTooManyRequests, "LADO_PHONE_LOCKED")
	wantError(t, "begin while locked", h.approveBegin(cookie, kindPlan, d.Binding), http.StatusTooManyRequests, "LADO_PHONE_LOCKED")
	noApprovals(t, h)
}

func TestLoginClosedWhenPhoneApprovalOff(t *testing.T) {
	h := newApprovalHarness(t)
	b := h.loginBegin()
	if _, err := h.s.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res, c := h.loginFinish(b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)); res != "stale" || c != nil {
		t.Fatalf("a login begun before turning off = %q", res)
	}
	if b := h.loginBegin(); b.Result != "closed" {
		t.Fatalf("login begin while off = %q", b.Result)
	}
}

// seedApprovedPlan puts a task whose latest revision rev an approval covers,
// as edit leaves it, with its plan view and rules.
func (h *approvalHarness) seedApprovedPlan(id domain.LadoTaskID, rev domain.LadoPlanRevisionID, edit func(*domain.LadoTask)) {
	r := domain.LadoPlanRevision{ID: rev, TaskID: id, Number: 3, SHA256: testPlanSHA}
	task := domain.LadoTask{
		ID: id, ProjectID: testProject, IssueRef: domain.IssueID("acme/app#" + string(id)), Issue: domain.LadoObservedIssue{Title: string(id)},
		Plan: domain.LadoTaskPlan{Latest: r, Approval: domain.LadoPlanApproval{ID: domain.LadoPlanApprovalID("appr-" + id), RevisionID: rev, TaskID: id, SHA256: testPlanSHA}},
	}
	edit(&task)
	h.lado.set(func(f *fakeLado) {
		f.tasks[testProject] = replaceTask(f.tasks[testProject], task)
		f.plans[rev] = ladotask.PlanView{Task: task, Revision: r, Content: "# 계획\n", Latest: true, Approved: true}
		f.rules[id] = ladopolicy.EffectiveView{Resolution: ladopolicy.Resolution{Ceiling: domain.PermissionModeAuto, SHA256: testPolicySHA}}
	})
}

func TestApprovalsListShowsPlansToApproveAgain(t *testing.T) {
	h := newApprovalHarness(t)
	// The rules changed since the approval; its rules block the run; its run
	// failed. A plan approved and queued waits for nothing.
	h.seedApprovedPlan("task-rules", "rev-rules", func(task *domain.LadoTask) {
		task.Plan.Snapshot = domain.LadoPolicySnapshotRef{ID: "snap-1", SHA256: strings.Repeat("1", 64)}
		task.Plan.PolicySHA256 = strings.Repeat("2", 64)
	})
	h.seedApprovedPlan("task-blocked", "rev-blocked", func(task *domain.LadoTask) {
		task.Plan.Snapshot = domain.LadoPolicySnapshotRef{ID: "snap-2", SHA256: testPolicySHA, Blocking: true}
	})
	h.seedApprovedPlan("task-failed", "rev-failed", func(task *domain.LadoTask) {
		task.Execution = domain.LadoExecution{SessionID: "sess-failed", TaskID: "task-failed", PlanApprovalID: task.Plan.Approval.ID, SessionTerminated: true}
	})
	h.seedApprovedPlan("task-queued", "rev-queued", func(task *domain.LadoTask) {
		task.Queue = domain.LadoQueueEntry{ID: "q-1", TaskID: "task-queued", PlanApprovalID: task.Plan.Approval.ID}
	})
	cookie := h.login()
	list := decodeInto[listView](t, h.send(http.MethodGet, "/api/approvals", nil, cookie))
	lines := map[string]string{}
	for _, item := range list.Items {
		if item.Kind == kindPlan {
			lines[item.ID] = item.Line
		}
	}
	want := map[string]string{
		"rev-2":       "계획 리비전 2",
		"rev-rules":   "계획 리비전 3 · 규칙이 바뀌어 다시 승인 필요",
		"rev-blocked": "계획 리비전 3 · 막는 규칙 있음",
		"rev-failed":  "계획 리비전 3 · 실패 — 다시 승인 가능",
	}
	if !maps.Equal(lines, want) {
		t.Fatalf("plan lines = %v\nwant %v", lines, want)
	}

	// The detail agrees: changed rules and a failed run are approved again
	// from the phone, a blocked plan is not, and its summary says why.
	for id, approvable := range map[string]bool{"rev-rules": true, "rev-failed": true, "rev-blocked": false} {
		d := h.detail(cookie, kindPlan, id)
		if d.Approvable != approvable || len(d.Lines) == 0 || !strings.HasSuffix(want[id], d.Lines[0]) {
			t.Errorf("%s: detail = %+v", id, d)
		}
		if !approvable && d.Reason == "" {
			t.Errorf("%s: no reason", id)
		}
	}
	b := h.beginOK(cookie, h.detail(cookie, kindPlan, "rev-rules"))
	if w := h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)); w.Code != http.StatusOK {
		t.Fatalf("approve a plan whose rules changed: %d %s\n%s", w.Code, w.Body, h.logs)
	}
}
