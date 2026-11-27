package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// 이 파일은 사용자 턴 없이 provider 가 스스로 연 사이클(Claude Code 의
// <task-notification> 턴)을 드라이버가 자율 턴으로 묶는지 본다. 시험은
// 공개 드라이버 API 와 fakeAgent 만 쓰고 자율 턴의 내부 상태는 읽지 않는다.

// recordedEvent 는 받은 이벤트와 받은 시각이다.
type recordedEvent struct {
	event ports.ChatEvent
	at    time.Time
}

// providerInitiatedHarness 는 conversation 의 이벤트를 따로 모으는 시험 틀이다.
// 채널을 계속 비워 두므로 emit 이 소비자를 기다리다 이벤트를 버리지 않는다.
type providerInitiatedHarness struct {
	t     *testing.T
	agent *fakeAgent
	conv  ports.ChatConversation
	quiet time.Duration

	mu     sync.Mutex
	events []recordedEvent
	done   chan struct{}
}

func newProviderInitiatedDriver(agent *fakeAgent, enabled bool, quiet time.Duration) *Driver {
	driver := New(Config{
		Harness: domain.HarnessClaudeCode,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true, ports.ChatCapabilityApprovals: true,
			ports.ChatCapabilityInterrupt: true, ports.ChatCapabilityCompaction: true,
		},
		Probe:                  func(context.Context) error { return nil },
		Launch:                 func(context.Context, LaunchConfig) (Launch, error) { return Launch{Command: "fake"}, nil },
		ProviderInitiatedTurns: enabled,
		ProviderInitiatedQuiet: quiet,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	driver.useTestProcess(fakeSpawn(agent))
	return driver
}

// startProviderInitiatedHarness 는 새 대화를 열고 controller ready 까지 받은 뒤
// 모은 이벤트를 비운다.
func startProviderInitiatedHarness(
	t *testing.T,
	agent *fakeAgent,
	enabled bool,
	quiet time.Duration,
) *providerInitiatedHarness {
	t.Helper()
	driver := newProviderInitiatedDriver(agent, enabled, quiet)
	conv, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := attachProviderInitiatedHarness(t, agent, conv, quiet)
	h.waitFor("controller ready", 0, func(event ports.ChatEvent) bool {
		return event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerReady
	}, 2*time.Second)
	h.reset()
	return h
}

func attachProviderInitiatedHarness(
	t *testing.T,
	agent *fakeAgent,
	conv ports.ChatConversation,
	quiet time.Duration,
) *providerInitiatedHarness {
	t.Helper()
	h := &providerInitiatedHarness{t: t, agent: agent, conv: conv, quiet: quiet, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		for event := range conv.Events() {
			h.mu.Lock()
			h.events = append(h.events, recordedEvent{event: event, at: time.Now()})
			h.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = conv.Close()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("event stream did not close after Close")
		}
	})
	return h
}

func (h *providerInitiatedHarness) reset() {
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
}

// mark 는 지금까지 받은 이벤트 수다. 이후 검사의 시작 위치로 쓴다.
func (h *providerInitiatedHarness) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (h *providerInitiatedHarness) snapshot() []recordedEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedEvent(nil), h.events...)
}

func (h *providerInitiatedHarness) push(update acpsdk.SessionUpdate) {
	h.t.Helper()
	if err := h.agent.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(h.conv.ProviderConversationID()),
		Update:    update,
	}); err != nil {
		h.t.Fatalf("provider session/update: %v", err)
	}
}

