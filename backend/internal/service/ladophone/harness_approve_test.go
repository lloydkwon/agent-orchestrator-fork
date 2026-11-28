package ladophone

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladopolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

const (
	testProject  domain.ProjectID          = "proj"
	testPlanTask domain.LadoTaskID         = "task-plan"
	testRevision domain.LadoPlanRevisionID = "rev-2"
	testIntTask  domain.LadoTaskID         = "task-int"
	testSession  domain.SessionID          = "sess-int"
	testPRURL                              = "https://github.com/acme/app/pull/7"
)

var (
	testPlanSHA   = strings.Repeat("a", 64)
	testPolicySHA = strings.Repeat("b", 64)
	testHead      = strings.Repeat("c", 40)
)

// approvalHarness is phone approval turned on with one enrolled passkey
// (key), a fake LADO with a plan and an integration waiting, a notifier and
// a spy on the audit events phone approval appends.
type approvalHarness struct {
	*phoneHarness
	lado    *fakeLado
	alerts  *fakeNotifier
	audits  *auditSpy
	key     *softKey
	passkey domain.LadoPhonePasskey
}

func newApprovalHarness(t *testing.T) *approvalHarness {
	t.Helper()
	lado, alerts := newFakeLado(), &fakeNotifier{}
	var spy *auditSpy
	ph := newPhoneHarnessWith(t, func(o *Options) {
		spy = &auditSpy{Store: o.Store}
		o.Store = spy
		o.Projects, o.Tasks, o.Rules, o.Executions, o.Integrations, o.Notifier = lado, lado, lado, lado, lado, alerts
	})
	h := &approvalHarness{phoneHarness: ph, lado: lado, alerts: alerts, audits: spy}
	h.enable()
	h.key = newSoftKey(t)
	f := h.pairPhone(h.key)
	passkey, err := h.s.ConfirmEnroll(context.Background(), f.Code, h.enrollProof("enroll-proof-approval", h.key.credentialSHA256()))
	if err != nil {
		t.Fatalf("ConfirmEnroll: %v", err)
	}
	h.passkey = passkey
	h.seedPlan(testPlanSHA, true)
	h.seedIntegration(testSession, testIntTask, testPRURL, testHead, 7, h.cleanItems()...)
	return h
}

// seedPlan puts the planned task's revision rev-2 with sha and the rules.
func (h *approvalHarness) seedPlan(sha string, latest bool) {
	rev := domain.LadoPlanRevision{ID: testRevision, TaskID: testPlanTask, Number: 2, SHA256: sha}
	task := domain.LadoTask{
		ID: testPlanTask, ProjectID: testProject, IssueRef: "acme/app#12",
		Issue: domain.LadoObservedIssue{Title: "로그인 화면을 더한다"}, Plan: domain.LadoTaskPlan{Latest: rev},
	}
	h.lado.set(func(f *fakeLado) {
		f.projects = []domain.ProjectID{testProject}
		f.tasks[testProject] = replaceTask(f.tasks[testProject], task)
		f.plans[testRevision] = ladotask.PlanView{Task: task, Revision: rev, Content: "# 계획\n로그인을 더한다.\n", Latest: latest}
		f.rules[testPlanTask] = ladopolicy.EffectiveView{Resolution: ladopolicy.Resolution{
			Entries: []ladopolicy.Entry{{On: true}, {On: true}, {On: false}},
			Ceiling: domain.PermissionModeAuto,
			SHA256:  testPolicySHA,
		}}
	})
}

// seedIntegration puts an execution with one open PR whose head verifies to
// items, and its task waiting for the integration approval.
func (h *approvalHarness) seedIntegration(session domain.SessionID, taskID domain.LadoTaskID, prURL, head string, number int, items ...domain.LadoVerifyItem) {
	record := verifiedRecord(h.t, session, prURL, head, items...)
	outcome, _ := record.Verification.Overall()
	exec := domain.LadoExecution{
		SessionID: session, TaskID: taskID,
		PR:           domain.LadoExecutionPR{URL: prURL, Number: number, HeadSHA: head, TargetBranch: "main", Title: "Fix the bug"},
		Verification: domain.LadoExecutionVerification{HeadSHA: head, Outcome: outcome},
	}
	task := domain.LadoTask{
		ID: taskID, ProjectID: testProject, IssueRef: "acme/app#13",
		Issue: domain.LadoObservedIssue{Title: "버그를 고친다"}, Execution: exec,
	}
	h.lado.set(func(f *fakeLado) {
		f.projects = []domain.ProjectID{testProject}
		f.tasks[testProject] = replaceTask(f.tasks[testProject], task)
		f.execs[session] = ladoqueue.ExecutionView{Execution: exec, Task: task}
		f.records[session] = []ladoverify.ApprovePRResult{record}
	})
}

