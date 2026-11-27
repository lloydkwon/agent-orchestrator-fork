package acp

import (
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// 자율 턴(provider-initiated turn)은 사용자 턴 밖에서 provider 가 스스로 연
// 사이클이다. Claude Code 가 백그라운드 작업 완료 알림(<task-notification>)으로
// 여는 사이클은 session/prompt 를 거치지 않고 session/update 만 보낸다. 그래서
// activeTurn 이 비어 있을 때 온 출력을 접두사 id 의 턴 하나로 묶고, 마지막 갱신
// 뒤 조용한 시간이 지나면 닫는다.
//
// 잠금 순서는 turnEmitMu → c.mu 다. c.mu 를 쥔 채 turnEmitMu 를 잡거나 emit 하지
// 않는다. turnEmitMu 를 쥔 쪽은 channel 전송만 기다린다. controller 는 sendMu 를
// 쥔 채 StartDeferredTurn 과 Compact 를 부르고, 그 둘은 turnEmitMu 를 기다린다.
// 그러므로 turnEmitMu 를 쥔 채 controller 의 응답(승인 결정 같은)을 기다리면 안
// 된다. 남는 교착 위험은 events 버퍼(eventBuffer)가 가득 찼을 때 동기 emit 이
// 막히는 기존 경우와 같은 종류다.
//
// 알려진 한계:
//   - StartDeferredTurn 이 activeTurn 을 정한 뒤 runTurn 이 TurnStarted 를 내기
//     전에 온 자율 갱신은 사용자 턴의 것으로 묶인다.
//   - tool_call 로 알린 도구가 끝나지 않으면, 갱신 없이 조용한 시간의
//     providerInitiatedOpenToolWaitLimit 배(기본 10분)가 더 지날 때까지 자율 턴이
//     열려 있다. 그 뒤에는 닫고 그 도구를 실패로 정리한다. 그 도구의 마지막
//     갱신이 늦게 오면 턴 없는 갱신으로 나간다.

// providerInitiatedOpenToolWaitLimit 는 알린 도구만 끝나지 않은 채 갱신이 없을
// 때, 첫 조용한 시간 뒤에 몇 번 더 기다리는지다. 도구의 마지막 갱신이 오지
// 않아도 자율 턴이 끝없이 열려 있지 않게 한다.
const providerInitiatedOpenToolWaitLimit = 40

// providerInitiatedTurn 은 열린 자율 턴이다. c.mu 가 지킨다. seq 는 갱신마다
// 늘고, 타이머는 자기가 잡은 seq 가 그대로일 때만 턴을 닫는다. openToolWaits 는
// 마지막 갱신 뒤 열린 도구 때문에 닫지 못하고 다시 기다린 횟수다.
type providerInitiatedTurn struct {
	id            string
	seq           uint64
	timer         *time.Timer
	openToolWaits int
}

// turnItems 는 한 턴에서 아직 끝나지 않은 항목들이다. 턴을 닫을 때
// settleTurnItems 가 이 항목들의 완료 이벤트를 낸다.
type turnItems struct {
	messages       map[string]string
	thoughts       map[string]string
	nestedMessages map[string]nestedMessageState
	tools          map[string]*toolState
}

// closingTurn 은 c.mu 아래에서 떼어 낸 자율 턴이다. 떼어 낸 뒤의 항목 map 은
// 이 값만 가지므로 잠금 없이 읽어도 된다.
type closingTurn struct {
	id    string
	items turnItems
}

// providerInitiatedQuietPeriod 는 설정된 조용한 시간을, 설정이 없으면 기본값을
// 돌려준다.
func (c *conversation) providerInitiatedQuietPeriod() time.Duration {
	if c.providerInitiatedQuiet > 0 {
		return c.providerInitiatedQuiet
	}
	return defaultProviderInitiatedQuiet
}

// currentTurnLocked 는 지금 이벤트를 묶을 턴 id 다. 사용자 턴(activeTurn)이
// 우선이고, 없으면 열린 자율 턴이다. c.mu 를 쥐고 부른다.
func (c *conversation) currentTurnLocked() string {
	if c.activeTurn != "" {
		return c.activeTurn
	}
	if c.autoTurn != nil {
		return c.autoTurn.id
	}
	return ""
}

// lockTurnBoundary 는 플래그가 켜져 있으면 turnEmitMu 를 잡고 푸는 함수를
// 돌려준다. 플래그가 꺼져 있으면 아무것도 잡지 않아 지금과 같다. c.mu 를 쥐기
// 전에 부른다.
func (c *conversation) lockTurnBoundary() func() {
	if !c.providerInitiatedTurns {
		return func() {}
	}
	c.turnEmitMu.Lock()
	return c.turnEmitMu.Unlock
}

// takeProviderInitiatedTurnLocked 는 열린 자율 턴을 떼어 내고 타이머를 멈춘다.
// 항목 map 은 새로 만들어 다음 턴이 쓰게 한다. 열린 자율 턴이 없으면 nil 이다.
// c.mu 를 쥐고 부른다.
func (c *conversation) takeProviderInitiatedTurnLocked() *closingTurn {
	turn := c.autoTurn
	if turn == nil {
		return nil
	}
	c.autoTurn = nil
	if turn.timer != nil {
		turn.timer.Stop()
	}
	closing := &closingTurn{id: turn.id, items: turnItems{
		messages: c.messages, thoughts: c.thoughts,
		nestedMessages: c.nestedMessages, tools: c.tools,
	}}
	c.resetTurnItemsLocked()
	return closing
}

// stopProviderInitiatedTurnLocked 는 닫는 이벤트 없이 자율 턴 상태를 지운다.
// Interrupt 가 떼어 두고 아직 내지 않은 턴도 버린다. Close, Hibernate,
// Terminate 가 쓴다. c.mu 를 쥐고 부른다.
func (c *conversation) stopProviderInitiatedTurnLocked() {
	c.interruptedTurn = nil
	if c.autoTurn == nil {
		return
	}
	if c.autoTurn.timer != nil {
		c.autoTurn.timer.Stop()
	}
	c.autoTurn = nil
}

// resetTurnItemsLocked 는 턴마다 새로 쓰는 항목 상태를 비운다. c.mu 를 쥐고
// 부른다.
func (c *conversation) resetTurnItemsLocked() {
	c.messages = make(map[string]string)
	c.thoughts = make(map[string]string)
	c.nestedMessages = make(map[string]nestedMessageState)
	c.tools = make(map[string]*toolState)
	c.turnDiffs = nil
	c.turnDiffTurnID = ""
	c.providerFailure = nil
}

// emitClosingTurn 은 떼어 낸 자율 턴의 열린 항목을 정리하고 TurnCompleted 를
// 낸다. ControllerState 이벤트는 내지 않는다. 자율 턴은 controller 를 Busy 로
// 만들지 않았기 때문이다. turnEmitMu 를 쥐고 부른다.
func (c *conversation) emitClosingTurn(closing *closingTurn, state domain.TurnState) {
	if closing == nil {
		return
	}
	c.settleTurnItems(closing.id, closing.items, state)
	c.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: closing.id, TurnState: state})
}

