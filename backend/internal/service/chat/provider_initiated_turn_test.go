package chat

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// 이 파일은 provider 가 사용자 턴 밖에서 스스로 연 턴(자율 턴, 예: Claude Code 의
// <task-notification> 사이클)을 controller 가 어떻게 받는지 본다. ACP 드라이버는 이런 턴에
// ports.ProviderInitiatedTurnPrefix 로 시작하는 id 를 붙이고, ControllerState 없이
// TurnStarted 와 TurnCompleted 만 낸다.
//
// dispatch 중의 묶임은 live 투영 루프로는 닿지 않는 창이라(sendMu 가 dispatch 와 투영을
// 직렬로 만든다) 패키지 안에서 projectEvent 를 직접 부른다. 그래서 이 파일은 package chat 이다.

const (
	autonomousTestProject = domain.ProjectID("p1")
	autonomousTestSession = domain.SessionID("p1-1")
	autonomousTurnID      = ports.ProviderInitiatedTurnPrefix + "cycle-1"
	autonomousThreadID    = "thread-1"
)

/* ---- 자율 턴을 흘리는 provider 대역 ---------------------------------------- */

type autonomousTurnConversation struct {
	events chan ports.ChatEvent
	// live 가 참이면 이 대화는 살아 있는 provider 에 다시 붙은 것이다(live reconnect).
	live bool

	mu        sync.Mutex
	sent      []string
	turnSeq   int
	onSend    func(providerTurnID string)
	closeOnce sync.Once
}

func newAutonomousTurnConversation(live bool) *autonomousTurnConversation {
	return &autonomousTurnConversation{events: make(chan ports.ChatEvent, 64), live: live}
}

func (c *autonomousTurnConversation) ProviderConversationID() string { return autonomousThreadID }

func (c *autonomousTurnConversation) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true,
		ports.ChatCapabilityApprovals: true,
		ports.ChatCapabilityInterrupt: true,
		ports.ChatCapabilityResume:    true,
	}
}

func (c *autonomousTurnConversation) SendTurn(_ context.Context, msg ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	c.mu.Lock()
	c.sent = append(c.sent, msg.Text)
	c.turnSeq++
	providerTurnID := fmt.Sprintf("provider-turn-%d", c.turnSeq)
	onSend := c.onSend
	c.mu.Unlock()
	if onSend != nil {
		onSend(providerTurnID)
	}
	return ports.ChatTurnRef{ProviderTurnID: providerTurnID}, nil
}

func (c *autonomousTurnConversation) Interrupt(context.Context, string) error { return nil }

func (c *autonomousTurnConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return nil
}

func (c *autonomousTurnConversation) Events() <-chan ports.ChatEvent { return c.events }

func (c *autonomousTurnConversation) Close() error {
	c.closeOnce.Do(func() { close(c.events) })
	return nil
}

// ReconnectedLive 는 ports.ChatLiveReconnector 다.
func (c *autonomousTurnConversation) ReconnectedLive() bool { return c.live }

// PreservesProviderOnClose 는 persistent host 처럼 닫을 때 provider 와 진행 중인 턴을 남긴다.
// 그래야 첫 controller 를 멈춘 뒤에도 running 자율 행이 남아 live reconnect 를 볼 수 있다.
func (c *autonomousTurnConversation) PreservesProviderOnClose() bool { return true }

func (c *autonomousTurnConversation) setOnSend(fn func(providerTurnID string)) {
	c.mu.Lock()
	c.onSend = fn
	c.mu.Unlock()
}

func (c *autonomousTurnConversation) sentTexts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sent...)
}

func (c *autonomousTurnConversation) emit(events ...ports.ChatEvent) {
	for _, event := range events {
		c.events <- event
	}
}

type autonomousTurnDriver struct{ conv ports.ChatConversation }

func (d autonomousTurnDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }
func (d autonomousTurnDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return d.conv.Capabilities(), nil
}
func (d autonomousTurnDriver) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.conv, nil
}
func (d autonomousTurnDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return d.conv, nil
}
func (d autonomousTurnDriver) Driver(domain.AgentHarness) (ports.ChatDriver, error) { return d, nil }
func (d autonomousTurnDriver) SupportsChat(domain.AgentHarness) bool                { return true }

/* ---- 실제 SQLite 와 실제 lifecycle 을 쓰는 하네스 -------------------------------- */

