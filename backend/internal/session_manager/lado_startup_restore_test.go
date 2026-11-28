package sessionmanager

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// rebootChatLauncher 는 재부팅 뒤의 Chat 호스트를 흉내 낸다. 붙기만 하는
// 확인(ReconnectOnly)에는 호스트가 없다고 답하고, 그 밖의 시작은 받는다.
type rebootChatLauncher struct {
	recordingLauncher
	reconnects []ChatStart
}

func (l *rebootChatLauncher) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	if cfg.ReconnectOnly {
		l.reconnects = append(l.reconnects, cfg)
		return ChatStarted{}, ports.ErrChatHostNotRunning
	}
	return l.recordingLauncher.StartChat(ctx, cfg)
}

// restoreRecorder 는 시작 때 다시 띄운 세션과 그 턴이 끊겼는지를 모은다.
type restoreRecorder struct {
	mu       sync.Mutex
	restored map[domain.SessionID]bool
}

func (r *restoreRecorder) record(id domain.SessionID, interrupted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.restored == nil {
		r.restored = map[domain.SessionID]bool{}
	}
	r.restored[id] = interrupted
}

func enableStartupRestore(m *Manager, on bool) *restoreRecorder {
	rec := &restoreRecorder{}
	m.SetLadoStartupRestore(LadoStartupRestore{
		Enabled:  func(context.Context) bool { return on },
		Restored: rec.record,
	})
	return rec
}

func deadTUISession(id domain.SessionID, state domain.ActivityState) domain.SessionRecord {
	return domain.SessionRecord{
		ID: id, ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode,
		Metadata: domain.SessionMetadata{WorkspacePath: "/ws/" + string(id), Branch: "ao/" + string(id) + "/root", RuntimeHandleID: "dead-" + string(id), AgentSessionID: "native-" + string(id)},
		Activity: domain.Activity{State: state},
	}
}

// 재부팅 뒤 런타임이 사라진 세션은, 설정이 켜져 있으면 같은 세션으로 다시
// 뜬다. 종료로 바뀌지 않으므로 앱이 새 오케스트레이터를 만들지 않는다.
func TestStartupRestoreRelaunchesSessionsWhoseAgentIsGone(t *testing.T) {
	ctx := context.Background()
	m, st, rt, ws := newLifecycleManager()
	launcher := &rebootChatLauncher{}
	m.chat = launcher
	restored := enableStartupRestore(m, true)

	orchestrator := deadTUISession("mer-orch", domain.ActivityIdle)
	orchestrator.Kind = domain.KindOrchestrator
	st.sessions[orchestrator.ID] = orchestrator
	st.sessions["mer-busy"] = deadTUISession("mer-busy", domain.ActivityActive)
	st.sessions["mer-chat"] = domain.SessionRecord{
		ID: "mer-chat", ProjectID: "mer", Kind: domain.KindWorker, Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{WorkspacePath: "/ws/chat", Branch: "ao/chat/root", ProviderConversationID: "thread-1"},
		Activity: domain.Activity{State: domain.ActivityActive},
	}

	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	if rt.created != 2 {
		t.Fatalf("TUI 세션 둘을 다시 띄워야 한다: runtimes=%d", rt.created)
	}
	if len(launcher.reconnects) != 1 || len(launcher.started) != 1 {
		t.Fatalf("Chat 세션은 붙기 확인 한 번 뒤 한 번 시작해야 한다: reconnects=%d started=%d", len(launcher.reconnects), len(launcher.started))
	}
	if len(ws.restoreConfigs) != 3 {
		t.Fatalf("세 세션의 작업 폴더를 확인해야 한다: restores=%d", len(ws.restoreConfigs))
	}
	for _, id := range []domain.SessionID{"mer-orch", "mer-busy", "mer-chat"} {
		got := st.sessions[id]
		if got.IsTerminated || got.Activity.State == domain.ActivityExited {
			t.Fatalf("%s 는 살아 있어야 한다: %+v", id, got.Activity)
		}
	}
	if got := st.sessions["mer-orch"].Metadata.RuntimeHandleID; got == "" || got == "dead-mer-orch" {
		t.Fatalf("오케스트레이터가 새 런타임을 받아야 한다: handle=%q", got)
	}
	want := map[domain.SessionID]bool{"mer-orch": false, "mer-busy": true, "mer-chat": true}
	if len(restored.restored) != len(want) {
		t.Fatalf("다시 띄운 세션 알림: got=%v want=%v", restored.restored, want)
	}
	for id, interrupted := range want {
		if got, ok := restored.restored[id]; !ok || got != interrupted {
			t.Fatalf("%s 의 끊긴 턴 표시: got=%v ok=%v want=%v", id, got, ok, interrupted)
		}
	}
}

// 설정이 꺼져 있으면 지금까지처럼 멈춘 채로 두고, 사람이 복원한다.
func TestStartupRestoreOffLeavesSessionsStopped(t *testing.T) {
	m, st, rt, ws := newLifecycleManager()
	restored := enableStartupRestore(m, false)
	st.sessions["mer-tui"] = deadTUISession("mer-tui", domain.ActivityActive)

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	if rt.created != 0 || len(ws.restoreConfigs) != 0 {
		t.Fatalf("꺼져 있는데 다시 띄웠다: runtimes=%d restores=%d", rt.created, len(ws.restoreConfigs))
	}
	if got := st.sessions["mer-tui"]; got.Activity.State != domain.ActivityExited || got.IsTerminated {
		t.Fatalf("멈춘 채 복원할 수 있어야 한다: %+v", got)
	}
	if len(restored.restored) != 0 {
		t.Fatalf("알림이 없어야 한다: %v", restored.restored)
	}
}

