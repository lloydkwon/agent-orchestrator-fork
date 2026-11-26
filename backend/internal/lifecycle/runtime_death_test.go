package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// 런타임이 죽어 끝낸 세션은 그렇게 끝났다고 답하고, 사람이 끝내는 길
// (MarkTerminated)을 지나면 더는 그렇게 답하지 않는다.
func TestTerminatedByRuntimeDeath(t *testing.T) {
	dead := ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}

	t.Run("runtime death", func(t *testing.T) {
		m, st, _ := newManager()
		rec := working("mer-1")
		rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
		st.sessions["mer-1"] = rec
		if m.TerminatedByRuntimeDeath("mer-1") {
			t.Fatal("a live session reported a runtime death")
		}
		if err := m.ApplyRuntimeObservation(ctx, "mer-1", dead); err != nil {
			t.Fatal(err)
		}
		if !st.sessions["mer-1"].IsTerminated || !m.TerminatedByRuntimeDeath("mer-1") {
			t.Fatal("a session its runtime death ended must report it")
		}
		// 죽은 세션을 사람이 다시 끝내면(kill) 사람이 끝낸 것이 된다.
		if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
			t.Fatal(err)
		}
		if m.TerminatedByRuntimeDeath("mer-1") {
			t.Fatal("a session killed after its runtime died must not report a runtime death")
		}
	})

	t.Run("ended by hand", func(t *testing.T) {
		m, st, _ := newManager()
		st.sessions["mer-1"] = working("mer-1")
		if err := m.MarkTerminated(ctx, "mer-1"); err != nil {
			t.Fatal(err)
		}
		if m.TerminatedByRuntimeDeath("mer-1") {
			t.Fatal("a session ended by hand reported a runtime death")
		}
	})

	// kill 이 런타임을 죽인 뒤 끝내기에 실패해도, 그 죽음은 사람이 끝낸 것이다.
	t.Run("death after an end began by hand", func(t *testing.T) {
		m, st, _ := newManager()
		rec := working("mer-1")
		rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
		st.sessions["mer-1"] = rec
		m.MarkEndingByHand("mer-1")
		if err := m.ApplyRuntimeObservation(ctx, "mer-1", dead); err != nil {
			t.Fatal(err)
		}
		if !st.sessions["mer-1"].IsTerminated || m.TerminatedByRuntimeDeath("mer-1") {
			t.Fatal("a death after an end began by hand must not be taken for a crash")
		}
	})

	t.Run("recent activity keeps it live", func(t *testing.T) {
		m, st, _ := newManager()
		st.sessions["mer-1"] = working("mer-1")
		if err := m.ApplyRuntimeObservation(ctx, "mer-1", dead); err != nil {
			t.Fatal(err)
		}
		if st.sessions["mer-1"].IsTerminated || m.TerminatedByRuntimeDeath("mer-1") {
			t.Fatal("a session with recent activity was ended or reported dead")
		}
	})
}
