package sessionmanager

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// handEndLCM 은 사람이 끝내기 시작한 세션을 기억하는 가짜 lifecycle 이다.
type handEndLCM struct {
	*fakeLCM
	ended []domain.SessionID
}

func (l *handEndLCM) MarkEndingByHand(id domain.SessionID) { l.ended = append(l.ended, id) }

// kill 과 교체는 런타임을 죽이기 전에 «사람이 끝냄»을 알린다. 그래야 그 뒤에
// 끝내기가 실패해도 런타임 관찰이 그 죽음을 재부팅 같은 사고로 보지 않고,
// 다음 오케스트레이터 요청이 사람이 끝낸 세션을 되살리지 않는다.
func TestEndingByHandIsRecordedBeforeTheRuntimeIsDestroyed(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(m *Manager, id domain.SessionID) error
	}{
		{name: "kill", end: func(m *Manager, id domain.SessionID) error { _, err := m.Kill(ctx, id); return err }},
		{name: "replacement", end: func(m *Manager, id domain.SessionID) error { return m.RetireForReplacement(ctx, id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, rt, _ := newManager()
			lcm := &handEndLCM{fakeLCM: m.lcm.(*fakeLCM)}
			m.lcm = lcm
			rec := mkLive("mer-1")
			st.sessions[rec.ID] = rec
			markedAtDestroy := -1
			rt.onDestroy = func(int, ports.RuntimeHandle) { markedAtDestroy = len(lcm.ended) }

			if err := tc.end(m, rec.ID); err != nil {
				t.Fatalf("end: %v", err)
			}
			if rt.destroyed == 0 || markedAtDestroy != 1 || lcm.ended[0] != rec.ID {
				t.Fatalf("destroyed %d, marked %d before destroy (%v); want the end marked once before the runtime went",
					rt.destroyed, markedAtDestroy, lcm.ended)
			}
		})
	}
}