// 앱이 꺼지기 전에 이미 에이전트가 끝나 있던 세션, 종료된 세션은 건드리지
// 않는다. 재부팅이 끊은 것만 되살린다.
func TestStartupRestoreSkipsSessionsThatWereAlreadyStopped(t *testing.T) {
	m, st, rt, ws := newLifecycleManager()
	restored := enableStartupRestore(m, true)
	st.sessions["mer-exited"] = deadTUISession("mer-exited", domain.ActivityExited)
	killed := deadTUISession("mer-killed", domain.ActivityExited)
	killed.IsTerminated = true
	st.sessions[killed.ID] = killed

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	if rt.created != 0 || len(ws.restoreConfigs) != 0 || len(restored.restored) != 0 {
		t.Fatalf("이미 멈춘 세션을 띄웠다: runtimes=%d restores=%d restored=%v", rt.created, len(ws.restoreConfigs), restored.restored)
	}
	if !st.sessions["mer-killed"].IsTerminated || st.sessions["mer-exited"].Activity.State != domain.ActivityExited {
		t.Fatal("이미 멈춘 세션의 상태가 바뀌었다")
	}
}

// 런타임 확인이 실패한 것은 죽었다는 증거가 아니다(AGENTS.md). 그대로 둔다.
func TestStartupRestoreDoesNotActOnAFailedProbe(t *testing.T) {
	m, st, rt, ws := newLifecycleManager()
	restored := enableStartupRestore(m, true)
	rt.aliveErr = ports.ErrRuntimeUnavailable
	rec := deadTUISession("mer-tui", domain.ActivityActive)
	st.sessions[rec.ID] = rec

	err := m.checkSessionHealth(context.Background(), rec)

	if !errors.Is(err, ports.ErrRuntimeUnavailable) {
		t.Fatalf("error=%v, want runtime unavailable", err)
	}
	if st.sessions[rec.ID].Activity.State != domain.ActivityActive || rt.created != 0 || len(ws.restoreConfigs) != 0 || len(restored.restored) != 0 {
		t.Fatal("확인이 실패했는데 세션을 바꾸거나 띄웠다")
	}
}

// LADO 가 영영 멈춘 실행(시간 초과 등)은 게이트가 막고, 멈춘 채로 남는다.
func TestStartupRestoreAsksTheWorkGate(t *testing.T) {
	m, st, rt, _ := newLifecycleManager()
	restored := enableStartupRestore(m, true)
	gate := &recordingWorkGate{restoreErr: errors.New("LADO stopped this execution")}
	m.SetWorkGate(gate)
	st.sessions["mer-tui"] = deadTUISession("mer-tui", domain.ActivityActive)

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(gate.restores) != 1 || gate.restores[0] != "mer-tui" {
		t.Fatalf("게이트에 물어야 한다: %v", gate.restores)
	}
	if rt.created != 0 || len(restored.restored) != 0 {
		t.Fatalf("게이트가 막았는데 띄웠다: runtimes=%d restored=%v", rt.created, restored.restored)
	}
	if got := st.sessions["mer-tui"]; got.Activity.State != domain.ActivityExited || got.IsTerminated {
		t.Fatalf("멈춘 채 남아야 한다: %+v", got)
	}
}

// 다시 띄우다 실패하면 세션을 종료하지 않고 멈춘 상태로 남겨, 사람이 Resume
// 으로 다시 시도할 수 있게 한다. 알림은 가지 않는다.
func TestStartupRestoreFailureLeavesSessionResumable(t *testing.T) {
	m, st, rt, _ := newLifecycleManager()
	restored := enableStartupRestore(m, true)
	rt.createErr = errors.New("tmux is not on PATH")
	st.sessions["mer-tui"] = deadTUISession("mer-tui", domain.ActivityActive)

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := st.sessions["mer-tui"]
	if got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("실패한 세션은 멈춘 채 복원할 수 있어야 한다: %+v", got)
	}
	if len(restored.restored) != 0 {
		t.Fatalf("실패했는데 알렸다: %v", restored.restored)
	}
}

// 런타임 감시기는 5초마다 죽은 세션을 종료로 바꾼다. 시작 점검이 세션을
// 다시 띄우기 전에 감시기가 먼저 돌면 복원을 놓치므로, 점검이 끝날 때까지는
// 감시기가 종료 처리를 미루게 알려 준다. 기능을 쓰지 않으면 미루지 않는다.
func TestStartupRestoreTellsTheReaperToWaitForTheHealthPass(t *testing.T) {
	m, st, _, _ := newLifecycleManager()
	st.sessions["mer-tui"] = deadTUISession("mer-tui", domain.ActivityActive)
	if m.AwaitingStartupRestore() {
		t.Fatal("기능이 없을 때는 감시기를 기다리게 하지 않아야 한다")
	}

	enableStartupRestore(m, true)
	if !m.AwaitingStartupRestore() {
		t.Fatal("시작 점검 전에는 감시기가 기다려야 한다")
	}

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.AwaitingStartupRestore() {
		t.Fatal("시작 점검이 끝나면 감시기가 다시 평소대로 돌아야 한다")
	}
}
