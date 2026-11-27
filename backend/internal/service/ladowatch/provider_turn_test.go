package ladowatch

// 이슈 #6: provider 가 사용자 프롬프트 없이 스스로 연 자율 턴
// (ports.ProviderInitiatedTurnPrefix, 예: Claude Code 의 task-notification
// 사이클)은 대화의 최신 턴이 된다. LADO 는 한도와 소유자의 Stop 을 최신
// 프롬프트 턴으로 판정하고, 쉼과 조용해진 시각은 자율 턴을 포함한 최신
// 턴으로 본다.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// providerTurn adds a turn the provider opened on its own to the worker,
// started a minute before at. A running one has not ended; any other ended at
// at.
func (f *fixture) providerTurn(state domain.TurnState, at time.Time) {
	started := at.Add(-time.Minute)
	turn := domain.ConversationTurn{
		ID: "auto-" + at.Format(time.RFC3339Nano), State: state, RequestedAt: started, StartedAt: &started,
	}
	if state.Terminal() {
		done := at
		turn.CompletedAt = &done
	}
	f.convs.addProviderTurn(worker1, turn)
}

// 한도로 멈춘 워커에서 task-notification 사이클 하나가 completed 로 끝나도
// 한도는 풀리지 않고, 계정 한도도 해제되지 않는다. 재설정 뒤에는 원래대로
// 재개한다.
func TestProviderTurnDoesNotLiftTheLimit(t *testing.T) {
	f := newFixture(t)
	f.limitAt(t0, sessionLimit)
	f.tick(t0.Add(time.Minute))
	key := "claude:device-claude"
	if l, ok := f.store.limit(key); !ok || !l.Active(t0.Add(time.Minute)) {
		t.Fatalf("account limit = %+v, %v", l, ok)
	}

	auto := t0.Add(30 * time.Minute)
	f.providerTurn(domain.TurnStateCompleted, auto)
	f.rest(auto)
	f.tick(auto.Add(time.Minute))
	if w := f.watch(); w.LimitState != domain.LadoLimitLimited || !w.LimitSeenAt.Equal(t0) {
		t.Fatalf("a provider-initiated turn lifted the limit: %+v", w.LadoWatchLimit)
	}
	if l, ok := f.store.limit(key); !ok || l.Released() || !l.Active(auto.Add(time.Minute)) {
		t.Fatalf("a provider-initiated turn released the account's limit: %+v", l)
	}
	if _, ok, _ := f.svc.LaunchAccountLimit(context.Background(), domain.HarnessClaudeCode, auto.Add(time.Minute)); !ok {
		t.Fatal("new workers start on an account still at its limit")
	}
	if len(f.msgs.texts()) != 0 {
		t.Fatalf("sent %v before the reset", f.msgs.texts())
	}

	f.tick(reset.Add(domain.LadoWakeResumeDelay))
	if !slices.Equal(f.msgs.texts(), []string{domain.LadoWakeLimitResumeText}) {
		t.Fatalf("sent %v at the reset, want one resume", f.msgs.texts())
	}
}

// 소유자가 사용자 턴을 Stop(interrupted)하거나 거둔(cancelled) 뒤에 자율 턴이
// 와도, LADO 는 그 워커를 재촉하지 않고 알림만 올린다.
func TestProviderTurnAfterTheOwnerStoppedSendsNoNudge(t *testing.T) {
	for _, state := range []domain.TurnState{domain.TurnStateInterrupted, domain.TurnStateCancelled} {
		f := newFixture(t)
		f.endTurn(state, "", t0)
		f.rest(t0)
		auto := t0.Add(10 * time.Minute)
		f.providerTurn(domain.TurnStateCompleted, auto)
		f.rest(auto)
		stall := domain.LadoWatchDefaults().Stall()
		f.tick(auto.Add(stall))
		f.tick(auto.Add(stall + time.Hour))
		if len(f.msgs.texts()) != 0 {
			t.Fatalf("%s: sent %v to a worker the owner stopped", state, f.msgs.texts())
		}
		if !slices.Equal(f.store.alertKinds(), []domain.LadoAlertKind{domain.LadoAlertStalled}) {
			t.Fatalf("%s: alerts = %v, want one stall alert", state, f.store.alertKinds())
		}
	}
}