// emitInterruptedProviderTurn 은 Interrupt 가 떼어 둔 자율 턴의 interrupted
// TurnCompleted 를 낸다. 떼어 둔 턴이 없으면 아무것도 하지 않는다. turnEmitMu 를
// 쥐고 부른다. 새 턴의 TurnStarted 를 내는 길은 모두 이 함수를 먼저 불러, 앞
// 턴의 닫힘이 새 턴의 시작보다 늦게 나가지 않게 한다. Interrupt 가 띄운
// goroutine 과 이 길들 가운데 먼저 turnEmitMu 를 잡은 쪽이 내고, 나중 쪽은
// 아무것도 하지 않는다.
func (c *conversation) emitInterruptedProviderTurn() {
	c.mu.Lock()
	closing := c.interruptedTurn
	c.interruptedTurn = nil
	c.mu.Unlock()
	c.emitClosingTurn(closing, domain.TurnStateInterrupted)
}

// armProviderInitiatedTimerLocked 는 자율 턴의 seq 를 잡은 타이머를 새로 건다.
// 앞 타이머는 멈춘다. 이미 울려 turnEmitMu 를 기다리는 앞 타이머는 seq 가
// 달라져 아무것도 하지 않는다. c.mu 를 쥐고 부른다.
func (c *conversation) armProviderInitiatedTimerLocked(turn *providerInitiatedTurn) {
	if turn.timer != nil {
		turn.timer.Stop()
	}
	id, seq := turn.id, turn.seq
	turn.timer = time.AfterFunc(c.providerInitiatedQuietPeriod(), func() {
		c.closeProviderInitiatedTurnAfterQuiet(id, seq)
	})
}