// waitFor 는 from 이후에서 match 를 만족하는 첫 이벤트를 기다린다. 오지 않으면
// 받은 이벤트를 모두 적고 실패한다.
func (h *providerInitiatedHarness) waitFor(
	desc string,
	from int,
	match func(ports.ChatEvent) bool,
	timeout time.Duration,
) (int, recordedEvent) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		events := h.snapshot()
		for i := from; i < len(events); i++ {
			if match(events[i].event) {
				return i, events[i]
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: 이벤트가 %s 안에 오지 않았다\n%s", desc, timeout, dumpRecordedEvents(events, from))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expectNone 은 window 동안 from 이후에 match 를 만족하는 이벤트가 없는지 본다.
func (h *providerInitiatedHarness) expectNone(
	desc string,
	from int,
	match func(ports.ChatEvent) bool,
	window time.Duration,
) {
	h.t.Helper()
	deadline := time.Now().Add(window)
	for {
		events := h.snapshot()
		for i := from; i < len(events); i++ {
			if match(events[i].event) {
				h.t.Fatalf("%s: 나오지 말아야 할 이벤트가 %d 번째에 나왔다\n%s", desc, i, dumpRecordedEvents(events, from))
			}
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *providerInitiatedHarness) count(from int, match func(ports.ChatEvent) bool) int {
	n := 0
	events := h.snapshot()
	for i := from; i < len(events); i++ {
		if match(events[i].event) {
			n++
		}
	}
	return n
}

func dumpRecordedEvents(events []recordedEvent, from int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "받은 이벤트 %d 개(%d 번째부터):\n", len(events)-from, from)
	for i := from; i < len(events); i++ {
		event := events[i].event
		fmt.Fprintf(&b, "  [%d] kind=%s turn=%q item=%q state=%q controller=%q activity=%q request=%q\n",
			i, event.Kind, event.ProviderTurnID, event.ProviderItemID, event.TurnState,
			event.ControllerState, event.ActivityStatus, event.RequestID)
	}
	return b.String()
}

func isEventKind(kind ports.ChatEventKind) func(ports.ChatEvent) bool {
	return func(event ports.ChatEvent) bool { return event.Kind == kind }
}

func isTurnEvent(kind ports.ChatEventKind, turnID string) func(ports.ChatEvent) bool {
	return func(event ports.ChatEvent) bool { return event.Kind == kind && event.ProviderTurnID == turnID }
}

func isProviderInitiatedStart(event ports.ChatEvent) bool {
	return event.Kind == ports.ChatEventTurnStarted && ports.IsProviderInitiatedTurn(event.ProviderTurnID)
}

func isTurnBoundary(event ports.ChatEvent) bool {
	return event.Kind == ports.ChatEventTurnStarted || event.Kind == ports.ChatEventTurnCompleted
}

func backgroundToolCall(id string, status acpsdk.ToolCallStatus) acpsdk.SessionUpdate {
	return acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
		SessionUpdate: "tool_call", ToolCallId: acpsdk.ToolCallId(id), Title: "Run background task",
		Kind: acpsdk.ToolKindExecute, Status: status,
	}}
}

func backgroundToolStatus(id string, status acpsdk.ToolCallStatus) acpsdk.SessionUpdate {
	return acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
		SessionUpdate: "tool_call_update", ToolCallId: acpsdk.ToolCallId(id), Status: &status,
	}}
}

func assertNoProviderInitiatedIDs(t *testing.T, desc string, events []ports.ChatEvent) {
	t.Helper()
	for i, event := range events {
		if ports.IsProviderInitiatedTurn(event.ProviderTurnID) {
			t.Fatalf("%s: %d 번째 이벤트 %s 가 자율 턴 id %q 를 가진다", desc, i, event.Kind, event.ProviderTurnID)
		}
	}
}

