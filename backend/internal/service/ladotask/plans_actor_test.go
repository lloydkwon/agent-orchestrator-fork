package ladotask

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A plan approved with a phone passkey's proof is audited as that passkey,
// and its proof keeps the way it came.
func TestApprovePlanRecordsPasskeyActor(t *testing.T) {
	pf := newPlanFixture(t)
	ctx := context.Background()
	rev := pf.planned(t)
	proof := pf.proof(rev.ID)
	proof.Via = domain.LadoPresenceViaPasskey + "3f9a1c0d2b4e5f60"

	if _, err := pf.svc.ApprovePlan(ctx, rev.ID, rev.SHA256, policyA, proof); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	var actor string
	if err := pf.raw.QueryRow(`SELECT actor FROM lado_audit_events WHERE action = 'plan.approved'`).Scan(&actor); err != nil {
		t.Fatalf("read the approval's audit event: %v", err)
	}
	if actor != "human:passkey:3f9a1c0d2b4e5f60" {
		t.Errorf("audit actor = %q, want human:passkey:3f9a1c0d2b4e5f60", actor)
	}
	var via string
	if err := pf.raw.QueryRow(`SELECT via FROM lado_presence_proofs WHERE nonce = ?`, proof.Nonce).Scan(&via); err != nil {
		t.Fatalf("read the approval's proof: %v", err)
	}
	if via != proof.Via {
		t.Errorf("stored proof via = %q, want %q", via, proof.Via)
	}
}