// bindProviderInitiatedTurn 은 processUpdate 가 갱신을 묶을 턴 id 를 정한다.
// 사용자 턴이 있으면 그 턴이다. 열린 자율 턴이 있으면 그 턴을 연장한다.
// 둘 다 없고 provider 출력이 다시 시작된 갱신(resumed)이면 자율 턴을 연다.
//
// 자율 턴에 묶인 경우 돌려준 release 를 부를 때까지 turnEmitMu 를 쥔다. 그래야
// 갱신을 처리하는 동안 다른 턴이 자율 턴을 떼어 내 항목 map 을 바꾸지 못하고,
// 자율 TurnStarted 가 그 턴의 출력보다 먼저 나간다.
func (c *conversation) bindProviderInitiatedTurn(resumed bool) (string, func()) {
	noop := func() {}
	if !c.providerInitiatedTurns || c.historyReplayActive() {
		c.mu.Lock()
		turnID := c.activeTurn
		c.mu.Unlock()
		return turnID, noop
	}
	c.turnEmitMu.Lock()
	// turnEmitMu 를 기다리는 동안 loadHistory 가 재생을 시작했을 수 있다.
	// 재생 중의 갱신은 history 로 잡히므로 자율 턴을 열지 않는다.
	if c.historyReplayActive() {
		c.turnEmitMu.Unlock()
		c.mu.Lock()
		turnID := c.activeTurn
		c.mu.Unlock()
		return turnID, noop
	}
	// Interrupt 가 떼어 둔 턴의 닫힘을 이 갱신이 열 새 자율 턴보다 먼저 낸다.
	c.emitInterruptedProviderTurn()
	c.mu.Lock()
	if c.closed || c.activeTurn != "" {
		turnID := c.activeTurn
		c.mu.Unlock()
		c.turnEmitMu.Unlock()
		return turnID, noop
	}
	if turn := c.autoTurn; turn != nil {
		turn.seq++
		turn.openToolWaits = 0
		c.armProviderInitiatedTimerLocked(turn)
		c.mu.Unlock()
		return turn.id, c.turnEmitMu.Unlock
	}
	if !resumed || c.prepared != nil || c.compactingTurnID != "" {
		c.mu.Unlock()
		c.turnEmitMu.Unlock()
		return "", noop
	}
	turn := &providerInitiatedTurn{id: ports.ProviderInitiatedTurnPrefix + uuid.NewString()}
	c.autoTurn = turn
	c.resetTurnItemsLocked()
	c.armProviderInitiatedTimerLocked(turn)
	c.mu.Unlock()
	c.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.id})
	return turn.id, c.turnEmitMu.Unlock
}

// providerInitiatedRequestPendingLocked 는 대기 중인 승인이나 입력이 있는지
// 알려 준다. 사람이 답할 일이므로 이것이 있는 동안은 기다리는 횟수와 관계없이
// 자율 턴을 닫지 않는다. c.mu 를 쥐고 부른다.
func (c *conversation) providerInitiatedRequestPendingLocked() bool {
	return len(c.pending) > 0 || len(c.pendingInputs) > 0
}