func replaceTask(tasks []domain.LadoTask, task domain.LadoTask) []domain.LadoTask {
	for i := range tasks {
		if tasks[i].ID == task.ID {
			tasks[i] = task
			return tasks
		}
	}
	return append(tasks, task)
}

// cleanItems verify a head that only needs its paths outside the plan
// confirmed.
func (h *approvalHarness) cleanItems() []domain.LadoVerifyItem {
	return []domain.LadoVerifyItem{
		mustItem(h.t, domain.LadoVerifyRequiredChecks, domain.LadoVerifyPass, "", nil),
		mustItem(h.t, domain.LadoVerifyPaths, domain.LadoVerifyConfirm, domain.LadoCodeOutsidePlan,
			domain.LadoPathsDetails{OutsidePlan: []string{"docs/notes.md"}, OutsideCount: 1}),
		mustItem(h.t, domain.LadoVerifyProtection, domain.LadoVerifyPass, "", nil),
	}
}

// dbItem is a db_changes item naming one pre-merge SQL file.
func dbItem(t *testing.T) domain.LadoVerifyItem {
	return mustItem(t, domain.LadoVerifyDBChanges, domain.LadoVerifyPass, "", domain.LadoDBChangesDetails{
		Repo:  "acme/app",
		Files: []domain.LadoDBChangeFileRef{{Path: "db/0001_users.sql", BlobSHA: strings.Repeat("d", 40), Phase: domain.LadoDBChangePreMerge}},
	})
}

func mustItem(t *testing.T, kind domain.LadoVerifyKind, outcome domain.LadoVerifyOutcome, code string, details any) domain.LadoVerifyItem {
	t.Helper()
	item, err := domain.NewLadoVerifyItem(kind, outcome, code, details)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func verifiedRecord(t *testing.T, session domain.SessionID, prURL, head string, items ...domain.LadoVerifyItem) ladoverify.ApprovePRResult {
	t.Helper()
	v := domain.LadoVerification{ExecutionSessionID: session, HeadSHA: head, PRURL: prURL, Items: items}
	sum, err := v.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	return ladoverify.ApprovePRResult{PRURL: prURL, Verification: v, SHA256: sum, Confirm: v.ConfirmCodes()}
}

// send makes one request as tailscale serve delivers it from the owner's
// phone, with the session cookie when one is given.
func (h *phoneHarness) send(method, path string, body any, cookie string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := phoneRequest(method, path, func(r *http.Request) {
		r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
	})
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		r.Body = io.NopCloser(strings.NewReader(string(data)))
		r.ContentLength = int64(len(data))
	}
	w := httptest.NewRecorder()
	h.s.phoneHandler(testGateConfig()).ServeHTTP(w, r)
	return w
}

func decodeInto[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %d %q: %v", w.Code, w.Body.String(), err)
	}
	return v
}

// assertionView is a begin answer of a login or an approval.
type assertionView struct {
	Result        string `json:"result"`
	Ceremony      string `json:"ceremony"`
	BindingSHA256 string `json:"bindingSha256"`
	Options       struct {
		PublicKey struct {
			Challenge        string `json:"challenge"`
			RPID             string `json:"rpId"`
			UserVerification string `json:"userVerification"`
			AllowCredentials []struct {
				ID string `json:"id"`
			} `json:"allowCredentials"`
		} `json:"publicKey"`
	} `json:"options"`
}

