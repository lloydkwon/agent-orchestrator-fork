package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// 실제 lifecycle 이 런타임 사망으로 끝낸 오케스트레이터를, 세션 서비스가 쓰는
// 실제 세션 관리자도 그렇게 끝났다고 답한다. 이 연결이 끊기면 재부팅 뒤
// 오케스트레이터가 복원되지 않고 다시 Chat 으로 새로 뜬다.
func TestWiring_SessionManagerReportsRuntimeDeathFromLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "mer", Path: "/repo/mer"}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "mer", Kind: domain.KindOrchestrator, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now().Add(-5 * time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}

	lcm := lifecycle.New(store, nil)
	mgr := sessionmanager.New(sessionmanager.Deps{Store: store, Lifecycle: lcm, DataDir: t.TempDir()})
	reporter, ok := any(mgr).(interface {
		TerminatedByRuntimeDeath(domain.SessionID) bool
	})
	if !ok {
		t.Fatal("the session manager does not report runtime deaths")
	}

	if err := lcm.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.GetSession(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsTerminated || !reporter.TerminatedByRuntimeDeath(rec.ID) {
		t.Fatalf("terminated %v, reported %v; want the runtime death seen through the session manager",
			got.IsTerminated, reporter.TerminatedByRuntimeDeath(rec.ID))
	}
}
