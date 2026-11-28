package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// runtimeDeathCommander 는 런타임이 죽어 끝난 세션을 아는 commander 다.
type runtimeDeathCommander struct {
	*fakeCommander
	died     map[domain.SessionID]bool
	restored []domain.SessionID
	// onRestore 는 복원이 돌려주기 전에 돈다(복원 중에 일어난 일을 흉내 낸다).
	onRestore func(id domain.SessionID)
}

func (f *runtimeDeathCommander) TerminatedByRuntimeDeath(id domain.SessionID) bool {
	return f.died[id]
}

func (f *runtimeDeathCommander) RestoreWithMode(ctx context.Context, id domain.SessionID) (sessionmanager.RestoreResult, error) {
	f.restored = append(f.restored, id)
	if f.onRestore != nil {
		f.onRestore(id)
	}
	return f.fakeCommander.RestoreWithMode(ctx, id)
}

// deadOrchestratorFixture 는 터미널 오케스트레이터 하나가 끝난 프로젝트다.
// older 가 있으면 그보다 먼저 만든, 역시 끝난 오케스트레이터를 더한다.
func deadOrchestratorFixture(died map[domain.SessionID]bool, withOlder bool) (*Service, *runtimeDeathCommander) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer"}
	created := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
	st.sessions["mer-4"] = domain.SessionRecord{
		ID: "mer-4", ProjectID: "mer", Kind: domain.KindOrchestrator,
		Mode: domain.SessionModeTUI, IsTerminated: true, CreatedAt: created,
	}
	if withOlder {
		st.sessions["mer-1"] = domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer", Kind: domain.KindOrchestrator,
			Mode: domain.SessionModeTUI, IsTerminated: true, CreatedAt: created.Add(-time.Hour),
		}
	}
	fc := &runtimeDeathCommander{
		fakeCommander: &fakeCommander{
			spawnRecord: domain.SessionRecord{ID: "mer-24", ProjectID: "mer", Kind: domain.KindOrchestrator},
			restoreResult: sessionmanager.RestoreResult{Session: domain.SessionRecord{
				ID: "mer-4", ProjectID: "mer", Kind: domain.KindOrchestrator, Mode: domain.SessionModeTUI,
			}},
		},
		died: died,
	}
	return &Service{manager: fc, store: st}, fc
}

// 재부팅 등으로 런타임이 죽어 끝난 직전 오케스트레이터는 새로 만들지 않고
// 복원해 예전 대화를 잇는다.
func TestSpawnOrchestratorRestoresOrchestratorWhoseRuntimeDied(t *testing.T) {
	svc, fc := deadOrchestratorFixture(map[domain.SessionID]bool{"mer-4": true}, false)

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "", "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-4" || got.Mode != domain.SessionModeTUI {
		t.Fatalf("got %s (%s), want the restored terminal orchestrator mer-4", got.ID, got.Mode)
	}
	if len(fc.restored) != 1 || fc.restored[0] != "mer-4" || fc.spawned {
		t.Fatalf("restored %v, spawned %v; want mer-4 restored and nothing spawned", fc.restored, fc.spawned)
	}
}

// 사람이 끝낸 직전 오케스트레이터는 지금처럼 새로 만든다.
func TestSpawnOrchestratorSpawnsAfterAnOrchestratorEndedByHand(t *testing.T) {
	svc, fc := deadOrchestratorFixture(nil, false)

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "", "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-24" || len(fc.restored) != 0 || !fc.spawned {
		t.Fatalf("got %s, restored %v, spawned %v; want a new orchestrator", got.ID, fc.restored, fc.spawned)
	}
}

// 가장 최근 오케스트레이터가 판단한다: 그보다 오래된 것이 런타임 사망으로
// 끝났어도, 최근 것을 사람이 끝냈으면 새로 만든다.
func TestSpawnOrchestratorLooksOnlyAtTheNewestOrchestrator(t *testing.T) {
	svc, fc := deadOrchestratorFixture(map[domain.SessionID]bool{"mer-1": true}, true)

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "", "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-24" || len(fc.restored) != 0 {
		t.Fatalf("got %s, restored %v; want a new orchestrator", got.ID, fc.restored)
	}
}

