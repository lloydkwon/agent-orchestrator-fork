package ladophone

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// planBindingJSON is the canonical binding of the seeded plan, written out
// so the format is pinned here.
func planBindingJSON(sha, policy string) string {
	return `{"kind":"plan","binding":{"revisionId":"rev-2","sha256":"` + sha + `","policySha256":"` + policy + `"}}`
}

func noApprovals(t *testing.T, h *approvalHarness) {
	t.Helper()
	if n, m := h.lado.approveAttempts(), h.lado.approvalsMade(); n != 0 || m != 0 {
		t.Fatalf("approval services called %d times, %d approvals recorded; want none", n, m)
	}
}

func TestApprovalDetailsBindWhatTheyShow(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	d := h.detail(cookie, kindPlan, string(testRevision))
	if d.Kind != kindPlan || !d.Approvable || d.DesktopOnly {
		t.Fatalf("plan detail = %+v", d)
	}
	var pb planBinding
	if err := json.Unmarshal(d.Binding, &pb); err != nil || pb != (planBinding{RevisionID: "rev-2", SHA256: testPlanSHA, PolicySHA256: testPolicySHA}) {
		t.Fatalf("plan binding = %s (%v)", d.Binding, err)
	}
	di := h.detail(cookie, kindIntegration, string(testSession))
	var ib integrationBinding
	if err := json.Unmarshal(di.Binding, &ib); err != nil {
		t.Fatal(err)
	}
	want := h.lado.records[testSession][0]
	if !di.Approvable || ib.SessionID != string(testSession) || len(ib.PRs) != 1 || ib.PRs[0].URL != testPRURL || ib.PRs[0].Head != testHead ||
		ib.PRs[0].VerificationSHA256 != want.SHA256 || !slices.Equal(ib.PRs[0].Confirm, []string{domain.LadoCodeOutsidePlan}) {
		t.Fatalf("integration detail = %+v, binding %s", di, di.Binding)
	}
	// The details carry Go's lines, the plan apart as the worker's text.
	if d.Title != "이 계획을 승인할까요?" || len(d.Lines) == 0 || d.Lines[0] != "로그인 화면을 더한다" || d.AgentText != "# 계획\n로그인을 더한다.\n" {
		t.Fatalf("plan detail summary = %q %q %q", d.Title, d.Lines, d.AgentText)
	}
	if di.Title != "통합을 승인할까요? PR #7 → main" || !slices.Contains(di.Lines, "계획 밖 변경 경로 (1): docs/notes.md") || di.Next == "" || di.AgentText != "" {
		t.Fatalf("integration detail summary = %q %q %q", di.Title, di.Lines, di.Next)
	}
	// Opening an integration runs one dry run of exactly its open PR.
	h.lado.read(func(f *fakeLado) {
		if len(f.dryRuns) != 1 || len(f.dryRuns[0].PRs) != 1 || f.dryRuns[0].PRs[0].PRURL != testPRURL || f.dryRuns[0].PRs[0].HeadSHA != testHead {
			t.Fatalf("dry runs = %+v", f.dryRuns)
		}
	})
	noApprovals(t, h)
}