// 소유자가 자율 턴을 Stop 해도 소유자가 멈춘 것이다: 재촉하지 않고, 한도로
// 멈춘 워커는 재설정 뒤에도 재개하지 않는다. 그 Stop 은 한도를 풀지 않는다.
func TestStoppedProviderTurnIsTheOwnerStopping(t *testing.T) {
	t.Run("stall", func(t *testing.T) {
		f := newFixture(t)
		auto := t0.Add(10 * time.Minute)
		f.providerTurn(domain.TurnStateInterrupted, auto)
		f.rest(auto)
		stall := domain.LadoWatchDefaults().Stall()
		f.tick(auto.Add(stall))
		f.tick(auto.Add(stall + time.Hour))
		if len(f.msgs.texts()) != 0 {
			t.Fatalf("sent %v to a worker whose provider turn the owner stopped", f.msgs.texts())
		}
		if !slices.Equal(f.store.alertKinds(), []domain.LadoAlertKind{domain.LadoAlertStalled}) {
			t.Fatalf("alerts = %v, want one stall alert", f.store.alertKinds())
		}
	})
	t.Run("limit", func(t *testing.T) {
		f := newFixture(t)
		f.limitAt(t0, sessionLimit)
		f.tick(t0.Add(time.Minute))
		auto := t0.Add(30 * time.Minute)
		f.providerTurn(domain.TurnStateInterrupted, auto)
		f.rest(auto)
		f.tick(auto.Add(time.Minute))
		if w := f.watch(); w.LimitState != domain.LadoLimitLimited {
			t.Fatalf("a stopped provider turn lifted the limit: %+v", w.LadoWatchLimit)
		}
		due := reset.Add(domain.LadoWakeResumeDelay)
		f.tick(due)
		f.tick(due.Add(time.Minute))
		if len(f.msgs.texts()) != 0 {
			t.Fatalf("resumed a worker whose provider turn the owner stopped: %v", f.msgs.texts())
		}
	})
}

// 자율 턴이 도는 동안 워커는 쉬지 않는다: AO 가 idle 로 보여도 멈춤이 아니다.
func TestRunningProviderTurnIsNotResting(t *testing.T) {
	f := newFixture(t)
	f.providerTurn(domain.TurnStateRunning, t0.Add(10*time.Minute))
	stall := domain.LadoWatchDefaults().Stall()
	f.tick(t0.Add(stall))
	f.tick(t0.Add(stall + 3*time.Hour))
	if len(f.msgs.texts()) != 0 || f.watch().StallState != domain.LadoStallNone {
		t.Fatalf("a worker in a running provider turn stalled: sent %v, watch %+v", f.msgs.texts(), f.watch().LadoWatchStall)
	}
	if len(f.store.alerts) != 0 {
		t.Fatalf("alerts = %v", f.store.alertKinds())
	}
}

// 멈춤은 자율 턴의 끝에서 센다. AO 의 활동 시각이 마지막 사용자 턴에 머물러
// 있어도, 자율 턴의 끝 + stallMinutes 전에는 멈춤이 아니고 그 뒤에는 멈춤이다.
func TestStallCountsFromTheProviderTurnEnd(t *testing.T) {
	f := newFixture(t)
	autoEnd := t0.Add(90 * time.Minute)
	f.providerTurn(domain.TurnStateCompleted, autoEnd)
	stall := domain.LadoWatchDefaults().Stall()
	f.tick(t0.Add(stall))
	f.tick(autoEnd.Add(stall - time.Minute))
	if len(f.msgs.texts()) != 0 || f.watch().StallState != domain.LadoStallNone {
		t.Fatalf("stalled before the provider turn's end + stall: sent %v, watch %+v", f.msgs.texts(), f.watch().LadoWatchStall)
	}
	f.tick(autoEnd.Add(stall))
	if !slices.Equal(f.msgs.texts(), []string{domain.LadoWakeStallNudgeText}) {
		t.Fatalf("sent %v at the provider turn's end + stall, want one nudge", f.msgs.texts())
	}
	if w := f.watch(); w.StallState != domain.LadoStallStalled || w.Nudges != 1 {
		t.Fatalf("stall watch = %+v", w.LadoWatchStall)
	}
}