// asBegin is the answer as softKey.get reads it.
func (a assertionView) asBegin() beginView {
	var b beginView
	b.Result, b.Ceremony = a.Result, a.Ceremony
	b.Options.PublicKey.Challenge = a.Options.PublicKey.Challenge
	return b
}

type loginFinishView struct {
	Result      string `json:"result"`
	Fingerprint string `json:"fingerprint"`
}

type errorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type detailView struct {
	Kind        string          `json:"kind"`
	Title       string          `json:"title"`
	Lines       []string        `json:"lines"`
	Next        string          `json:"next"`
	AgentText   string          `json:"agentText"`
	Approvable  bool            `json:"approvable"`
	DesktopOnly bool            `json:"desktopOnly"`
	Reason      string          `json:"reason"`
	Binding     json.RawMessage `json:"binding"`
}

type listView struct {
	Items []struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Title   string `json:"title"`
		Project string `json:"project"`
		Issue   string `json:"issue"`
		Line    string `json:"line"`
	} `json:"items"`
}

func (h *approvalHarness) loginBegin() assertionView {
	h.t.Helper()
	w := h.send(http.MethodPost, "/login/begin", map[string]any{}, "")
	if w.Code != http.StatusOK {
		h.t.Fatalf("/login/begin: %d %s", w.Code, w.Body)
	}
	return decodeInto[assertionView](h.t, w)
}

// loginFinish answers the login ceremony and returns the result and the
// session cookie it set, if any.
func (h *approvalHarness) loginFinish(ceremony string, credential any) (string, *http.Cookie) {
	h.t.Helper()
	w := h.send(http.MethodPost, "/login/finish", map[string]any{"ceremony": ceremony, "credential": credential}, "")
	if w.Code != http.StatusOK {
		h.t.Fatalf("/login/finish: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return decodeInto[loginFinishView](h.t, w).Result, c
		}
	}
	return decodeInto[loginFinishView](h.t, w).Result, nil
}

// login logs the phone in with the enrolled passkey and returns the cookie.
func (h *approvalHarness) login() string {
	h.t.Helper()
	b := h.loginBegin()
	if b.Result != "ok" {
		h.t.Fatalf("login begin = %q\n%s", b.Result, h.logs)
	}
	result, cookie := h.loginFinish(b.Ceremony, h.key.get(h.t, b.asBegin(), iPhoneFlags))
	if result != "ok" || cookie == nil {
		h.t.Fatalf("login finish = %q\n%s", result, h.logs)
	}
	return cookie.Value
}

func (h *approvalHarness) detail(cookie, kind, id string) detailView {
	h.t.Helper()
	w := h.send(http.MethodGet, "/api/approvals/"+kind+"/"+id, nil, cookie)
	if w.Code != http.StatusOK {
		h.t.Fatalf("detail %s %s: %d %s\n%s", kind, id, w.Code, w.Body, h.logs)
	}
	return decodeInto[detailView](h.t, w)
}

func (h *approvalHarness) approveBegin(cookie, kind string, binding any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.send(http.MethodPost, "/api/approve/begin", map[string]any{"kind": kind, "binding": binding}, cookie)
}

// beginOK begins an approval of what the detail bound and returns the
// ceremony.
func (h *approvalHarness) beginOK(cookie string, d detailView) assertionView {
	h.t.Helper()
	w := h.approveBegin(cookie, d.Kind, d.Binding)
	if w.Code != http.StatusOK {
		h.t.Fatalf("approve begin: %d %s\n%s", w.Code, w.Body, h.logs)
	}
	b := decodeInto[assertionView](h.t, w)
	if b.Result != "ok" || b.Ceremony == "" {
		h.t.Fatalf("approve begin = %+v", b)
	}
	return b
}

func (h *approvalHarness) approveFinish(cookie, ceremony string, credential any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.send(http.MethodPost, "/api/approve/finish", map[string]any{"ceremony": ceremony, "credential": credential}, cookie)
}

func wantError(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("%s: status %d %s, want %d %s", what, w.Code, w.Body, status, code)
	}
	if code == "" {
		return
	}
	if got := decodeInto[errorView](t, w); got.Code != code {
		t.Fatalf("%s: code %q (%s), want %q", what, got.Code, got.Message, code)
	}
}