// 모드를 명시한 요청은 그 모드로 새로 만든다.
func TestSpawnOrchestratorWithExplicitModeDoesNotRestore(t *testing.T) {
	svc, fc := deadOrchestratorFixture(map[domain.SessionID]bool{"mer-4": true}, false)

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, domain.SessionModeChat, "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-24" || len(fc.restored) != 0 || fc.spawnedCfg.RequestedMode != domain.SessionModeChat {
		t.Fatalf("got %s, restored %v, mode %q; want a new Chat orchestrator", got.ID, fc.restored, fc.spawnedCfg.RequestedMode)
	}
}

// 복원이 실패하면 지금처럼 새로 만든다.
func TestSpawnOrchestratorSpawnsWhenRestoreFails(t *testing.T) {
	svc, fc := deadOrchestratorFixture(map[domain.SessionID]bool{"mer-4": true}, false)
	fc.restoreErr = errors.New("workspace gone")

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "", "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-24" || len(fc.restored) != 1 || !fc.spawned {
		t.Fatalf("got %s, restored %v, spawned %v; want a tried restore then a new orchestrator", got.ID, fc.restored, fc.spawned)
	}
}

// 복원은 됐는데 오류가 돌아오면, 살아난 그 오케스트레이터를 돌려주고 새로
// 만들지 않는다. 새로 만들면 오케스트레이터가 둘이 된다.
func TestSpawnOrchestratorKeepsARestoredOrchestratorWhenRestoreReportsAnError(t *testing.T) {
	svc, fc := deadOrchestratorFixture(map[domain.SessionID]bool{"mer-4": true}, false)
	fc.restoreErr = errors.New("re-read after relaunch failed")
	st := svc.store.(*fakeStore)
	// 다시 띄우기는 됐고(살아남), 그 뒤 읽기에서 오류가 났다.
	fc.onRestore = func(id domain.SessionID) {
		rec := st.sessions[id]
		rec.IsTerminated = false
		st.sessions[id] = rec
	}

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "", "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-4" || fc.spawned || len(fc.restored) != 1 {
		t.Fatalf("got %s, spawned %v, restored %v; want the live restored mer-4 and no second orchestrator", got.ID, fc.spawned, fc.restored)
	}
}

// 권한을 정해 보낸 요청은 그 권한으로 새로 만든다.
func TestSpawnOrchestratorWithApprovalDoesNotRestore(t *testing.T) {
	svc, fc := deadOrchestratorFixture(map[domain.SessionID]bool{"mer-4": true}, false)

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "", domain.PermissionModeDefault)
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-24" || len(fc.restored) != 0 {
		t.Fatalf("got %s, restored %v; want a new orchestrator", got.ID, fc.restored)
	}
}

// 죽은 뒤 프로젝트의 에이전트를 바꿨으면 새 설정으로 새로 만든다.
func TestSpawnOrchestratorDoesNotRestoreAnotherHarness(t *testing.T) {
	svc, fc := deadOrchestratorFixture(map[domain.SessionID]bool{"mer-4": true}, false)
	st := svc.store.(*fakeStore)
	project := st.projects["mer"]
	project.Config.Orchestrator.Harness = domain.HarnessCodex
	st.projects["mer"] = project
	rec := st.sessions["mer-4"]
	rec.Harness = domain.HarnessClaudeCode
	st.sessions["mer-4"] = rec
	fc.spawnRecord.Harness = domain.HarnessCodex

	got, err := svc.SpawnOrchestrator(context.Background(), "mer", false, "", "")
	if err != nil {
		t.Fatalf("SpawnOrchestrator: %v", err)
	}
	if got.ID != "mer-24" || len(fc.restored) != 0 {
		t.Fatalf("got %s, restored %v; want a new orchestrator on the new harness", got.ID, fc.restored)
	}
}
