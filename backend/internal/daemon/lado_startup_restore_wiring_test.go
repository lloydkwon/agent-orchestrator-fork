package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

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

type startupRestoreHolder struct{ hold func() bool }

func (h *startupRestoreHolder) SetLadoRuntimeDeathHold(hold func() bool) { h.hold = hold }

// 데몬은 설정 → 실행의 스위치를 세션 관리자에, 다시 띄운 세션을 워커 감시에,
// 시작 점검이 끝났는지를 런타임 감시기에 잇는다.
func TestWireLadoStartupRestoreConnectsSettingWatchAndReaper(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := &startupRestoreSink{awaiting: true}
	settings := &startupRestoreSettings{settings: domain.DefaultLadoDispatchSettings()}
	listener := &startupRestoreListener{}
	holder := &startupRestoreHolder{}

	wireLadoStartupRestore(sink, settings, listener, holder, log)

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

	if holder.hold == nil || !holder.hold() {
		t.Fatal("시작 점검 전에는 감시기를 기다리게 해야 한다")
	}
	sink.awaiting = false
	if holder.hold() {
		t.Fatal("시작 점검이 끝나면 감시기를 풀어야 한다")
	}
}

// 세션 관리자가 훅을 받지 못하는 구성(다른 부분의 시험)에서는 아무것도
// 설치하지 않는다.
func TestWireLadoStartupRestoreWithoutSessionsInstallsNothing(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	holder := &startupRestoreHolder{}

	wireLadoStartupRestore(nil, &startupRestoreSettings{}, &startupRestoreListener{}, holder, log)

	if holder.hold != nil {
		t.Fatal("세션 관리자가 없는데 감시기 보류를 설치했다")
	}
}