type autonomousTurnHarness struct {
	st   *sqlite.Store
	svc  *Service
	ctrl *Controller
	conv *autonomousTurnConversation

	ids     atomic.Int64
	clockMu sync.Mutex
	clock   time.Time
}

func newAutonomousTurnHarness(t *testing.T) *autonomousTurnHarness {
	t.Helper()
	dir := t.TempDir()
	st := sqlitetest.MustOpenAt(t, dir)
	ctx := context.Background()
	if err := st.UpsertProject(ctx, domain.ProjectRecord{
		ID: string(autonomousTestProject), Path: dir, RegisteredAt: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := st.CreateSession(ctx, domain.SessionRecord{
		ID: autonomousTestSession, ProjectID: autonomousTestProject, Kind: domain.KindOrchestrator,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return &autonomousTurnHarness{st: st, clock: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)}
}

func (h *autonomousTurnHarness) now() time.Time {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	return h.clock
}

// advance 는 시험 시계를 움직이고 움직인 뒤의 시각을 돌려준다.
func (h *autonomousTurnHarness) advance(d time.Duration) time.Time {
	h.clockMu.Lock()
	defer h.clockMu.Unlock()
	h.clock = h.clock.Add(d)
	return h.clock
}

// start 는 conv 로 controller 를 띄운다. 활동은 실제 lifecycle.Manager 가 받는다.
func (h *autonomousTurnHarness) start(t *testing.T, conv *autonomousTurnConversation, providerConversationID string) {
	t.Helper()
	st := h.st
	svc := New(Options{
		Store: st,
		Reader: SnapshotReaderFunc(func(ctx context.Context, conversationID string) (ConversationRows, error) {
			rows, err := st.LoadConversationSnapshot(ctx, conversationID)
			if err != nil {
				return ConversationRows{}, err
			}
			return ConversationRows{
				Conversation: rows.Conversation, ActiveBranch: rows.ActiveBranch, Turns: rows.Turns,
				Messages: rows.Messages, Activities: rows.Activities,
				BranchPoints: rows.BranchPoints, BranchedFromEarlierMessage: rows.BranchedFromEarlierMessage,
			}, nil
		}),
		Sessions: st,
		Drivers:  autonomousTurnDriver{conv: conv},
		Activity: lifecycle.New(st, nil),
		Log:      slog.New(slog.DiscardHandler),
		NewID:    func() string { return fmt.Sprintf("auto-id-%03d", h.ids.Add(1)) },
		Now:      h.now,
	})
	ctrl, err := svc.Start(context.Background(), StartConfig{
		SessionID: autonomousTestSession, ProjectID: autonomousTestProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: providerConversationID,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { svc.StopAll(context.Background()) })
	h.svc, h.ctrl, h.conv = svc, ctrl, conv
}

func (h *autonomousTurnHarness) turns(t *testing.T) []domain.ConversationTurn {
	t.Helper()
	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	return snapshot.Turns
}

func (h *autonomousTurnHarness) messages(t *testing.T) []domain.ConversationMessage {
	t.Helper()
	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	return snapshot.Messages
}

func turnByProviderID(turns []domain.ConversationTurn, providerTurnID string) (domain.ConversationTurn, bool) {
	for _, turn := range turns {
		if turn.ProviderTurnID == providerTurnID {
			return turn, true
		}
	}
	return domain.ConversationTurn{}, false
}

func turnByID(turns []domain.ConversationTurn, id string) (domain.ConversationTurn, bool) {
	for _, turn := range turns {
		if turn.ID == id {
			return turn, true
		}
	}
	return domain.ConversationTurn{}, false
}

// awaitTurnState 는 providerTurnID 의 행이 state 가 될 때까지 기다린다.
func (h *autonomousTurnHarness) awaitTurnState(t *testing.T, providerTurnID string, state domain.TurnState) domain.ConversationTurn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []domain.ConversationTurn
	for time.Now().Before(deadline) {
		last = h.turns(t)
		if turn, ok := turnByProviderID(last, providerTurnID); ok && turn.State == state {
			return turn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("provider turn %s 의 행이 %s 가 되지 않았다; 마지막 행 = %+v", providerTurnID, state, last)
	return domain.ConversationTurn{}
}

// awaitActivity 는 세션 활동이 pred 를 만족할 때까지 기다린다. 끝내 만족하지 않으면 마지막 값과
// false 를 돌려주어, 부르는 쪽이 assertion 으로 실패를 적게 한다.
func (h *autonomousTurnHarness) awaitActivity(t *testing.T, pred func(domain.Activity) bool) (domain.Activity, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last domain.Activity
	for time.Now().Before(deadline) {
		rec, found, err := h.st.GetSession(context.Background(), autonomousTestSession)
		if err != nil || !found {
			t.Fatalf("read session: found=%v err=%v", found, err)
		}
		last = rec.Activity
		if pred(last) {
			return last, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last, false
}

// awaitProjectionIdle 은 투영 루프가 지금 처리 중인 lifecycle 이벤트(afterProject 까지)를
// 끝낼 때까지 기다린다. 루프는 lifecycle 이벤트를 sendMu 를 쥔 채 처리한다.
func (h *autonomousTurnHarness) awaitProjectionIdle() {
	h.ctrl.sendMu.Lock()
	h.ctrl.sendMu.Unlock() //nolint:staticcheck // 잡았다 놓는 것 자체가 장벽이다.
}

func activityIs(state domain.ActivityState) func(domain.Activity) bool {
	return func(activity domain.Activity) bool { return activity.State == state }
}

/* ---- 시험 ------------------------------------------------------------------ */

// 자율 턴은 Busy 를 세우지 않고 자기 행으로 기록되며, 그 시작과 끝이 세션 활동이 된다.
func TestProviderInitiatedTurnRecordsActivityWithoutBusy(t *testing.T) {
	h := newAutonomousTurnHarness(t)
	h.start(t, newAutonomousTurnConversation(false), "")
	ctx := context.Background()

	startedAt := h.advance(time.Minute)
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: autonomousTurnID})
	h.awaitTurnState(t, autonomousTurnID, domain.TurnStateRunning)
	h.awaitProjectionIdle()
	if activity, ok := h.awaitActivity(t, activityIs(domain.ActivityActive)); !ok {
		t.Errorf("자율 TurnStarted 뒤 세션 활동 = %+v, active 여야 한다", activity)
	}
	if got := h.ctrl.State(); got == ports.ChatControllerBusy {
		t.Errorf("자율 턴이 열린 동안 controller 상태 = %q, busy 가 아니어야 한다", got)
	}

	// 워커가 이 턴 안에서 ao report --checkpoint 를 했다고 본다.
	checkpointAt := h.advance(30 * time.Second)
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventMessageCompleted, ProviderTurnID: autonomousTurnID,
		ProviderItemID: "cycle-1-answer", Text: "백그라운드 작업 결과를 정리했다",
	})
	completedAt := h.advance(30 * time.Second)
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: autonomousTurnID, TurnState: domain.TurnStateCompleted,
	})
	h.awaitTurnState(t, autonomousTurnID, domain.TurnStateCompleted)
	h.awaitProjectionIdle()
	activity, ok := h.awaitActivity(t, func(activity domain.Activity) bool {
		return activity.State == domain.ActivityIdle && activity.LastActivityAt.Equal(completedAt)
	})
	if !ok {
		t.Errorf("자율 TurnCompleted 뒤 세션 활동 = %+v, idle 이고 LastActivityAt 이 %s 여야 한다", activity, completedAt)
	}

	latest, err := h.st.LatestConversationTurn(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("LatestConversationTurn: %v", err)
	}
	if latest.ProviderTurnID != autonomousTurnID || latest.State != domain.TurnStateCompleted {
		t.Errorf("최신 턴 = %+v, 자율 턴 %s 이 completed 로 와야 한다", latest, autonomousTurnID)
	}
	if latest.RequestedAt.After(checkpointAt) {
		t.Errorf("자율 턴 RequestedAt = %s, 같은 턴 안의 checkpoint 시각 %s 보다 늦으면 안 된다", latest.RequestedAt, checkpointAt)
	}
	if !latest.RequestedAt.Equal(startedAt) {
		t.Errorf("자율 턴 RequestedAt = %s, TurnStarted 시각 %s 여야 한다", latest.RequestedAt, startedAt)
	}
	if got := h.ctrl.State(); got == ports.ChatControllerBusy {
		t.Errorf("자율 턴이 끝난 뒤 controller 상태 = %q, busy 가 아니어야 한다", got)
	}
}

// 자율 턴이 열린 동안 보낸 사용자 메시지는 큐에 남지 않고 바로 provider 로 간다. 자율 턴의 끝은
// 그 사용자 턴의 소유를 풀지 않는다.
func TestProviderInitiatedTurnDoesNotQueueUserMessage(t *testing.T) {
	h := newAutonomousTurnHarness(t)
	h.start(t, newAutonomousTurnConversation(false), "")
	ctx := context.Background()

	h.advance(time.Minute)
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: autonomousTurnID})
	h.awaitTurnState(t, autonomousTurnID, domain.TurnStateRunning)
	h.awaitProjectionIdle()

	h.advance(10 * time.Second)
	const text = "자율 턴 중에 보낸 메시지"
	turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: text})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if turn.State != domain.TurnStateRunning || turn.ProviderTurnID == "" {
		t.Fatalf("자율 턴 중에 보낸 사용자 턴 = %+v, 큐에 남지 않고 바로 dispatch 되어야 한다", turn)
	}
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != text {
		t.Fatalf("provider 가 받은 사용자 메시지 = %q, [%q] 여야 한다", got, text)
	}

	// ACP 드라이버는 사용자 턴의 TurnStarted 보다 자율 TurnCompleted 를 먼저 낸다.
	h.advance(5 * time.Second)
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: autonomousTurnID, TurnState: domain.TurnStateCompleted,
	})
	h.awaitTurnState(t, autonomousTurnID, domain.TurnStateCompleted)
	h.awaitProjectionIdle()
	if !h.ctrl.busy() {
		t.Errorf("자율 턴의 끝이 사용자 턴 %s 의 소유를 풀었다", turn.ProviderTurnID)
	}
	if userRow, ok := turnByID(h.turns(t), turn.ID); !ok || userRow.State != domain.TurnStateRunning {
		t.Errorf("자율 턴이 끝난 뒤 사용자 턴 행 = %+v (있음=%v), running 이어야 한다", userRow, ok)
	}

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: domain.TurnStateCompleted},
	)
	h.awaitTurnState(t, turn.ProviderTurnID, domain.TurnStateCompleted)
	h.awaitProjectionIdle()
	if got := h.ctrl.State(); got != ports.ChatControllerReady {
		t.Errorf("사용자 턴이 끝난 뒤 controller 상태 = %q, ready 여야 한다", got)
	}
}

