package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LADO fork (#4): ReleaseIdleChat stops a quiet orchestrator's provider so its
// next launch resumes the same conversation with its instructions rebuilt.

func orchestratorReleaseHarness(t *testing.T) (*harness, *hibernationConversation) {
	t.Helper()
	// Hibernation is off and someone is looking at the orchestrator: neither
	// keeps a refresh from releasing it.
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted, func() bool { return false })
	ctx := context.Background()
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("get orchestrator = %v, %v", found, err)
	}
	rec.Kind = domain.KindOrchestrator
	if err := h.st.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetChatView(ctx, testSession, "viewer", true); err != nil {
		t.Fatal(err)
	}
	return h, conv
}

func TestReleaseIdleChatStopsAQuietOrchestrator(t *testing.T) {
	h, conv := orchestratorReleaseHarness(t)
	ctx := context.Background()

	released, err := h.svc.ReleaseIdleChat(ctx, testSession, 5*time.Second)
	if err != nil || !released || conv.calls.Load() != 1 || h.svc.HasLiveChatController(testSession) {
		t.Fatalf("release: released=%v err=%v stops=%d live=%v", released, err, conv.calls.Load(), h.svc.HasLiveChatController(testSession))
	}
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found || rec.HibernatedAt == nil || rec.IsTerminated {
		t.Fatalf("released orchestrator: found=%v err=%v hibernated=%v terminated=%v", found, err, rec.HibernatedAt, rec.IsTerminated)
	}
	if rec.Metadata.ProviderConversationID != conv.ProviderConversationID() {
		t.Fatalf("released orchestrator conversation = %q, want %q kept for resume", rec.Metadata.ProviderConversationID, conv.ProviderConversationID())
	}
}

func TestReleaseIdleChatKeepsABusyOrchestratorRunning(t *testing.T) {
	for name, tc := range map[string]struct {
		quiet  time.Duration
		change func(*domain.SessionRecord)
		inBg   bool
	}{
		"activity inside the quiet time": {quiet: time.Minute},
		"turn in progress": {quiet: 5 * time.Second, change: func(rec *domain.SessionRecord) {
			rec.Activity.State = domain.ActivityActive
		}},
		"waiting on a person": {quiet: 5 * time.Second, change: func(rec *domain.SessionRecord) {
			rec.Activity.State = domain.ActivityBlocked
		}},
		"background work": {quiet: 5 * time.Second, inBg: true},
	} {
		t.Run(name, func(t *testing.T) {
			h, conv := orchestratorReleaseHarness(t)
			ctx := context.Background()
			if tc.change != nil {
				rec, _, err := h.st.GetSession(ctx, testSession)
				if err != nil {
					t.Fatal(err)
				}
				tc.change(&rec)
				if err := h.st.UpdateSession(ctx, rec); err != nil {
					t.Fatal(err)
				}
			}
			conv.backgroundRunning = tc.inBg

			released, _ := h.svc.ReleaseIdleChat(ctx, testSession, tc.quiet)
			if released || conv.calls.Load() != 0 || !h.svc.HasLiveChatController(testSession) {
				t.Fatalf("busy orchestrator: released=%v stops=%d live=%v", released, conv.calls.Load(), h.svc.HasLiveChatController(testSession))
			}
			rec, _, err := h.st.GetSession(ctx, testSession)
			if err != nil || rec.HibernatedAt != nil {
				t.Fatalf("busy orchestrator recorded a stop: at=%v err=%v", rec.HibernatedAt, err)
			}
		})
	}
}
