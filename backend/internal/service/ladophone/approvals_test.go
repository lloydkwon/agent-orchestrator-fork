package ladophone

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
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