// dispatch 중에 온 자율 TurnStarted 는 dispatch 중인 사용자 턴 행에 묶이지 않고 자기 행으로 adopt
// 된다.
func TestProviderInitiatedTurnStartedDuringDispatchIsAdoptedSeparately(t *testing.T) {
	h := newAutonomousTurnHarness(t)
	h.start(t, newAutonomousTurnConversation(false), "")
	ctx := context.Background()

	var (
		projected, primary bool
		projectErr         error
		dispatching        string
	)
	h.conv.setOnSend(func(string) {
		// dispatch 가 SendTurn 을 부르는 동안이라 dispatchingTurnID 가 서 있다.
		h.ctrl.mu.Lock()
		dispatching = h.ctrl.dispatchingTurnID
		h.ctrl.mu.Unlock()
		projected, primary, projectErr = h.ctrl.projectEvent(ctx, ports.ChatEvent{
			Kind: ports.ChatEventTurnStarted, ProviderTurnID: autonomousTurnID,
		})
	})
	const text = "dispatch 중인 사용자 메시지"
	turn, sendErr := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: text})
	if dispatching == "" {
		t.Fatal("시험 전제: SendTurn 동안 dispatchingTurnID 가 비어 있다")
	}
	if projectErr != nil || !projected {
		t.Fatalf("자율 TurnStarted 투영: projected=%v err=%v", projected, projectErr)
	}
	if primary {
		t.Error("dispatch 중에 온 자율 TurnStarted 가 primary 턴으로 잡혔다")
	}
	if sendErr != nil {
		t.Errorf("Send: %v", sendErr)
	}

	var userTurnID string
	for _, message := range h.messages(t) {
		if message.Role == domain.MessageRoleUser && message.Text == text {
			userTurnID = message.TurnID
		}
	}
	if userTurnID == "" || userTurnID != dispatching {
		t.Fatalf("사용자 메시지의 턴 = %q, dispatch 중이던 턴 %q 여야 한다", userTurnID, dispatching)
	}
	turns := h.turns(t)
	autonomous, ok := turnByProviderID(turns, autonomousTurnID)
	if !ok {
		t.Fatalf("자율 턴 %s 의 행이 없다; 행 = %+v", autonomousTurnID, turns)
	}
	if autonomous.ID == userTurnID {
		t.Errorf("자율 턴 %s 이 사용자 턴 행 %s 에 BindTurnToProvider 로 묶였다", autonomousTurnID, userTurnID)
	}
	if autonomous.State != domain.TurnStateRunning {
		t.Errorf("adopt 된 자율 행 상태 = %s, running 이어야 한다", autonomous.State)
	}
	userRow, ok := turnByID(turns, userTurnID)
	if !ok || userRow.ProviderTurnID != turn.ProviderTurnID || turn.ProviderTurnID == "" {
		t.Errorf("사용자 턴 행 = %+v (있음=%v), provider 턴 %q 로 묶여야 한다", userRow, ok, turn.ProviderTurnID)
	}
	h.ctrl.mu.Lock()
	pending := h.ctrl.pendingTurnID
	h.ctrl.mu.Unlock()
	if pending != turn.ProviderTurnID {
		t.Errorf("pendingTurnID = %q, 사용자 턴 %q 여야 한다", pending, turn.ProviderTurnID)
	}
}