// providerInitiatedToolOpenLocked 는 이 자율 턴에서 tool_call 로 알린 도구 가운데
// 끝나지 않은 것이 있는지 알려 준다. 모르는 id 의 tool_call_update 로 생긴
// 항목은 그 턴이 시작한 도구가 아니고 끝날 갱신이 온다는 보장도 없으므로 세지
// 않는다. c.mu 를 쥐고 부른다.
func (c *conversation) providerInitiatedToolOpenLocked() bool {
	for _, tool := range c.tools {
		if tool.announced && toolStillOpen(tool.status) {
			return true
		}
	}
	return false
}

// closeProviderInitiatedTurnAfterQuiet 은 타이머가 울렸을 때 자율 턴을 닫는다.
// 그 사이 갱신이 와 seq 가 바뀌었거나 턴이 이미 떼어졌으면 아무것도 하지
// 않는다. 대기 중인 요청이 있으면 같은 seq 로 타이머를 다시 건다. 알린 도구가
// 끝나지 않았으면 providerInitiatedOpenToolWaitLimit 번까지 다시 걸고, 그 뒤에는
// 닫는다. 닫을 때 settleTurnItems 가 끝나지 않은 도구를 실패로 정리한다.
func (c *conversation) closeProviderInitiatedTurnAfterQuiet(id string, seq uint64) {
	c.turnEmitMu.Lock()
	defer c.turnEmitMu.Unlock()
	c.mu.Lock()
	turn := c.autoTurn
	if c.closed || turn == nil || turn.id != id || turn.seq != seq {
		c.mu.Unlock()
		return
	}
	if c.providerInitiatedRequestPendingLocked() {
		c.armProviderInitiatedTimerLocked(turn)
		c.mu.Unlock()
		return
	}
	if c.providerInitiatedToolOpenLocked() && turn.openToolWaits < providerInitiatedOpenToolWaitLimit {
		turn.openToolWaits++
		c.armProviderInitiatedTimerLocked(turn)
		c.mu.Unlock()
		return
	}
	closing := c.takeProviderInitiatedTurnLocked()
	c.mu.Unlock()
	c.emitClosingTurn(closing, domain.TurnStateCompleted)
}

// interruptProviderInitiatedTurn 은 자율 id 로 온 Interrupt 를 처리한다.
// provider 가 스스로 연 사이클은 AO 의 session/prompt 가 아니므로
// session/cancel 은 보내지 않고, 기록만 interrupted 로 닫는다. 닫는 emit 은
// 따로 띄운 goroutine 에서 한다. Interrupt 를 부른 controller 가 sendMu 를 쥔
// 채 기다리지 않게 하기 위해서다. 떼어 낸 턴은 interruptedTurn 에 맡겨, 그
// goroutine 보다 먼저 turnEmitMu 를 잡은 새 턴이 자기 TurnStarted 앞에서 대신
// 내게 한다(emitInterruptedProviderTurn). 처리했으면 true 다.
//
// 맡겨 둔 턴은 많아야 하나다. 새 자율 턴은 turnEmitMu 아래에서 맡겨 둔 턴을
// 먼저 낸 뒤에만 열리고, Interrupt 는 열린 자율 턴이 있어야 맡긴다.
//
// 한계: 그 순간 처리 중이던 자율 갱신의 항목은 떼어 낸 map 에 들어가지 못해
// 완료 이벤트 없이 남을 수 있다.
func (c *conversation) interruptProviderInitiatedTurn(providerTurnID string) bool {
	c.mu.Lock()
	turn := c.autoTurn
	if c.activeTurn != "" || turn == nil || providerTurnID == "" || providerTurnID != turn.id {
		c.mu.Unlock()
		return false
	}
	c.interruptedTurn = c.takeProviderInitiatedTurnLocked()
	c.mu.Unlock()
	go func() {
		c.turnEmitMu.Lock()
		defer c.turnEmitMu.Unlock()
		c.emitInterruptedProviderTurn()
	}()
	return true
}
