package ladoverify

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// An integration approved with a phone passkey's proof is recorded as that
// passkey, in the approval's actor and in its audit event.
func TestApproveIntegrationRecordsPasskeyActor(t *testing.T) {
	f := newApproveFixture(t)
	exec := f.add("1", verifyHead1)
	preview := f.preview(exec)
	const via = domain.LadoPresenceViaPasskey + "3f9a1c0d2b4e5f60"
	const want = "human:passkey:3f9a1c0d2b4e5f60"
	desktop := presenceProof("pk-approve-1", false)

	result, err := f.svc.ApproveIntegration(f.ctx, ApproveInput{
		SessionID: exec.SessionID, PRURL: exec.PR.URL, HeadSHA: exec.PR.HeadSHA,
		VerificationSHA256: preview.SHA256, Acknowledge: preview.Confirm,
		Presence: func(action, subject string) (domain.LadoPresenceProof, error) {
			proof, err := desktop(action, subject)
			proof.Via = via
			return proof, err
		},
	})
	if err != nil {
		t.Fatalf("approval = %v", err)
	}
	if result.Approval == nil || result.Approval.Actor != want {
		t.Fatalf("approval = %+v, want its actor %s", result.Approval, want)
	}
	if stored := f.store.approval(result.Approval.ID); stored.Actor != want {
		t.Errorf("stored approval actor = %q, want %q", stored.Actor, want)
	}
	if audits := f.store.auditEvents(); len(audits) != 1 || audits[0].Actor != want {
		t.Errorf("audit events = %+v, want one by %s", audits, want)
	}
}