// live reconnect 는 running 자율 행을 recovered 로 닫고, 그 행으로 Busy 를 세우지 않는다.
func TestProviderInitiatedTurnLiveReconnectRecoversRunningRow(t *testing.T) {
	h := newAutonomousTurnHarness(t)
	ctx := context.Background()
	h.start(t, newAutonomousTurnConversation(false), "")
	h.advance(time.Minute)
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: autonomousTurnID})
	h.awaitTurnState(t, autonomousTurnID, domain.TurnStateRunning)
	h.awaitProjectionIdle()
	h.svc.StopAll(ctx)

	rec, found, err := h.st.GetSession(ctx, autonomousTestSession)
	if err != nil || !found {
		t.Fatalf("read session before reconnect: found=%v err=%v", found, err)
	}
	rec.Metadata.ProviderConversationID = autonomousThreadID
	if err := h.st.UpdateSession(ctx, rec); err != nil {
		t.Fatalf("update session before reconnect: %v", err)
	}

	h.advance(time.Minute)
	h.start(t, newAutonomousTurnConversation(true), autonomousThreadID)
	if row, ok := turnByProviderID(h.turns(t), autonomousTurnID); !ok || row.State != domain.TurnStateRecovered {
		t.Errorf("live reconnect 뒤 자율 행 = %+v (있음=%v), recovered 로 닫혀야 한다", row, ok)
	}
	if got := h.ctrl.State(); got == ports.ChatControllerBusy {
		t.Errorf("live reconnect 뒤 controller 상태 = %q, 자율 행으로 busy 가 되면 안 된다", got)
	}
	turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "다시 붙은 뒤의 메시지"})
	if err != nil {
		t.Fatalf("Send after reconnect: %v", err)
	}
	if turn.State != domain.TurnStateRunning {
		t.Errorf("live reconnect 뒤 보낸 사용자 턴 = %+v, 바로 dispatch 되어야 한다", turn)
	}
}