func TestApproveCookieAloneRefused(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	// No ceremony, or one nobody began.
	wantError(t, "finish without a ceremony", h.approveFinish(cookie, "", map[string]any{}), http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	wantError(t, "a made-up ceremony", h.approveFinish(cookie, randomToken(), map[string]any{}), http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	// The desktop's approval routes do not exist here.
	for _, path := range []string{"/api/v1/lado/plans/rev-2/approve", "/api/v1/lado/executions/sess-int/integration-approve", "/api/approve"} {
		if w := h.send(http.MethodPost, path, map[string]any{"sha256": testPlanSHA, "policySha256": testPolicySHA}, cookie); w.Code != http.StatusNotFound {
			t.Errorf("POST %s with the cookie = %d, want 404", path, w.Code)
		}
	}
	// A begun ceremony finished with the cookie and no assertion, or an
	// empty one, approves nothing.
	for _, kind := range []string{kindPlan, kindIntegration} {
		id := string(testRevision)
		if kind == kindIntegration {
			id = string(testSession)
		}
		b := h.beginOK(cookie, h.detail(cookie, kind, id))
		wantError(t, kind+" finish without an assertion", h.approveFinish(cookie, b.Ceremony, map[string]any{}), http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED")
		h.paceDryRuns()
		b = h.beginOK(cookie, h.detail(cookie, kind, id))
		wantError(t, kind+" finish with a null credential", h.approveFinish(cookie, b.Ceremony, nil), http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED")
	}
	noApprovals(t, h)
}

func TestLoginChallengeCannotFinishApproval(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	// A login ceremony with its own assertion does not finish an approval,
	// and that try burns it.
	lb := h.loginBegin()
	wantError(t, "login ceremony at approve/finish", h.approveFinish(cookie, lb.Ceremony, h.key.get(t, lb.asBegin(), iPhoneFlags)),
		http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	if res, c := h.loginFinish(lb.Ceremony, h.key.get(t, lb.asBegin(), iPhoneFlags)); res != "stale" || c != nil {
		t.Fatalf("the burned login ceremony then logs in: %q", res)
	}
	// A login challenge signed and sent with an approval ceremony does not
	// verify.
	d := h.detail(cookie, kindPlan, string(testRevision))
	ab := h.beginOK(cookie, d)
	lb = h.loginBegin()
	wantError(t, "login challenge for an approval", h.approveFinish(cookie, ab.Ceremony, h.key.get(t, lb.asBegin(), iPhoneFlags)),
		http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED")
	// An approval ceremony does not log in.
	ab = h.beginOK(cookie, d)
	if res, c := h.loginFinish(ab.Ceremony, h.key.get(t, ab.asBegin(), iPhoneFlags)); res != "stale" || c != nil {
		t.Fatalf("an approval ceremony at login/finish = %q", res)
	}
	noApprovals(t, h)
}

func TestCeremonyBurnsOnFailedFinish(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	d := h.detail(cookie, kindPlan, string(testRevision))
	finishBoth := func(what string, first func(b assertionView) (string, any), firstStatus int, firstCode string) {
		t.Helper()
		b := h.beginOK(cookie, d)
		c, cred := first(b)
		wantError(t, what, h.approveFinish(c, b.Ceremony, cred), firstStatus, firstCode)
		wantError(t, what+", then the right assertion", h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)),
			http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	}
	wrong := otherSigner(t, h.key)
	finishBoth("a wrong signature", func(b assertionView) (string, any) {
		return cookie, wrong.get(t, b.asBegin(), iPhoneFlags)
	}, http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED")
	finishBoth("no user verification", func(b assertionView) (string, any) {
		return cookie, h.key.get(t, b.asBegin(), protocol.FlagUserPresent|protocol.FlagBackupEligible|protocol.FlagBackupState)
	}, http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED")
	finishBoth("no session", func(b assertionView) (string, any) {
		return "", h.key.get(t, b.asBegin(), iPhoneFlags)
	}, http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")
	other := h.login()
	finishBoth("another session", func(b assertionView) (string, any) {
		return other, h.key.get(t, b.asBegin(), iPhoneFlags)
	}, http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	// Past 120 seconds the ceremony is too old, and gone after.
	b := h.beginOK(cookie, d)
	h.clock.advance(approveTTL + time.Second)
	wantError(t, "a ceremony past its time", h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)),
		http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	noApprovals(t, h)
	if got := h.alerts.kinds(); !slices.Contains(got, AlertSignatureFailed) {
		t.Fatalf("alerts = %v, want the wrong signature raised", got)
	}
}

func TestCeremonyReplayRefused(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	b := h.beginOK(cookie, h.detail(cookie, kindPlan, string(testRevision)))
	cred := h.key.get(t, b.asBegin(), iPhoneFlags)
	if w := h.approveFinish(cookie, b.Ceremony, cred); w.Code != http.StatusOK {
		t.Fatalf("approve finish: %d %s\n%s", w.Code, w.Body, h.logs)
	}
	if n := h.lado.approvalsMade(); n != 1 {
		t.Fatalf("approvals = %d, want 1", n)
	}
	wantError(t, "the same finish again", h.approveFinish(cookie, b.Ceremony, cred), http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	// The old assertion does not finish a new ceremony of the same plan.
	h.seedPlan(testPlanSHA, true)
	b2 := h.beginOK(cookie, h.detail(cookie, kindPlan, string(testRevision)))
	wantError(t, "an old assertion for a new ceremony", h.approveFinish(cookie, b2.Ceremony, cred), http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED")
	if n := h.lado.approvalsMade(); n != 1 {
		t.Fatalf("approvals after the replays = %d, want 1", n)
	}
}

func TestBeginRefusesStaleBinding(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	d := h.detail(cookie, kindPlan, string(testRevision))
	var pb map[string]any
	if err := json.Unmarshal(d.Binding, &pb); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]any{"sha256": strings.Repeat("e", 64), "policySha256": strings.Repeat("f", 64), "revisionId": "rev-1"} {
		changed := map[string]any{}
		for k, v := range pb {
			changed[k] = v
		}
		changed[field] = value
		w := h.approveBegin(cookie, kindPlan, changed)
		if field == "revisionId" {
			// A revision LADO does not have is not found rather than stale.
			wantError(t, "plan binding with another revision", w, http.StatusNotFound, "LADO_PLAN_NOT_FOUND")
			continue
		}
		wantError(t, "plan binding with another "+field, w, http.StatusConflict, "LADO_PHONE_STALE")
	}
	wantError(t, "a binding with an unknown field", h.approveBegin(cookie, kindPlan, map[string]any{
		"revisionId": "rev-2", "sha256": testPlanSHA, "policySha256": testPolicySHA, "extra": 1,
	}), http.StatusBadRequest, "LADO_PHONE_BAD_REQUEST")
	wantError(t, "another kind", h.approveBegin(cookie, "rule", d.Binding), http.StatusBadRequest, "LADO_PHONE_BAD_REQUEST")
	// The rules change after the detail was read.
	h.lado.set(func(f *fakeLado) {
		v := f.rules[testPlanTask]
		v.SHA256 = strings.Repeat("9", 64)
		f.rules[testPlanTask] = v
	})
	wantError(t, "plan whose rules changed", h.approveBegin(cookie, kindPlan, d.Binding), http.StatusConflict, "LADO_PHONE_STALE")

	di := h.detail(cookie, kindIntegration, string(testSession))
	mutate := func(edit func(b *integrationBinding)) integrationBinding {
		var b integrationBinding
		if err := json.Unmarshal(di.Binding, &b); err != nil {
			t.Fatal(err)
		}
		edit(&b)
		return b
	}
	for name, b := range map[string]integrationBinding{
		"head":    mutate(func(b *integrationBinding) { b.PRs[0].Head = strings.Repeat("e", 40) }),
		"record":  mutate(func(b *integrationBinding) { b.PRs[0].VerificationSHA256 = strings.Repeat("e", 64) }),
		"codes":   mutate(func(b *integrationBinding) { b.PRs[0].Confirm = nil }),
		"pr":      mutate(func(b *integrationBinding) { b.PRs[0].URL = "https://github.com/acme/app/pull/9" }),
		"session": mutate(func(b *integrationBinding) { b.SessionID = "sess-other" }),
	} {
		h.paceDryRuns()
		w := h.approveBegin(cookie, kindIntegration, b)
		if w.Code != http.StatusConflict && w.Code != http.StatusNotFound {
			t.Errorf("integration binding with another %s: %d %s, want refused", name, w.Code, w.Body)
		}
	}
	// A new record after the detail was read.
	h.seedIntegration(testSession, testIntTask, testPRURL, testHead, 7, append(h.cleanItems(),
		mustItem(t, domain.LadoVerifyReview, domain.LadoVerifyConfirm, domain.LadoCodeReviewInconclusive, domain.LadoReviewDetails{Rounds: 2}))...)
	h.paceDryRuns()
	wantError(t, "integration whose record changed", h.approveBegin(cookie, kindIntegration, di.Binding), http.StatusConflict, "LADO_PHONE_STALE")
	noApprovals(t, h)
}

func TestPlanChangedAfterBeginRefused(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	d := h.detail(cookie, kindPlan, string(testRevision))
	b := h.beginOK(cookie, d)
	// A newer revision is pinned after begin.
	h.seedPlan(testPlanSHA, false)
	wantError(t, "a superseded plan", h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)), http.StatusConflict, "LADO_PLAN_SUPERSEDED")
	// The revision's pinned bytes differ after begin.
	h.seedPlan(testPlanSHA, true)
	b = h.beginOK(cookie, h.detail(cookie, kindPlan, string(testRevision)))
	h.seedPlan(strings.Repeat("e", 64), true)
	wantError(t, "a changed plan", h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)), http.StatusConflict, "LADO_PLAN_CHANGED")
	// The phone handed over what it bound, never what it read later.
	h.lado.read(func(f *fakeLado) {
		if len(f.planCalls) != 2 {
			t.Fatalf("ApprovePlan calls = %+v", f.planCalls)
		}
		for _, c := range f.planCalls {
			if c.revision != testRevision || c.sha256 != testPlanSHA || c.policySHA256 != testPolicySHA {
				t.Errorf("ApprovePlan got %+v, want the bound values", c)
			}
		}
	})
	if n := h.lado.approvalsMade(); n != 0 {
		t.Fatalf("approvals = %d", n)
	}
}

func TestIntegrationDBSQLRefusedAtBeginAndFinish(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	const dbSession, dbURL = domain.SessionID("sess-db"), "https://github.com/acme/app/pull/8"
	h.seedIntegration(dbSession, "task-db", dbURL, strings.Repeat("8", 40), 8, append(h.cleanItems(), dbItem(t))...)
	d := h.detail(cookie, kindIntegration, string(dbSession))
	if d.Approvable || !d.DesktopOnly || !strings.Contains(d.Reason, "데스크톱") {
		t.Fatalf("detail of a head with database SQL = %+v", d)
	}
	wantError(t, "begin with database SQL", h.approveBegin(cookie, kindIntegration, d.Binding), http.StatusForbidden, "LADO_PHONE_DESKTOP_ONLY")

	// Database SQL that turns up after begin: Allow refuses it before
	// anything is recorded.
	h.paceDryRuns()
	b := h.beginOK(cookie, h.detail(cookie, kindIntegration, string(testSession)))
	h.lado.set(func(f *fakeLado) {
		rec := f.records[testSession][0]
		rec.Verification.Items = append(slices.Clone(rec.Verification.Items), dbItem(t))
		f.records[testSession][0] = rec
	})
	wantError(t, "finish with database SQL", h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)),
		http.StatusForbidden, "LADO_PHONE_DESKTOP_ONLY")
	h.lado.read(func(f *fakeLado) {
		if f.allowAsked != 1 || len(f.approveCalls) != 1 || len(f.integrated) != 0 {
			t.Fatalf("Allow asked %d, approvals tried %d, recorded %d; want 1, 1, 0", f.allowAsked, len(f.approveCalls), len(f.integrated))
		}
	})
}

func TestRevokedOrServeMovedRefused(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	d := h.detail(cookie, kindPlan, string(testRevision))
	finish := func(b assertionView) *httpResult {
		w := h.approveFinish(cookie, b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags))
		return &httpResult{w.Code, decodeInto[errorView](t, w).Code}
	}

	// tailscale serve points the port elsewhere after begin.
	b := h.beginOK(cookie, d)
	h.ts.mu.Lock()
	h.ts.target = "http://127.0.0.1:9999"
	h.ts.mu.Unlock()
	if got := finish(b); *got != (httpResult{http.StatusConflict, "LADO_PHONE_SERVE_MOVED"}) {
		t.Fatalf("serve moved: %+v", got)
	}
	h.ts.mu.Lock()
	h.ts.target = ServeTargetFor(h.socket())
	h.ts.mu.Unlock()

	// Phone approval is turned off after begin.
	b = h.beginOK(cookie, d)
	if _, _, err := h.store.DisableLadoPhone(context.Background(), domain.LadoAuditEvent{At: h.clock.Now(), Action: AuditDisabled, SubjectKind: "daemon", SubjectID: "phone_approval"}); err != nil {
		t.Fatal(err)
	}
	if got := finish(b); *got != (httpResult{http.StatusConflict, "LADO_PHONE_OFF"}) {
		t.Fatalf("turned off: %+v", got)
	}
	if _, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-again-bbbb", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject)); err != nil {
		t.Fatal(err)
	}
	// Turning it on again ended the sessions.
	wantError(t, "the old session after turning on again", h.send(http.MethodGet, "/api/approvals", nil, cookie), http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")
	cookie = h.login()

	// The passkey is revoked after begin.
	b = h.beginOK(cookie, d)
	if _, err := h.store.RevokeLadoPhonePasskey(context.Background(), h.passkey.ID, domain.LadoAuditEvent{At: h.clock.Now(), Action: AuditPasskeyRevoked, SubjectKind: "phone_passkey", SubjectID: h.passkey.ID}); err != nil {
		t.Fatal(err)
	}
	if got := finish(b); *got != (httpResult{http.StatusForbidden, "LADO_PHONE_PASSKEY_REVOKED"}) {
		t.Fatalf("revoked: %+v", got)
	}
	noApprovals(t, h)
}

type httpResult struct {
	status int
	code   string
}

func TestApproveRecordsPasskeyActorAndBindingAudit(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	fp := h.passkey.Fingerprint
	begun := h.clock.Now()
	b := h.beginOK(cookie, h.detail(cookie, kindPlan, string(testRevision)))
	cred := h.key.get(t, b.asBegin(), iPhoneFlags)
	h.clock.advance(30 * time.Second)
	w := h.approveFinish(cookie, b.Ceremony, cred)
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s\n%s", w.Code, w.Body, h.logs)
	}
	var proof domain.LadoPresenceProof
	h.lado.read(func(f *fakeLado) {
		if len(f.planApproved) != 1 {
			t.Fatalf("plan approvals = %+v", f.planApproved)
		}
		proof = f.planApproved[0].proof
	})
	want := domain.LadoPresenceProof{
		Nonce: "pk-" + b.Ceremony, Action: domain.LadoPresencePlanApprove, SubjectID: string(testRevision),
		Via: domain.LadoPresenceViaPasskey + fp, ExpiresAt: begun.Add(approveTTL).UTC(), UsedAt: h.clock.Now().UTC(),
	}
	if proof != want || proof.Actor() != "human:passkey:"+fp {
		t.Fatalf("proof = %+v\nwant  %+v", proof, want)
	}
	bindingSHA := sha256Hex(planBindingJSON(testPlanSHA, testPolicySHA))
	clientData := cred["response"].(map[string]any)["clientDataJSON"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(clientData)
	if err != nil {
		t.Fatal(err)
	}
	checkAudit := func(kind, subjectKind, subjectID, ceremony, binding string, rawClientData []byte) {
		t.Helper()
		ev := h.audits.last(t, AuditApproved)
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if ev.Actor != "human:passkey:"+fp || ev.SubjectKind != subjectKind || ev.SubjectID != subjectID ||
			payload["kind"] != kind || payload["bindingSha256"] != binding || payload["ceremonyId"] != ceremony ||
			payload["clientDataSha256"] != sha256Hex(string(rawClientData)) || payload["passkeyId"] != h.passkey.ID {
			t.Fatalf("audit = %+v %v", ev, payload)
		}
	}
	checkAudit(kindPlan, "plan_revision", string(testRevision), b.Ceremony, bindingSHA, raw)

	// An integration: the proof names the head in merge order, and Allow was
	// asked before recording.
	bi := h.beginOK(cookie, h.detail(cookie, kindIntegration, string(testSession)))
	icred := h.key.get(t, bi.asBegin(), iPhoneFlags)
	if w := h.approveFinish(cookie, bi.Ceremony, icred); w.Code != http.StatusOK {
		t.Fatalf("approve integration: %d %s\n%s", w.Code, w.Body, h.logs)
	}
	h.lado.read(func(f *fakeLado) {
		if len(f.integrated) != 1 || f.allowAsked != 1 {
			t.Fatalf("integrations %+v, Allow asked %d", f.integrated, f.allowAsked)
		}
		p := f.integrated[0].proof
		if p.Nonce != "pk-"+bi.Ceremony || p.Action != domain.LadoPresenceIntegrationApprove || p.SubjectID != string(testSession)+"@"+testHead ||
			p.Actor() != "human:passkey:"+fp {
			t.Fatalf("integration proof = %+v", p)
		}
		in := f.approveCalls[0]
		if len(in.PRs) != 1 || in.PRs[0].PRURL != testPRURL || in.PRs[0].HeadSHA != testHead ||
			in.PRs[0].VerificationSHA256 != f.records[testSession][0].SHA256 || !slices.Equal(in.PRs[0].Acknowledge, []string{domain.LadoCodeOutsidePlan}) {
			t.Fatalf("approval input = %+v", in)
		}
	})
	iraw, _ := base64.RawURLEncoding.DecodeString(icred["response"].(map[string]any)["clientDataJSON"].(string))
	checkAudit(kindIntegration, "lado_execution", string(testSession), bi.Ceremony, bi.BindingSHA256, iraw)
	if stored := h.passkeys()[0]; !stored.LastUsedAt.Equal(h.clock.Now()) {
		t.Fatalf("last use = %v", stored.LastUsedAt)
	}
}

func TestIntegrationPresenceRefusesAnotherSubject(t *testing.T) {
	proof := domain.LadoPresenceProof{Nonce: "pk-x", Action: domain.LadoPresenceIntegrationApprove}
	presence := oncePresence(proof, string(testSession)+"@"+testHead)
	if _, err := presence(domain.LadoPresencePlanApprove, string(testSession)+"@"+testHead); err == nil {
		t.Fatal("presence gave a proof for another action")
	}
	presence = oncePresence(proof, string(testSession)+"@"+testHead)
	if _, err := presence(domain.LadoPresenceIntegrationApprove, string(testSession)+"@"+strings.Repeat("d", 40)); err == nil {
		t.Fatal("presence gave a proof for another head")
	}
	presence = oncePresence(proof, string(testSession)+"@"+testHead)
	got, err := presence(domain.LadoPresenceIntegrationApprove, string(testSession)+"@"+testHead)
	if err != nil || got.Nonce != "pk-x" || got.SubjectID != string(testSession)+"@"+testHead {
		t.Fatalf("presence = %+v, %v", got, err)
	}
	if _, err := presence(domain.LadoPresenceIntegrationApprove, string(testSession)+"@"+testHead); err == nil {
		t.Fatal("presence gave its proof twice")
	}
}

func TestApproveChallengeBindsBinding(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	dp := h.detail(cookie, kindPlan, string(testRevision))
	bp := h.beginOK(cookie, dp)
	wantSHA := sha256Hex(planBindingJSON(testPlanSHA, testPolicySHA))
	challenge := func(ceremony, binding string) string {
		sum := sha256.Sum256([]byte("lado-phone-approve-v1\n" + ceremony + "\n" + binding))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	}
	pk := bp.Options.PublicKey
	if bp.BindingSHA256 != wantSHA || pk.Challenge != challenge(bp.Ceremony, wantSHA) {
		t.Fatalf("plan begin = %+v, want binding %s", bp, wantSHA)
	}
	if pk.RPID != testDNS || pk.UserVerification != "required" || len(pk.AllowCredentials) != 1 || pk.AllowCredentials[0].ID != b64(h.key.id) {
		t.Fatalf("approval options = %+v", pk)
	}

	// The integration's binding, its codes sorted, gives another challenge,
	// and a session keeps one approval ceremony: the plan's is gone.
	di := h.detail(cookie, kindIntegration, string(testSession))
	bi := h.beginOK(cookie, di)
	rec := h.lado.records[testSession][0]
	wantI := sha256Hex(`{"kind":"integration","binding":{"sessionId":"sess-int","prs":[{"url":"` + testPRURL + `","head":"` + testHead +
		`","verificationSha256":"` + rec.SHA256 + `","confirm":["outside_plan"]}]}}`)
	if bi.BindingSHA256 != wantI || bi.Options.PublicKey.Challenge != challenge(bi.Ceremony, wantI) || bi.Options.PublicKey.Challenge == pk.Challenge {
		t.Fatalf("integration begin = %+v, want binding %s", bi, wantI)
	}
	wantError(t, "the plan ceremony after a newer begin", h.approveFinish(cookie, bp.Ceremony, h.key.get(t, bp.asBegin(), iPhoneFlags)),
		http.StatusConflict, "LADO_PHONE_CEREMONY_STALE")
	// The same binding begun again has a new ceremony and challenge.
	bp2 := h.beginOK(cookie, dp)
	if bp2.BindingSHA256 != wantSHA || bp2.Ceremony == bp.Ceremony || bp2.Options.PublicKey.Challenge == pk.Challenge {
		t.Fatalf("plan begun again = %+v", bp2)
	}
	noApprovals(t, h)
}

func TestApprovalAnswersOnlyTheSessionPasskey(t *testing.T) {
	h := newApprovalHarness(t)
	second := newSoftKey(t)
	f := h.pairPhone(second)
	if _, err := h.s.ConfirmEnroll(context.Background(), f.Code, h.enrollProof("enroll-proof-second", second.credentialSHA256())); err != nil {
		t.Fatalf("enroll a second passkey: %v", err)
	}
	if b := h.loginBegin(); len(b.Options.PublicKey.AllowCredentials) != 2 {
		t.Fatalf("login offers %d passkeys, want both", len(b.Options.PublicKey.AllowCredentials))
	}
	cookie := h.login()
	b := h.beginOK(cookie, h.detail(cookie, kindPlan, string(testRevision)))
	if allow := b.Options.PublicKey.AllowCredentials; len(allow) != 1 || allow[0].ID != b64(h.key.id) {
		t.Fatalf("approval offers %+v, want only the session's passkey", allow)
	}
	wantError(t, "the other passkey's assertion", h.approveFinish(cookie, b.Ceremony, second.get(t, b.asBegin(), iPhoneFlags)),
		http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED")
	noApprovals(t, h)
}
