package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// 데몬이 막 뜬 동안에는, 재부팅으로 런타임이 사라진 세션을 시작 점검이 다시
// 띄울 수 있다. 그동안 감시기가 본 «런타임 죽음»은 세션을 종료로 바꾸지 않고,
// 보류가 풀린 뒤의 관찰은 지금까지처럼 종료로 바꾼다.
func TestRuntimeObservation_LadoHoldDefersTerminationUntilReleased(t *testing.T) {
	m, st, _ := newManager()
	held := true
	m.SetLadoRuntimeDeathHold(func() bool { return held })
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	st.sessions["mer-1"] = rec
	dead := ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}

	if err := m.ApplyRuntimeObservation(ctx, "mer-1", dead); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; got.IsTerminated || got.Activity.State != domain.ActivityActive {
		t.Fatalf("보류 중에 세션이 바뀌었다: %+v", got)
	}

	held = false
	if err := m.ApplyRuntimeObservation(ctx, "mer-1", dead); err != nil {
		t.Fatal(err)
	}
	if got := st.sessions["mer-1"]; !got.IsTerminated || got.Activity.State != domain.ActivityExited {
		t.Fatalf("보류가 풀린 뒤에는 종료로 바뀌어야 한다: %+v", got)
	}
}