// 턴 밖에서 온 메시지 조각과 도구 호출은 자율 턴 하나로 묶이고, 조용한 시간이
// 지나면 같은 id 로 한 번 닫힌다. controller 상태 이벤트는 내지 않는다.
func TestProviderInitiatedTurnWrapsOutputWithoutUserTurn(t *testing.T) {
	cases := []struct {
		name    string
		output  func(h *providerInitiatedHarness, cycle int)
		isItem  func(ports.ChatEvent) bool
		itemEnd ports.ChatEventKind
	}{
		{
			name: "agent_message_chunk",
			output: func(h *providerInitiatedHarness, _ int) {
				for i, text := range []string{"백그라운드 ", "작업이 ", "끝났다"} {
					if i > 0 {
						// 조용한 시간보다 짧은 간격은 같은 턴을 연장한다.
						time.Sleep(h.quiet / 5)
					}
					h.push(acpsdk.UpdateAgentMessageText(text))
				}
			},
			isItem:  isEventKind(ports.ChatEventMessageDelta),
			itemEnd: ports.ChatEventMessageCompleted,
		},
		{
			name: "tool_call",
			output: func(h *providerInitiatedHarness, cycle int) {
				h.push(backgroundToolCall(fmt.Sprintf("bg-tool-%d", cycle), acpsdk.ToolCallStatusCompleted))
			},
			isItem: func(event ports.ChatEvent) bool {
				return event.Kind == ports.ChatEventActivityStarted || event.Kind == ports.ChatEventActivityCompleted
			},
			itemEnd: ports.ChatEventActivityCompleted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startProviderInitiatedHarness(t, &fakeAgent{}, true, 250*time.Millisecond)
			seen := map[string]bool{}
			for cycle := 0; cycle < 2; cycle++ {
				from := h.mark()
				tc.output(h, cycle)
				lastPush := time.Now()

				startIdx, started := h.waitFor("자율 TurnStarted", from, isProviderInitiatedStart, 2*time.Second)
				turnID := started.event.ProviderTurnID
				if seen[turnID] {
					t.Fatalf("사이클 %d 의 자율 턴 id %q 가 앞 사이클과 같다", cycle, turnID)
				}
				seen[turnID] = true

				doneIdx, done := h.waitFor("자율 TurnCompleted", from,
					isEventKind(ports.ChatEventTurnCompleted), h.quiet+3*time.Second)
				if done.event.ProviderTurnID != turnID || done.event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("TurnCompleted = turn %q state %q, want turn %q completed",
						done.event.ProviderTurnID, done.event.TurnState, turnID)
				}
				if waited := done.at.Sub(lastPush); waited < h.quiet {
					t.Fatalf("마지막 갱신 뒤 %s 만에 닫혔다, 조용한 시간 %s 을 기다려야 한다", waited, h.quiet)
				}
				// 닫힌 뒤 같은 사이클에서 턴 경계가 더 나오지 않는다.
				h.expectNone("닫힌 자율 턴 뒤의 턴 경계", doneIdx+1, isTurnBoundary, 2*h.quiet)

				events := h.snapshot()
				itemEndIdx := -1
				items := 0
				for i := from; i < len(events); i++ {
					event := events[i].event
					if event.Kind == ports.ChatEventControllerState {
						t.Fatalf("자율 턴이 controller 상태 이벤트를 냈다\n%s", dumpRecordedEvents(events, from))
					}
					if tc.isItem(event) {
						items++
						if i < startIdx {
							t.Fatalf("출력이 자율 TurnStarted 보다 먼저 나왔다\n%s", dumpRecordedEvents(events, from))
						}
						if event.ProviderTurnID != turnID {
							t.Fatalf("출력 %s 의 turn = %q, want %q", event.Kind, event.ProviderTurnID, turnID)
						}
					}
					if event.Kind == tc.itemEnd && event.ProviderTurnID == turnID && itemEndIdx < 0 {
						itemEndIdx = i
					}
				}
				if items == 0 {
					t.Fatalf("자율 턴의 출력이 없다\n%s", dumpRecordedEvents(events, from))
				}
				if itemEndIdx < 0 || itemEndIdx > doneIdx {
					t.Fatalf("%s 가 TurnCompleted 보다 먼저 나와야 한다\n%s", tc.itemEnd, dumpRecordedEvents(events, from))
				}
				if got := h.count(from, isEventKind(ports.ChatEventTurnStarted)); got != 1 {
					t.Fatalf("TurnStarted %d 번, want 1\n%s", got, dumpRecordedEvents(events, from))
				}
				if got := h.count(from, isEventKind(ports.ChatEventTurnCompleted)); got != 1 {
					t.Fatalf("TurnCompleted %d 번, want 1\n%s", got, dumpRecordedEvents(events, from))
				}
			}
		})
	}
}

