package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestLatestConversationTurnUsesRowOrderAndSkipsRollback(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	if _, err := s.LatestConversationTurn(ctx, conversation.ID); !errors.Is(err, domain.ErrNoConversationTurn) {
		t.Fatalf("empty conversation: %v", err)
	}
	// Both prompts share RequestedAt; SQLite row order must decide the latest.
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")
	turn, err := s.LatestConversationTurn(ctx, conversation.ID)
	if err != nil || turn.ID != "turn-b" || turn.State != domain.TurnStateQueued {
		t.Fatalf("latest same-time turn: %+v, %v", turn, err)
	}
	if _, err := s.RollbackTurns(ctx, conversation.ID, "turn-b", testNow); err != nil {
		t.Fatal(err)
	}
	turn, err = s.LatestConversationTurn(ctx, conversation.ID)
	if err != nil || turn.ID != "turn-a" {
		t.Fatalf("latest after rollback: %+v, %v", turn, err)
	}
}

func TestLatestConversationTurnFollowsActiveBranchAndNestedCutoffs(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")
	child := domain.ConversationBranch{
		ID: "latest-child", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-latest-child", ParentBranchID: conversation.ActiveBranchID,
		ForkAfterTurnID: "turn-a", ReplacedTurnID: "turn-b", ForkAfterSequence: 1,
	}
	if err := s.CreateConversationBranch(ctx, child, testNow); err != nil {
		t.Fatal(err)
	}
	activateTestBranch(t, s, session, conversation, child.ID, child.ProviderConversationID, "generation-child")
	turn, err := s.LatestConversationTurn(ctx, conversation.ID)
	if err != nil || turn.ID != "turn-a" {
		t.Fatalf("active lineage leaked replaced root turn: %+v, %v", turn, err)
	}
	appendBranchPrompt(t, s, session, conversation, "generation-child", "child", "Child prompt")
	turn, err = s.LatestConversationTurn(ctx, conversation.ID)
	if err != nil || turn.ID != "turn-child" {
		t.Fatalf("latest child turn: %+v, %v", turn, err)
	}
	// A narrower nested cutoff must hide every turn on both ancestor branches.
	nested := domain.ConversationBranch{
		ID: "latest-nested", ConversationID: conversation.ID, SessionID: session.ID,
		ProviderConversationID: "thread-latest-nested", ParentBranchID: child.ID,
		ReplacedTurnID: "turn-a", ForkAfterSequence: 0,
	}
	if err := s.CreateConversationBranch(ctx, nested, testNow); err != nil {
		t.Fatal(err)
	}
	activateTestBranch(t, s, session, conversation, nested.ID, nested.ProviderConversationID, "generation-nested")
	if _, err := s.LatestConversationTurn(ctx, conversation.ID); !errors.Is(err, domain.ErrNoConversationTurn) {
		t.Fatalf("nested cutoff leaked an ancestor turn: %v", err)
	}
}

// 이슈 #6: provider 가 스스로 연 자율 턴(ports.ProviderInitiatedTurnPrefix)이
// 사용자 턴보다 늦어도 LatestPromptedConversationTurn 은 사용자 턴을 고른다.
// LatestConversationTurn 은 그대로 자율 턴을 최신 턴으로 본다.
func TestLatestPromptedConversationTurnSkipsProviderInitiatedTurns(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	prefix := ports.ProviderInitiatedTurnPrefix
	appendBranchPrompt(t, s, session, conversation, "generation-root", "a", "A")
	if err := s.BindTurnToProvider(ctx, "turn-a", "provider-turn-a", testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleTurn(ctx, conversation.ID, "provider-turn-a", domain.TurnStateCompleted, "", testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	autoID := prefix + "auto-1"
	autoAt := testNow.Add(10 * time.Minute)
	if err := s.AdoptProviderTurn(ctx, conversation.ID, session.ID, "generation-root", "turn-auto", autoID, autoAt); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleTurn(ctx, conversation.ID, autoID, domain.TurnStateCompleted, "", autoAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestConversationTurn(ctx, conversation.ID)
	if err != nil || latest.ID != "turn-auto" {
		t.Fatalf("latest turn = %+v, %v, want the provider-initiated turn", latest, err)
	}
	prompted, err := s.LatestPromptedConversationTurn(ctx, conversation.ID, prefix)
	if err != nil || prompted.ID != "turn-a" || prompted.State != domain.TurnStateCompleted {
		t.Fatalf("latest prompted turn = %+v, %v, want turn-a", prompted, err)
	}

	// 아직 provider id 가 없는 queued 사용자 턴도 프롬프트 턴이다.
	appendBranchPrompt(t, s, session, conversation, "generation-root", "b", "B")
	if err := s.AdoptProviderTurn(ctx, conversation.ID, session.ID, "generation-root", "turn-auto-2", prefix+"auto-2", autoAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	prompted, err = s.LatestPromptedConversationTurn(ctx, conversation.ID, prefix)
	if err != nil || prompted.ID != "turn-b" || prompted.State != domain.TurnStateQueued {
		t.Fatalf("latest prompted turn = %+v, %v, want the queued turn-b", prompted, err)
	}

	// 접두사가 비면 모든 provider 턴을 거르게 되므로 오류로 막는다.
	if _, err := s.LatestPromptedConversationTurn(ctx, conversation.ID, ""); err == nil || errors.Is(err, domain.ErrNoConversationTurn) {
		t.Fatalf("empty prefix: %v, want an error", err)
	}
}

func TestLatestPromptedConversationTurnWithOnlyProviderInitiatedTurns(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	prefix := ports.ProviderInitiatedTurnPrefix
	if _, err := s.LatestPromptedConversationTurn(ctx, conversation.ID, prefix); !errors.Is(err, domain.ErrNoConversationTurn) {
		t.Fatalf("empty conversation: %v", err)
	}
	if err := s.AdoptProviderTurn(ctx, conversation.ID, session.ID, "generation-root", "turn-auto", prefix+"auto-1", testNow); err != nil {
		t.Fatal(err)
	}
	if latest, err := s.LatestConversationTurn(ctx, conversation.ID); err != nil || latest.ID != "turn-auto" {
		t.Fatalf("latest turn = %+v, %v", latest, err)
	}
	if turn, err := s.LatestPromptedConversationTurn(ctx, conversation.ID, prefix); !errors.Is(err, domain.ErrNoConversationTurn) {
		t.Fatalf("only provider-initiated turns: %+v, %v, want ErrNoConversationTurn", turn, err)
	}
	// 접두사가 없는 adopt 턴(예: compaction)은 자율 턴이 아니다.
	if err := s.AdoptProviderTurn(ctx, conversation.ID, session.ID, "generation-root", "turn-compact", "compact-1", testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if turn, err := s.LatestPromptedConversationTurn(ctx, conversation.ID, prefix); err != nil || turn.ID != "turn-compact" {
		t.Fatalf("adopted turn without the prefix = %+v, %v", turn, err)
	}
}
