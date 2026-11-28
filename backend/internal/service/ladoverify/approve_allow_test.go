package ladoverify

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Allow is asked once an approval is ready and before anything is
// recorded: its refusal comes back as it is, with no approval, proof,
// audit event, status post or database change step. A dry run does not ask
// it, and an Allow that lets the approval through changes nothing.
func TestApproveIntegrationAllowRefusesBeforeRecording(t *testing.T) {
	f := newApproveFixture(t)
	db := newFakeDBChanges(t)
	f.svc.SetDBChanges(db)
	exec := f.add("1", verifyHead1)
	refusal := errors.New("the phone cannot approve database SQL")
	var (
		asked    [][]ApprovePRResult
		sessions []domain.SessionID
	)
	allow := func(_ context.Context, got domain.LadoExecution, prs []ApprovePRResult) error {
		sessions = append(sessions, got.SessionID)
		asked = append(asked, prs)
		if _, has, _ := prs[0].Verification.DBChanges(); has {
			return refusal
		}
		return nil
	}

	preview, err := f.svc.ApproveIntegration(f.ctx, ApproveInput{SessionID: exec.SessionID, PRURL: exec.PR.URL, HeadSHA: exec.PR.HeadSHA, DryRun: true, Allow: allow})
	if err != nil {
		t.Fatalf("dry run with Allow = %v", err)
	}
	if len(asked) != 0 {
		t.Fatalf("a dry run asked Allow %d times, want none", len(asked))
	}

	_, err = f.svc.ApproveIntegration(f.ctx, ApproveInput{
		SessionID: exec.SessionID, PRURL: exec.PR.URL, HeadSHA: exec.PR.HeadSHA,
		VerificationSHA256: preview.SHA256, Acknowledge: preview.Confirm,
		Presence: presenceProof("nonce-allow", false), Allow: allow,
	})
	if !errors.Is(err, refusal) || err.Error() != refusal.Error() {
		t.Fatalf("refused approval = %v, want Allow's error as it is", err)
	}
	if len(asked) != 1 || sessions[0] != exec.SessionID || len(asked[0]) != 1 || asked[0][0].SHA256 != preview.SHA256 || asked[0][0].Approval != nil {
		t.Fatalf("Allow was asked %+v, want once about the ready head not approved yet", asked)
	}
	f.store.mu.Lock()
	proofs := len(f.store.proofs)
	f.store.mu.Unlock()
	if n, audits := f.store.approvalCount(), f.store.auditEvents(); n != 0 || proofs != 0 || len(audits) != 0 {
		t.Errorf("a refused approval wrote %d approvals, %d proofs and %v", n, proofs, audits)
	}
	if posts := f.host.postsMade(); len(posts) != 0 {
		t.Errorf("a refused approval posted %+v", posts)
	}
	db.read(func(d *fakeDBChanges) {
		if len(d.approved) != 0 || len(d.holdAsks) != 0 {
			t.Errorf("a refused approval told the database change service %+v and asked holds %+v", d.approved, d.holdAsks)
		}
	})
	select {
	case <-f.svc.kick:
		t.Error("a refused approval kicked a pass")
	default:
	}

	// Without database SQL the same Allow lets the approval through.
	db.set(func(d *fakeDBChanges) { d.off = true })
	plain := f.add("2", verifyHead3)
	p := f.preview(plain)
	result, err := f.svc.ApproveIntegration(f.ctx, ApproveInput{
		SessionID: plain.SessionID, PRURL: plain.PR.URL, HeadSHA: plain.PR.HeadSHA,
		VerificationSHA256: p.SHA256, Acknowledge: p.Confirm,
		Presence: presenceProof("nonce-allowed", false), Allow: allow,
	})
	if err != nil || result.Approval == nil {
		t.Fatalf("approval Allow lets through = %+v, %v; want recorded", result.Approval, err)
	}
	if len(asked) != 2 || sessions[1] != plain.SessionID || f.store.approvalCount() != 1 {
		t.Errorf("Allow asked about %v and %d approvals stored, want %s then %s and 1", sessions, f.store.approvalCount(), exec.SessionID, plain.SessionID)
	}
}
