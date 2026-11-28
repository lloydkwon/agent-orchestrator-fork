package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// LADO fork (2026-11-27): the provider's background-task notices land in
// lado_session_background, an empty list as known-none.
func TestBackgroundTaskNoticesRecordTheSessionsList(t *testing.T) {
	h := newHarnessForHarness(t, domain.HarnessClaudeCode)
	started := time.Date(2026, 11, 27, 3, 1, 8, 0, time.UTC)
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventBackgroundTasks, BackgroundTasks: []domain.BackgroundTask{
		{Type: "shell", Description: "wait_event.sh 7000", StartedAt: started},
	}})
	got := awaitBackground(t, h, func(b domain.SessionBackgroundTasks) bool { return b.Running() })
	if got.Tasks[0].Description != "wait_event.sh 7000" || !got.Since().Equal(started) {
		t.Fatalf("recorded %+v", got)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventBackgroundTasks})
	awaitBackground(t, h, func(b domain.SessionBackgroundTasks) bool { return !b.Running() })
}

// backgroundReader is a provider that answers the start-up background read.
type backgroundReader struct {
	*fakeConversation
	tasks []domain.BackgroundTask
	known bool
}

func (r backgroundReader) BackgroundTasks(context.Context) ([]domain.BackgroundTask, bool, error) {
	return r.tasks, r.known, nil
}

// A restarted daemon reads the list once when the controller starts: the
// provider kept running while the daemon missed its notices.
func TestControllerStartReadsTheProvidersBackgroundTasks(t *testing.T) {
	conv := backgroundReader{fakeConversation: newFakeConversation(), known: true,
		tasks: []domain.BackgroundTask{{Type: "monitor", Description: "until done"}}}
	h := newHarnessWithConversation(t, conv)
	got := awaitBackground(t, h, func(b domain.SessionBackgroundTasks) bool { return b.Running() })
	if got.Tasks[0].Type != "monitor" {
		t.Fatalf("recorded %+v", got)
	}
}

// A provider that cannot tell leaves AO not knowing.
func TestControllerStartForgetsWhatAnOlderBridgeCannotTell(t *testing.T) {
	h := newHarnessWithConversation(t, backgroundReader{fakeConversation: newFakeConversation()})
	// The read runs off the critical path; give it time, then expect no row.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok, err := h.st.LadoSessionBackground(context.Background(), testSession); err != nil || ok {
			t.Fatalf("known=%v err=%v, want unknown", ok, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func awaitBackground(t *testing.T, h *harness, pred func(domain.SessionBackgroundTasks) bool) domain.SessionBackgroundTasks {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, ok, err := h.st.LadoSessionBackground(context.Background(), testSession)
		if err != nil {
			t.Fatal(err)
		}
		if ok && pred(b) {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("background tasks = %+v known=%v; condition not met", b, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