// restoreLiveTurnOwnership 은 running 자율 행을 소유 턴으로 고르지 않는다.
func TestRestoreLiveTurnOwnershipSkipsProviderInitiatedTurn(t *testing.T) {
	requestedAt := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	userTurn := domain.ConversationTurn{
		ID: "user-turn", ProviderTurnID: "provider-turn-7", State: domain.TurnStateRunning, RequestedAt: requestedAt,
	}
	autonomousTurn := domain.ConversationTurn{
		ID: "autonomous-turn", ProviderTurnID: autonomousTurnID, State: domain.TurnStateRunning,
		RequestedAt: requestedAt.Add(time.Minute),
	}

	withUser := &Controller{}
	if got := withUser.restoreLiveTurnOwnership([]domain.ConversationTurn{userTurn, autonomousTurn}); got != userTurn.ProviderTurnID {
		t.Errorf("사용자 턴과 더 늦은 자율 턴 가운데 고른 소유 턴 = %q, %q 여야 한다", got, userTurn.ProviderTurnID)
	}

	onlyAutonomous := &Controller{}
	if got := onlyAutonomous.restoreLiveTurnOwnership([]domain.ConversationTurn{autonomousTurn}); got != "" {
		t.Errorf("자율 행만 있을 때 고른 소유 턴 = %q, 없어야 한다", got)
	}
	if onlyAutonomous.state == ports.ChatControllerBusy || onlyAutonomous.pendingTurnID != "" {
		t.Errorf("자율 행만 있을 때 state=%q pending=%q, busy 가 되면 안 된다",
			onlyAutonomous.state, onlyAutonomous.pendingTurnID)
	}
}
