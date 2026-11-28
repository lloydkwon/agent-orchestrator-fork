package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// startupRestoreSink 는 세션 관리자를 대신해 설치된 복원 훅을 붙잡아 둔다.
type startupRestoreSink struct {
	restore  sessionmanager.LadoStartupRestore
	awaiting bool
}

func (s *startupRestoreSink) SetLadoStartupRestore(r sessionmanager.LadoStartupRestore) {
	s.restore = r
}
func (s *startupRestoreSink) AwaitingStartupRestore() bool { return s.awaiting }

type startupRestoreSettings struct {
	settings domain.LadoDispatchSettings
	err      error
}

func (s *startupRestoreSettings) Settings(context.Context) (domain.LadoDispatchSettings, error) {
	return s.settings, s.err
}

type startupRestoreListener struct {
	heard map[domain.SessionID]bool
}

func (l *startupRestoreListener) SessionRestored(id domain.SessionID, interrupted bool) {
	if l.heard == nil {
		l.heard = map[domain.SessionID]bool{}
	}
	l.heard[id] = interrupted
}

// startupRestoreExecutions 는 LADO 실행 기록을 대신한다.
type startupRestoreExecutions struct {
	execs map[domain.SessionID]domain.LadoExecution
	err   error
}

func (e *startupRestoreExecutions) LadoExecution(_ context.Context, id domain.SessionID) (domain.LadoExecution, bool, error) {
	if e.err != nil {
		return domain.LadoExecution{}, false, e.err
	}
	exec, ok := e.execs[id]
	return exec, ok, nil
}

var startupRestoreNow = time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// 데몬은 설정 → 실행의 스위치를 세션 관리자에, 다시 띄운 세션을 워커 감시에,
// 시작 점검이 끝났는지를 런타임 감시기에 잇는다.
func TestWireLadoStartupRestoreConnectsSettingWatchAndReaper(t *testing.T) {
	sink := &startupRestoreSink{awaiting: true}
	settings := &startupRestoreSettings{settings: domain.DefaultLadoDispatchSettings()}
	listener := &startupRestoreListener{}
	hold := newLadoStartupHold(func() time.Time { return startupRestoreNow })

	wireLadoStartupRestore(sink, settings, listener, &startupRestoreExecutions{}, hold, quietLog())

	ctx := context.Background()
	if sink.restore.Enabled == nil || !sink.restore.Enabled(ctx) {
		t.Fatal("기본 설정에서는 복원이 켜져 있어야 한다")
	}
	settings.settings.RestoreOnStart = domain.LadoRestoreOnStartOff
	if sink.restore.Enabled(ctx) {
		t.Fatal("스위치를 끄면 복원하지 않아야 한다")
	}
	// 설정을 읽지 못하면 아무것도 띄우지 않는다(지금까지의 동작).
	settings.settings.RestoreOnStart, settings.err = domain.LadoRestoreOnStartOn, errors.New("database is locked")
	if sink.restore.Enabled(ctx) {
		t.Fatal("설정을 읽지 못했는데 복원한다")
	}

	sink.restore.Restored("mer-1", true)
	sink.restore.Restored("mer-2", false)
	if len(listener.heard) != 2 || !listener.heard["mer-1"] || listener.heard["mer-2"] {
		t.Fatalf("워커 감시가 들은 것: %v", listener.heard)
	}

	if !hold.held() {
		t.Fatal("시작 점검 전에는 감시기를 기다리게 해야 한다")
	}
	sink.awaiting = false
	if hold.held() {
		t.Fatal("시작 점검이 끝나면 감시기를 풀어야 한다")
	}
}

// 세션 관리자가 훅을 받지 못하는 구성(다른 부분의 시험)에서는 감시기를
// 기다리게 하지 않는다.
func TestWireLadoStartupRestoreWithoutSessionsHoldsNothing(t *testing.T) {
	hold := newLadoStartupHold(func() time.Time { return startupRestoreNow })

	wireLadoStartupRestore(nil, &startupRestoreSettings{}, &startupRestoreListener{}, &startupRestoreExecutions{}, hold, quietLog())

	if hold.held() {
		t.Fatal("세션 관리자가 없는데 감시기를 기다리게 한다")
	}
}

// 감시기는 데몬이 뜨자마자 5초마다 돈다. 세션 관리자가 연결되기 전에도
// 기다리게 하고, 시작 점검이 끝내 알려 오지 않아도 상한이 지나면 풀어 준다.
func TestLadoStartupHoldCoversTheGapBeforeWiringAndExpires(t *testing.T) {
	now := startupRestoreNow
	hold := newLadoStartupHold(func() time.Time { return now })

	if !hold.held() {
		t.Fatal("연결 전에도 감시기를 기다리게 해야 한다")
	}
	hold.bind(func() bool { return true })
	now = startupRestoreNow.Add(ladoStartupHoldMax - time.Second)
	if !hold.held() {
		t.Fatal("상한 안에서는 시작 점검을 기다려야 한다")
	}
	now = startupRestoreNow.Add(ladoStartupHoldMax + time.Second)
	if hold.held() {
		t.Fatal("상한이 지나면 감시기를 풀어야 한다")
	}

	unbound := newLadoStartupHold(func() time.Time { return now })
	now = now.Add(ladoStartupHoldMax + time.Second)
	if unbound.held() {
		t.Fatal("끝내 연결되지 않아도 상한이 지나면 풀어야 한다")
	}
}

// 마감이 지났는데 PR 이 없는 실행은 LADO 가 곧 멈출 실행이다. 데몬이 꺼져
// 있는 동안 마감이 지난 실행을 다시 띄우지 않는다.
func TestLadoStartupRestoreRefusesAnExecutionPastItsDeadline(t *testing.T) {
	sink := &startupRestoreSink{}
	executions := &startupRestoreExecutions{execs: map[domain.SessionID]domain.LadoExecution{
		"overdue":  {SessionID: "overdue", DeadlineAt: startupRestoreNow.Add(-time.Hour)},
		"with-pr":  {SessionID: "with-pr", DeadlineAt: startupRestoreNow.Add(-time.Hour), PR: domain.LadoExecutionPR{URL: "https://example.test/pr/1"}},
		"in-time":  {SessionID: "in-time", DeadlineAt: startupRestoreNow.Add(time.Hour)},
		"no-limit": {SessionID: "no-limit"},
	}}
	hold := newLadoStartupHold(func() time.Time { return startupRestoreNow })
	wireLadoStartupRestore(sink, &startupRestoreSettings{settings: domain.DefaultLadoDispatchSettings()}, &startupRestoreListener{}, executions, hold, quietLog())

	ctx := context.Background()
	if reason := sink.restore.Refuse(ctx, "overdue"); reason == "" {
		t.Fatal("마감이 지난 실행을 막아야 한다")
	}
	for _, id := range []domain.SessionID{"with-pr", "in-time", "no-limit", "not-an-execution"} {
		if reason := sink.restore.Refuse(ctx, id); reason != "" {
			t.Fatalf("%s 를 막았다: %s", id, reason)
		}
	}
	// 실행 기록을 읽지 못하면 띄우지 않는다.
	executions.err = errors.New("database is locked")
	if reason := sink.restore.Refuse(ctx, "in-time"); reason == "" {
		t.Fatal("실행 기록을 읽지 못했는데 띄운다")
	}
}
