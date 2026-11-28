package chat

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// 자율 턴(provider-initiated turn)은 provider 가 사용자 턴 밖에서 스스로 연 턴이다. 예를 들어
// Claude Code 는 백그라운드 작업이 끝나면 <task-notification> 으로 새 사이클을 돈다. ACP 드라이버는
// 이런 턴에 ports.ProviderInitiatedTurnPrefix 로 시작하는 id 를 붙여 TurnStarted 와 TurnCompleted 를
// 낸다.
//
// AO 가 보낸 턴이 아니므로 controller 는 이 턴으로 Busy 를 세우거나 풀지 않는다. 세우면 그 사이
// 사용자가 보낸 메시지가 끝을 모르는 턴 뒤에 큐로 남는다. 대신 턴은 자기 행으로 기록하고, 그 시작과
// 끝을 세션 활동으로 보고해 lifecycle 이 일하는 세션을 멈춘 세션으로 보지 않게 한다.

// isProviderInitiatedTurnEvent 는 event 가 자율 턴의 시작이나 끝인지 알려 준다.
func isProviderInitiatedTurnEvent(event ports.ChatEvent) bool {
	if event.Kind != ports.ChatEventTurnStarted && event.Kind != ports.ChatEventTurnCompleted {
		return false
	}
	return ports.IsProviderInitiatedTurn(event.ProviderTurnID)
}

// ownsAOWork 는 AO 가 보낸 일(사용자 턴의 dispatch·진행, 요청한 compaction)이 지금 이 controller 를
// 차지하고 있는지 알려 준다. 그동안의 활동은 그 일이 보고하므로 자율 턴은 끼어들지 않는다.
func (c *Controller) ownsAOWork() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingTurnID != "" || c.dispatchingTurnID != "" || c.compactionPending
}

// afterProviderInitiatedTurn 은 커밋된 자율 턴의 시작과 끝을 세션 활동으로 보고한다. 사용자 턴의
// afterProject 와 달리 큐를 비우지 않는다. 자율 턴은 큐를 막은 적이 없기 때문이다.
func (c *Controller) afterProviderInitiatedTurn(ctx context.Context, event ports.ChatEvent, now time.Time) {
	switch event.Kind {
	case ports.ChatEventTurnStarted:
		if c.ownsAOWork() {
			return
		}
		c.reportActivity(ctx, domain.ActivityActive, "chat.turn.started", now)
	case ports.ChatEventTurnCompleted:
		// 재인증 요구는 어느 턴에서 왔든 계정 상태를 다시 읽게 한다.
		reauthRequired := errors.Is(event.Err, ports.ErrChatAuthRequired)
		if reauthRequired && c.onAccountChanged != nil {
			c.onAccountChanged(c.sessionID, c.generation, c.harness)
		}
		if c.ownsAOWork() {
			return
		}
		activityState := domain.ActivityIdle
		activityEvent := "chat.turn.completed"
		if reauthRequired {
			activityState = domain.ActivityWaitingInput
			activityEvent = "chat.account.reauth"
		}
		c.reportActivity(ctx, activityState, activityEvent, now)
	}
}