// 끝나지 않은 도구(pending, in_progress, 상태 없음)는 조용한 시간이 지나도
// 자율 턴을 닫지 못하게 한다. 도구가 끝나면 조용한 시간 뒤에 닫힌다.
func TestProviderInitiatedTurnWaitsForOpenTools(t *testing.T) {
	for _, status := range []acpsdk.ToolCallStatus{
		acpsdk.ToolCallStatusPending, acpsdk.ToolCallStatusInProgress, "",
	} {
		name := string(status)
		if name == "" {
			name = "empty_status"
		}
		t.Run(name, func(t *testing.T) {
			h := startProviderInitiatedHarness(t, &fakeAgent{}, true, 100*time.Millisecond)
			h.push(backgroundToolCall("bg-tool", status))
			_, started := h.waitFor("자율 TurnStarted", 0, isProviderInitiatedStart, 2*time.Second)
			turnID := started.event.ProviderTurnID

			h.expectNone("열린 도구가 있는 동안의 TurnCompleted", 0,
				isEventKind(ports.ChatEventTurnCompleted), 4*h.quiet)

			from := h.mark()
			h.push(backgroundToolStatus("bg-tool", acpsdk.ToolCallStatusCompleted))
			doneIdx, done := h.waitFor("도구가 끝난 뒤의 TurnCompleted", from,
				isEventKind(ports.ChatEventTurnCompleted), h.quiet+3*time.Second)
			if done.event.ProviderTurnID != turnID || done.event.TurnState != domain.TurnStateCompleted {
				t.Fatalf("TurnCompleted = turn %q state %q, want turn %q completed",
					done.event.ProviderTurnID, done.event.TurnState, turnID)
			}
			toolIdx, tool := h.waitFor("도구 완료", from, isTurnEvent(ports.ChatEventActivityCompleted, turnID), time.Second)
			if toolIdx > doneIdx || tool.event.ActivityStatus != domain.ActivityStatusCompleted {
				t.Fatalf("도구 완료가 TurnCompleted 앞에 completed 로 와야 한다\n%s", dumpRecordedEvents(h.snapshot(), 0))
			}
		})
	}
}

// 자율 턴 안에서 대기 중인 승인은 닫힘을 막는다. 승인이 풀리면 닫힌다.
func TestProviderInitiatedTurnWaitsForPendingApproval(t *testing.T) {
	agent := &fakeAgent{}
	h := startProviderInitiatedHarness(t, agent, true, 100*time.Millisecond)
	h.push(acpsdk.UpdateAgentMessageText("파일을 고쳐도 될까요"))
	_, started := h.waitFor("자율 TurnStarted", 0, isProviderInitiatedStart, 2*time.Second)
	turnID := started.event.ProviderTurnID

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type permissionResult struct {
		response acpsdk.RequestPermissionResponse
		err      error
	}
	result := make(chan permissionResult, 1)
	go func() {
		response, err := agent.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
			SessionId: acpsdk.SessionId(h.conv.ProviderConversationID()),
			ToolCall: acpsdk.ToolCallUpdate{
				ToolCallId: "bg-permission", Title: acpsdk.Ptr("Edit file"), Kind: acpsdk.Ptr(acpsdk.ToolKindEdit),
			},
			Options: []acpsdk.PermissionOption{
				{OptionId: "allow", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce},
				{OptionId: "reject", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
			},
		})
		result <- permissionResult{response: response, err: err}
	}()
	_, approval := h.waitFor("ApprovalRequested", 0, isEventKind(ports.ChatEventApprovalRequested), 2*time.Second)
	if approval.event.ProviderTurnID != turnID {
		t.Fatalf("ApprovalRequested turn = %q, want 자율 턴 %q", approval.event.ProviderTurnID, turnID)
	}

	h.expectNone("승인 대기 중의 TurnCompleted", 0, isEventKind(ports.ChatEventTurnCompleted), 4*h.quiet)

	from := h.mark()
	if err := h.conv.ResolveRequest(context.Background(), approval.event.RequestID, ports.ChatDecision{ID: "allow"}); err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.response.Outcome.Selected == nil || got.response.Outcome.Selected.OptionId != "allow" {
			t.Fatalf("permission result = %#v, %v", got.response, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider 가 승인 결과를 받지 못했다")
	}
	_, done := h.waitFor("승인 뒤의 TurnCompleted", from,
		isEventKind(ports.ChatEventTurnCompleted), 2*h.quiet+3*time.Second)
	if done.event.ProviderTurnID != turnID || done.event.TurnState != domain.TurnStateCompleted {
		t.Fatalf("TurnCompleted = turn %q state %q, want turn %q completed",
			done.event.ProviderTurnID, done.event.TurnState, turnID)
	}
}

