package daemon

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type recordingModeHook struct {
	fn func(domain.ProjectID, bool)
}

func (h *recordingModeHook) SetOnLadoModeChanged(fn func(domain.ProjectID, bool)) { h.fn = fn }

type recordingRefresher struct {
	mu        sync.Mutex
	noted     []domain.ProjectID
	refreshes chan struct{}
}

func (r *recordingRefresher) NoteLadoModeChanged(projectID domain.ProjectID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noted = append(r.noted, projectID)
}

func (r *recordingRefresher) RefreshPendingInstructions(context.Context) (int, error) {
	r.refreshes <- struct{}{}
	return 0, nil
}

func TestLadoModeChangeQueuesAndKicksTheInstructionRefresh(t *testing.T) {
	hook := &recordingModeHook{}
	refresher := &recordingRefresher{refreshes: make(chan struct{}, 4)}
	run := wireLadoInstructionRefresh(hook, refresher, slog.New(slog.DiscardHandler))
	if run == nil || hook.fn == nil {
		t.Fatalf("wiring = run %v, hook %v; want both set", run != nil, hook.fn != nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	hook.fn("proj", true)
	select {
	case <-refresher.refreshes:
	case <-time.After(ladoInstructionRefreshTick / 2):
		t.Fatal("a mode change did not try the queued orchestrators before the next tick")
	}
	refresher.mu.Lock()
	noted := append([]domain.ProjectID(nil), refresher.noted...)
	refresher.mu.Unlock()
	if len(noted) != 1 || noted[0] != "proj" {
		t.Fatalf("noted = %v, want [proj]", noted)
	}
}

func TestLadoInstructionRefreshNeedsASessionManagerThatRelaunches(t *testing.T) {
	hook := &recordingModeHook{}
	if run := wireLadoInstructionRefresh(hook, struct{}{}, slog.New(slog.DiscardHandler)); run != nil || hook.fn != nil {
		t.Fatalf("wiring without a refresher = run %v, hook %v; want neither", run != nil, hook.fn != nil)
	}
}
