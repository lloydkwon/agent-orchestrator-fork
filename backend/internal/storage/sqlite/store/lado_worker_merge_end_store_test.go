package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// sessionEndsOnPRMerge reads a session's merge-completion policy.
func sessionEndsOnPRMerge(t *testing.T, s *sqlite.Store, id domain.SessionID) bool {
	t.Helper()
	rec, ok, err := s.GetSession(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetSession %s = (%v, %v)", id, ok, err)
	}
	return rec.TerminateOnPRMerge
}

// LADO fork: linking a pull request worker turns on AO's merge-completion
// policy for its session, so a worker whose PR merged ends instead of idling
// on and holding its task and worker slots (both count live execution
// sessions). Before, infra-monitor #1's merged worker held the project's one
// slot and the next approved task waited on slot_project.
func TestLinkLadoExecutionEndsSessionOnPRMerge(t *testing.T) {
	s := openLadoVerifyStore(t)
	exec := mustLadoExecution(t, s, "1", ladoVerifyAt, false)
	if !sessionEndsOnPRMerge(t, s, exec.SessionID) {
		t.Fatal("the linked worker's session does not end on PR merge")
	}
}

// linkLadoWorker links a new worker session to a new queued task of project
// mer, started at started with prRepos, and returns the session, the entry
// and its lease token.
func linkLadoWorker(t *testing.T, s *sqlite.Store, key string, started time.Time, prRepos []string) (domain.SessionRecord, domain.LadoQueueEntryID, string) {
	t.Helper()
	entry := mustQueuedLadoTask(t, s, key, "mer", ladoQueueAt)
	claimed := mustClaimLadoQueue(t, s, started, 1, ladoTokens("merge-end-"+key), entry)[0]
	session := mustLadoSession(t, s, "mer", false)
	if err := s.LinkLadoExecution(context.Background(), entry, claimed.LeaseToken,
		domain.LadoExecution{SessionID: session.ID, StartedAt: started, PRRepos: prRepos}); err != nil {
		t.Fatalf("link %s: %v", entry, err)
	}
	return session, entry, claimed.LeaseToken
}

// A worker expected to open PRs in several repositories keeps its session
// (#64): AO would end it once the first PR merged, before the next opened.
func TestLinkLadoExecutionKeepsMultiRepoWorkerSession(t *testing.T) {
	s := openLadoVerifyStore(t)
	session, _, _ := linkLadoWorker(t, s, "1", ladoVerifyAt, []string{"api", "web"})
	if sessionEndsOnPRMerge(t, s, session.ID) {
		t.Fatal("a worker of two repositories ends on its first PR merge")
	}
	one, _, _ := linkLadoWorker(t, s, "2", ladoVerifyAt, []string{"api"})
	if !sessionEndsOnPRMerge(t, s, one.ID) {
		t.Fatal("a worker of one declared repository does not end on PR merge")
	}
}

// Linking a session again keeps the policy the user set since, and a link
// that starts before the session's last change (a recovered link starts at
// the session's creation) does not move the session's time back.
func TestLinkLadoExecutionKeepsUserPolicyAndSessionTime(t *testing.T) {
	s := openLadoVerifyStore(t)
	ctx := context.Background()
	session, entry, token := linkLadoWorker(t, s, "1", ladoVerifyAt, nil)
	later := ladoVerifyAt.Add(time.Hour)
	if ok, err := s.SetSessionTerminateOnPRMerge(ctx, session.ID, false, later); !ok || err != nil {
		t.Fatalf("turn the policy off = (%v, %v)", ok, err)
	}
	if err := s.LinkLadoExecution(ctx, entry, token, domain.LadoExecution{SessionID: session.ID, StartedAt: ladoVerifyAt}); err != nil {
		t.Fatalf("link again: %v", err)
	}
	if sessionEndsOnPRMerge(t, s, session.ID) {
		t.Fatal("linking again turned the user's policy back on")
	}

	recovered := mustLadoSession(t, s, "mer", false)
	before, _, _ := s.GetSession(ctx, recovered.ID)
	entry2 := mustQueuedLadoTask(t, s, "2", "mer", ladoQueueAt)
	claimed := mustClaimLadoQueue(t, s, ladoVerifyAt, 1, ladoTokens("merge-end-r"), entry2)[0]
	if err := s.LinkLadoExecution(ctx, entry2, claimed.LeaseToken,
		domain.LadoExecution{SessionID: recovered.ID, StartedAt: before.UpdatedAt.Add(-time.Hour)}); err != nil {
		t.Fatalf("link the recovered session: %v", err)
	}
	after, _, _ := s.GetSession(ctx, recovered.ID)
	if after.UpdatedAt.Before(before.UpdatedAt) {
		t.Fatalf("session updated_at moved back from %s to %s", before.UpdatedAt, after.UpdatedAt)
	}
}