// 자율 턴 안에서 대기 중인 입력 요청도 닫힘을 막는다. 답하면 닫힌다.
func TestProviderInitiatedTurnWaitsForPendingInput(t *testing.T) {
	agent := &fakeAgent{}
	h := startProviderInitiatedHarness(t, agent, true, 100*time.Millisecond)
	h.push(acpsdk.UpdateAgentMessageText("어느 쪽으로 할까요"))
	_, started := h.waitFor("자율 TurnStarted", 0, isProviderInitiatedStart, 2*time.Second)
	turnID := started.event.ProviderTurnID

	request := acpsdk.NewUnstableCreateElicitationRequestForm(acpsdk.UnstableElicitationSchema{
		Type:       acpsdk.UnstableElicitationSchemaTypeObject,
		Properties: map[string]any{"choice": map[string]any{"type": "string"}},
		Required:   []string{"choice"},
	})
	request.Form.Message = "Which approach?"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type inputResult struct {
		response acpsdk.UnstableCreateElicitationResponse
		err      error
	}
	result := make(chan inputResult, 1)
	go func() {
		response, err := agent.conn.UnstableCreateElicitation(ctx, request)
		result <- inputResult{response: response, err: err}
	}()
	_, input := h.waitFor("InputRequested", 0, isEventKind(ports.ChatEventInputRequested), 2*time.Second)
	if input.event.ProviderTurnID != turnID {
		t.Fatalf("InputRequested turn = %q, want 자율 턴 %q", input.event.ProviderTurnID, turnID)
	}

	h.expectNone("입력 대기 중의 TurnCompleted", 0, isEventKind(ports.ChatEventTurnCompleted), 4*h.quiet)

	from := h.mark()
	responder, ok := h.conv.(ports.ChatInputResponder)
	if !ok {
		t.Fatal("conversation 이 ChatInputResponder 가 아니다")
	}
	if err := responder.ResolveInput(context.Background(), input.event.RequestID,
		ports.ChatInputResponse{Action: "accept", Content: map[string]any{"choice": "native"}}); err != nil {
		t.Fatalf("ResolveInput: %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.response.Accept == nil || got.response.Accept.Content["choice"] != "native" {
			t.Fatalf("elicitation result = %#v, %v", got.response, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider 가 입력 결과를 받지 못했다")
	}
	_, done := h.waitFor("입력 뒤의 TurnCompleted", from,
		isEventKind(ports.ChatEventTurnCompleted), 2*h.quiet+3*time.Second)
	if done.event.ProviderTurnID != turnID || done.event.TurnState != domain.TurnStateCompleted {
		t.Fatalf("TurnCompleted = turn %q state %q, want turn %q completed",
			done.event.ProviderTurnID, done.event.TurnState, turnID)
	}
}

// assertClosedBefore 는 자율 턴이 다음 턴의 TurnStarted 보다 먼저 completed 로
// 한 번 닫히고, 그 뒤로 자율 턴 id 의 이벤트가 없는지 본다.
func assertClosedBefore(t *testing.T, h *providerInitiatedHarness, autoID string, nextStartIdx int) {
	t.Helper()
	events := h.snapshot()
	autoDone := -1
	autoMessageDone := -1
	for i, recorded := range events {
		event := recorded.event
		if event.Kind == ports.ChatEventTurnCompleted && event.ProviderTurnID == autoID {
			if autoDone >= 0 {
				t.Fatalf("자율 TurnCompleted 가 두 번 나왔다\n%s", dumpRecordedEvents(events, 0))
			}
			autoDone = i
			if event.TurnState != domain.TurnStateCompleted {
				t.Fatalf("자율 TurnCompleted state = %q, want completed", event.TurnState)
			}
		}
		if event.Kind == ports.ChatEventMessageCompleted && event.ProviderTurnID == autoID && autoMessageDone < 0 {
			autoMessageDone = i
		}
		if i > nextStartIdx && event.ProviderTurnID == autoID {
			t.Fatalf("다음 턴이 열린 뒤 자율 턴 이벤트가 나왔다\n%s", dumpRecordedEvents(events, 0))
		}
	}
	if autoDone < 0 || autoDone > nextStartIdx {
		t.Fatalf("자율 TurnCompleted 가 다음 TurnStarted(%d) 보다 먼저 나와야 한다\n%s",
			nextStartIdx, dumpRecordedEvents(events, 0))
	}
	if autoMessageDone < 0 || autoMessageDone > autoDone {
		t.Fatalf("자율 턴의 MessageCompleted 가 그 TurnCompleted 보다 먼저 나와야 한다\n%s", dumpRecordedEvents(events, 0))
	}
}

// 자율 턴이 열린 채 사용자 턴을 보내면 자율 턴이 먼저 닫히고 사용자 턴이 열린다.
func TestProviderInitiatedTurnClosesBeforeUserTurn(t *testing.T) {
	agent := &fakeAgent{promptNoPermission: true}
	h := startProviderInitiatedHarness(t, agent, true, time.Hour)
	h.push(acpsdk.UpdateAgentMessageText("백그라운드 결과"))
	_, started := h.waitFor("자율 TurnStarted", 0, isProviderInitiatedStart, 2*time.Second)
	autoID := started.event.ProviderTurnID

	ref, err := h.conv.SendTurn(context.Background(), ports.ChatUserMessage{Text: "다음 질문"})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	starter, ok := h.conv.(ports.ChatDeferredTurnStarter)
	if !ok {
		t.Fatal("conversation 이 ChatDeferredTurnStarter 가 아니다")
	}
	if err := starter.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}
	userStart, _ := h.waitFor("사용자 TurnStarted", 0,
		isTurnEvent(ports.ChatEventTurnStarted, ref.ProviderTurnID), 2*time.Second)
	h.waitFor("사용자 TurnCompleted", userStart, isTurnEvent(ports.ChatEventTurnCompleted, ref.ProviderTurnID), 2*time.Second)
	assertClosedBefore(t, h, autoID, userStart)
}

// 자율 턴이 열린 채 Compact 하면 자율 턴이 먼저 닫히고 compaction 턴이 열린다.
func TestProviderInitiatedTurnClosesBeforeCompaction(t *testing.T) {
	agent := &fakeAgent{customPrompt: func(context.Context, acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
		return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
	}}
	h := startProviderInitiatedHarness(t, agent, true, time.Hour)
	h.push(acpsdk.UpdateAgentMessageText("백그라운드 결과"))
	_, started := h.waitFor("자율 TurnStarted", 0, isProviderInitiatedStart, 2*time.Second)
	autoID := started.event.ProviderTurnID

	compactor, ok := h.conv.(ports.ChatCompactor)
	if !ok {
		t.Fatal("conversation 이 ChatCompactor 가 아니다")
	}
	if _, err := compactor.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	compactStart, compaction := h.waitFor("compaction TurnStarted", 0, func(event ports.ChatEvent) bool {
		return event.Kind == ports.ChatEventTurnStarted && event.ProviderTurnID != autoID
	}, 2*time.Second)
	h.waitFor("compaction TurnCompleted", compactStart,
		isTurnEvent(ports.ChatEventTurnCompleted, compaction.event.ProviderTurnID), 2*time.Second)
	assertClosedBefore(t, h, autoID, compactStart)
}

// 자율 id 로 Interrupt 하면 기록만 interrupted 로 닫고 session/cancel 은 보내지 않는다.
func TestProviderInitiatedTurnInterruptClosesWithoutCancel(t *testing.T) {
	agent := &fakeAgent{}
	h := startProviderInitiatedHarness(t, agent, true, time.Hour)
	h.push(backgroundToolCall("bg-tool", acpsdk.ToolCallStatusInProgress))
	_, started := h.waitFor("자율 TurnStarted", 0, isProviderInitiatedStart, 2*time.Second)
	autoID := started.event.ProviderTurnID

	if err := h.conv.Interrupt(context.Background(), autoID); err != nil {
		t.Fatalf("Interrupt(자율 턴) = %v, want nil", err)
	}
	doneIdx, done := h.waitFor("interrupted TurnCompleted", 0,
		isTurnEvent(ports.ChatEventTurnCompleted, autoID), 2*time.Second)
	if done.event.TurnState != domain.TurnStateInterrupted {
		t.Fatalf("TurnCompleted state = %q, want interrupted", done.event.TurnState)
	}
	toolIdx, _ := h.waitFor("열린 도구 정리", 0, isTurnEvent(ports.ChatEventActivityCompleted, autoID), time.Second)
	if toolIdx > doneIdx {
		t.Fatalf("열린 도구 정리가 TurnCompleted 보다 먼저 나와야 한다\n%s", dumpRecordedEvents(h.snapshot(), 0))
	}

	if err := h.conv.Interrupt(context.Background(), autoID); !errors.Is(err, ports.ErrChatNoActiveTurn) {
		t.Fatalf("닫힌 자율 턴의 Interrupt = %v, want ErrChatNoActiveTurn", err)
	}
	h.expectNone("두 번째 TurnCompleted", doneIdx+1, isEventKind(ports.ChatEventTurnCompleted), 300*time.Millisecond)
	agent.mu.Lock()
	cancels := agent.cancelCalls
	agent.mu.Unlock()
	if cancels != 0 {
		t.Fatalf("session/cancel %d 번, want 0", cancels)
	}
}

// session/load 재생과 RefreshHistory 재생은 사용자 메시지가 없는 출력이어도
// 자율 턴을 열지 않는다.
func TestProviderInitiatedTurnIgnoresHistoryReplay(t *testing.T) {
	replay := func(text string) []acpsdk.SessionUpdate {
		return []acpsdk.SessionUpdate{
			acpsdk.UpdateAgentMessageText(text),
			backgroundToolCall("replayed-tool", acpsdk.ToolCallStatusCompleted),
		}
	}
	agent := &fakeAgent{
		capabilities:      &acpsdk.AgentCapabilities{LoadSession: true},
		loadUpdateBatches: [][]acpsdk.SessionUpdate{replay("재생된 출력"), replay("다시 재생된 출력")},
	}
	quiet := 50 * time.Millisecond
	driver := newProviderInitiatedDriver(agent, true, quiet)
	conv, err := driver.Resume(context.Background(), ports.ChatResumeConfig{
		ProviderConversationID: "provider-session-1",
		WorkspacePath:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	h := attachProviderInitiatedHarness(t, agent, conv, quiet)

	history, err := conv.(ports.ChatHistoryReader).ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("session/load 재생 기록이 비었다")
	}
	assertNoProviderInitiatedIDs(t, "session/load 재생", history)

	refreshed, err := conv.(ports.ChatHistoryRefresher).RefreshHistory(context.Background())
	if err != nil {
		t.Fatalf("RefreshHistory: %v", err)
	}
	if len(refreshed) == 0 {
		t.Fatal("RefreshHistory 재생 기록이 비었다")
	}
	assertNoProviderInitiatedIDs(t, "RefreshHistory 재생", refreshed)

	h.expectNone("재생 뒤 live 자율 턴", 0, func(event ports.ChatEvent) bool {
		return event.Kind == ports.ChatEventTurnStarted || ports.IsProviderInitiatedTurn(event.ProviderTurnID)
	}, 6*quiet)
}

// live 대화의 RefreshHistory 는 재생을 시작하기 전에 열린 자율 턴을 live 로 닫는다.
func TestProviderInitiatedTurnClosesBeforeHistoryRefresh(t *testing.T) {
	agent := &fakeAgent{
		capabilities: &acpsdk.AgentCapabilities{LoadSession: true},
		loadUpdates:  []acpsdk.SessionUpdate{acpsdk.UpdateAgentMessageText("재생된 출력")},
	}
	driver := newProviderInitiatedDriver(agent, true, time.Hour)
	conv, err := driver.Resume(context.Background(), ports.ChatResumeConfig{
		ProviderConversationID: "provider-session-1",
		WorkspacePath:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	h := attachProviderInitiatedHarness(t, agent, conv, time.Hour)
	if _, err := conv.(ports.ChatHistoryReader).ReadHistory(context.Background()); err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}

	from := h.mark()
	h.push(acpsdk.UpdateAgentMessageText("live 자율 출력"))
	_, started := h.waitFor("자율 TurnStarted", from, isProviderInitiatedStart, 2*time.Second)
	autoID := started.event.ProviderTurnID

	refreshed, err := conv.(ports.ChatHistoryRefresher).RefreshHistory(context.Background())
	if err != nil {
		t.Fatalf("RefreshHistory: %v", err)
	}
	assertNoProviderInitiatedIDs(t, "RefreshHistory 재생", refreshed)
	doneIdx, done := h.waitFor("RefreshHistory 앞의 자율 TurnCompleted", from,
		isTurnEvent(ports.ChatEventTurnCompleted, autoID), 2*time.Second)
	if done.event.TurnState != domain.TurnStateCompleted {
		t.Fatalf("자율 TurnCompleted state = %q, want completed", done.event.TurnState)
	}
	h.expectNone("재생 뒤 다시 열린 턴", doneIdx+1, isEventKind(ports.ChatEventTurnStarted), 200*time.Millisecond)
}

// 플래그가 꺼져 있으면 지금처럼 턴 없는 갱신에 턴 경계를 내지 않는다.
func TestProviderInitiatedTurnDisabledEmitsNoTurn(t *testing.T) {
	quiet := 50 * time.Millisecond
	h := startProviderInitiatedHarness(t, &fakeAgent{}, false, quiet)
	h.push(acpsdk.UpdateAgentMessageText("턴 밖 출력"))
	h.push(backgroundToolCall("bg-tool", acpsdk.ToolCallStatusCompleted))
	_, delta := h.waitFor("MessageDelta", 0, isEventKind(ports.ChatEventMessageDelta), 2*time.Second)
	if delta.event.ProviderTurnID != "" {
		t.Fatalf("플래그가 꺼졌는데 MessageDelta turn = %q", delta.event.ProviderTurnID)
	}
	h.expectNone("플래그가 꺼진 대화의 턴 경계", 0, func(event ports.ChatEvent) bool {
		return isTurnBoundary(event) || ports.IsProviderInitiatedTurn(event.ProviderTurnID)
	}, 6*quiet)
}

// Close 는 열린 자율 턴의 타이머를 멈추고 닫는 이벤트를 내지 않는다.
func TestProviderInitiatedTurnStopsOnClose(t *testing.T) {
	h := startProviderInitiatedHarness(t, &fakeAgent{}, true, 100*time.Millisecond)
	h.push(acpsdk.UpdateAgentMessageText("닫히기 전 출력"))
	h.waitFor("자율 TurnStarted", 0, isProviderInitiatedStart, 2*time.Second)
	from := h.mark()
	if err := h.conv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 뒤 이벤트 스트림이 닫히지 않았다")
	}
	time.Sleep(3 * h.quiet)
	if got := h.count(from, isEventKind(ports.ChatEventTurnCompleted)); got != 0 {
		t.Fatalf("Close 뒤 TurnCompleted %d 번, want 0\n%s", got, dumpRecordedEvents(h.snapshot(), from))
	}
}

func TestProviderInitiatedTurnPrefixRecognizesOnlyPrefixedIDs(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{id: "", want: false},
		{id: ports.ProviderInitiatedTurnPrefix, want: false},
		{id: ports.ProviderInitiatedTurnPrefix + "3f1c", want: true},
		{id: "turn-1", want: false},
		{id: "x" + ports.ProviderInitiatedTurnPrefix + "3f1c", want: false},
	}
	for _, tc := range cases {
		if got := ports.IsProviderInitiatedTurn(tc.id); got != tc.want {
			t.Errorf("IsProviderInitiatedTurn(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
