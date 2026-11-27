package sessionmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// modeLadoSource gives an orchestrator LADO's section while its project's
// LADO mode is on, as ladotask.PromptSource.Standing reads the mode at
// launch.
type modeLadoSource struct {
	mu sync.Mutex
	on map[domain.ProjectID]bool
}

func (s *modeLadoSource) set(projectID domain.ProjectID, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.on == nil {
		s.on = map[domain.ProjectID]bool{}
	}
	s.on[projectID] = on
}

func (s *modeLadoSource) section(_ context.Context, kind domain.SessionKind, projectID domain.ProjectID, _ domain.SessionID, _ domain.AgentHarness) (domain.LadoStandingSection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind != domain.KindOrchestrator || !s.on[projectID] {
		return domain.LadoStandingSection{}, nil
	}
	return domain.LadoStandingSection{Text: ladoOrchestratorSection, ReplacesOrchestratorRole: true}, nil
}

// releasingChat stops a quiet provider as chat.Service.ReleaseIdleChat does:
// the session is marked hibernated and its controller goes away.
type releasingChat struct {
	*recordingLauncher
	store    *fakeStore
	now      func() time.Time
	released []domain.SessionID
}

func (c *releasingChat) ReleaseIdleChat(_ context.Context, id domain.SessionID, quiet time.Duration) (bool, error) {
	rec, ok := c.store.sessions[id]
	if !ok || !c.live || !rec.EligibleForChatRelease() || rec.Activity.LastActivityAt.Add(quiet).After(c.now()) {
		return false, nil
	}
	at := c.now()
	rec.HibernatedAt = &at
	rec.Revision++
	c.store.sessions[id] = rec
	c.live = false
	c.released = append(c.released, id)
	return true, nil
}

// StartChat resumes the conversation it is asked to, as a provider does,
// rather than recordingLauncher's fixed "thread-1".
func (c *releasingChat) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	cfg, err := prepareTestChatStart(ctx, cfg)
	if err != nil {
		return ChatStarted{}, err
	}
	c.started = append(c.started, cfg)
	started := ChatStarted{
		ProviderConversationID: cfg.ProviderConversationID,
		ControllerGeneration:   fmt.Sprintf("gen-%d", len(c.started)),
	}
	if cfg.ControllerGeneration != "" {
		started.ControllerGeneration = cfg.ControllerGeneration
	}
	if cfg.ControllerReady != nil {
		if _, err := cfg.ControllerReady(started); err != nil {
			return ChatStarted{}, err
		}
	}
	c.live = true
	return started, nil
}

var instructionRefreshNow = time.Date(2026, 11, 27, 6, 0, 0, 0, time.UTC)

func newChatRefreshManager(t *testing.T) (*Manager, *fakeStore, *releasingChat, *modeLadoSource) {
	t.Helper()
	chat := &releasingChat{recordingLauncher: &recordingLauncher{live: true}, now: func() time.Time { return instructionRefreshNow }}
	mgr, store, _ := newChatManager(chat)
	chat.store = store
	mgr.clock = func() time.Time { return instructionRefreshNow }
	source := &modeLadoSource{}
	mgr.SetLadoPrompt(source.section)
	store.projects["other"] = domain.ProjectRecord{ID: "other", Config: testRoleAgents()}
	for _, seed := range []struct {
		id      domain.SessionID
		project domain.ProjectID
		kind    domain.SessionKind
	}{
		{"mer-orchestrator-1", chatTestProject, domain.KindOrchestrator},
		{"mer-1", chatTestProject, domain.KindWorker},
		{"other-orchestrator-1", "other", domain.KindOrchestrator},
	} {
		store.sessions[seed.id] = domain.SessionRecord{
			ID: seed.id, ProjectID: seed.project, Kind: seed.kind,
			Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
			Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: instructionRefreshNow.Add(-time.Minute)},
			Metadata: domain.SessionMetadata{
				WorkspacePath: "/ws/" + string(seed.id), Branch: "ao/" + string(seed.id),
				ProviderConversationID: "thread-" + string(seed.id), ControllerGeneration: "gen-0",
			},
		}
	}
	return mgr, store, chat, source
}

// changeLadoMode turns the project's LADO mode on or off as the project
// settings service does, then lets the daemon's loop try once.
func changeLadoMode(t *testing.T, mgr *Manager, source *modeLadoSource, projectID domain.ProjectID, on bool) int {
	t.Helper()
	source.set(projectID, on)
	mgr.NoteLadoModeChanged(projectID)
	waiting, err := mgr.RefreshPendingInstructions(context.Background())
	if err != nil {
		t.Fatalf("RefreshPendingInstructions: %v", err)
	}
	return waiting
}

// quietChat makes a relaunched Chat session look settled again.
func quietChat(store *fakeStore, id domain.SessionID) {
	rec := store.sessions[id]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: instructionRefreshNow.Add(-time.Minute)}
	store.sessions[id] = rec
}

func TestLadoModeChangeRefreshesTheLiveChatOrchestrator(t *testing.T) {
	mgr, store, chat, source := newChatRefreshManager(t)
	const id = domain.SessionID("mer-orchestrator-1")

	if waiting := changeLadoMode(t, mgr, source, chatTestProject, true); waiting != 0 {
		t.Fatalf("waiting = %d after turning LADO on, want 0", waiting)
	}
	if len(chat.started) != 1 || chat.started[0].SessionID != id {
		t.Fatalf("started = %+v, want the mer orchestrator relaunched once", chat.started)
	}
	on := chat.started[0]
	assertLadoOrchestratorPrompt(t, on.SystemPrompt)
	if on.ProviderConversationID != "thread-"+string(id) {
		t.Fatalf("relaunch resumed %q, want the orchestrator's own conversation", on.ProviderConversationID)
	}
	if got := store.sessions[id]; got.HibernatedAt != nil || got.IsTerminated || got.Metadata.ProviderConversationID != "thread-"+string(id) {
		t.Fatalf("session after refresh = %+v, want the same live session and conversation", got)
	}

	quietChat(store, id)
	if waiting := changeLadoMode(t, mgr, source, chatTestProject, false); waiting != 0 {
		t.Fatalf("waiting = %d after turning LADO off, want 0", waiting)
	}
	if len(chat.started) != 2 || chat.started[1].SessionID != id {
		t.Fatalf("started = %+v, want the mer orchestrator relaunched again", chat.started)
	}
	off := chat.started[1]
	if strings.Contains(off.SystemPrompt, ladoOrchestratorSection) {
		t.Fatalf("prompt after turning LADO off still has its section:\n%s", off.SystemPrompt)
	}
	if !strings.Contains(off.SystemPrompt, "always spawn or redirect a worker session") {
		t.Fatalf("prompt after turning LADO off lacks AO's orchestrator rules:\n%s", off.SystemPrompt)
	}
	if off.ProviderConversationID != "thread-"+string(id) {
		t.Fatalf("relaunch resumed %q, want the orchestrator's own conversation", off.ProviderConversationID)
	}
	// The worker and the other project's orchestrator keep running as they are.
	if len(chat.released) != 2 || chat.released[0] != id || chat.released[1] != id {
		t.Fatalf("released = %v, want only %s, twice", chat.released, id)
	}
}

func TestLadoModeChangeWaitsForTheChatOrchestratorTurn(t *testing.T) {
	mgr, store, chat, source := newChatRefreshManager(t)
	const id = domain.SessionID("mer-orchestrator-1")
	working := store.sessions[id]
	working.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: instructionRefreshNow}
	store.sessions[id] = working

	if waiting := changeLadoMode(t, mgr, source, chatTestProject, true); waiting != 1 {
		t.Fatalf("waiting = %d while the orchestrator works, want 1", waiting)
	}
	if len(chat.released) != 0 || len(chat.started) != 0 {
		t.Fatalf("released %v, started %d during a turn; want neither", chat.released, len(chat.started))
	}

	// The turn ends; activity just now still counts as busy.
	justDone := store.sessions[id]
	justDone.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: instructionRefreshNow.Add(-time.Second)}
	store.sessions[id] = justDone
	if waiting, err := mgr.RefreshPendingInstructions(context.Background()); err != nil || waiting != 1 {
		t.Fatalf("RefreshPendingInstructions right after the turn = %d (%v), want 1 waiting", waiting, err)
	}

	quietChat(store, id)
	if waiting, err := mgr.RefreshPendingInstructions(context.Background()); err != nil || waiting != 0 {
		t.Fatalf("RefreshPendingInstructions once quiet = %d (%v), want 0 waiting", waiting, err)
	}
	if len(chat.started) != 1 {
		t.Fatalf("started = %d, want the orchestrator relaunched once it was quiet", len(chat.started))
	}
	assertLadoOrchestratorPrompt(t, chat.started[0].SystemPrompt)
}

func TestLadoModeChangeSkipsAnOrchestratorAlreadyRelaunched(t *testing.T) {
	mgr, store, chat, source := newChatRefreshManager(t)
	const id = domain.SessionID("mer-orchestrator-1")
	working := store.sessions[id]
	working.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: instructionRefreshNow}
	store.sessions[id] = working
	if waiting := changeLadoMode(t, mgr, source, chatTestProject, true); waiting != 1 {
		t.Fatalf("waiting = %d while the orchestrator works, want 1", waiting)
	}

	// Something else relaunched it meanwhile, which built its instructions.
	relaunched := store.sessions[id]
	relaunched.Metadata.ControllerGeneration = "gen-9"
	relaunched.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: instructionRefreshNow.Add(-time.Minute)}
	store.sessions[id] = relaunched
	if waiting, err := mgr.RefreshPendingInstructions(context.Background()); err != nil || waiting != 0 {
		t.Fatalf("RefreshPendingInstructions = %d (%v), want 0 waiting", waiting, err)
	}
	if len(chat.released) != 0 || len(chat.started) != 0 {
		t.Fatalf("released %v, started %d; want the relaunched orchestrator left alone", chat.released, len(chat.started))
	}
}

func TestRefreshOrchestratorInstructionsLeavesOtherSessionsAlone(t *testing.T) {
	mgr, store, chat, _ := newChatRefreshManager(t)
	ctx := context.Background()

	if outcome, err := mgr.RefreshOrchestratorInstructions(ctx, "mer-1"); err != nil || outcome != InstructionRefreshUnsupported {
		t.Fatalf("worker outcome = %q (%v), want %q", outcome, err, InstructionRefreshUnsupported)
	}
	ended := store.sessions["other-orchestrator-1"]
	ended.IsTerminated = true
	store.sessions[ended.ID] = ended
	if outcome, err := mgr.RefreshOrchestratorInstructions(ctx, ended.ID); err != nil || outcome != InstructionRefreshNotRunning {
		t.Fatalf("ended orchestrator outcome = %q (%v), want %q", outcome, err, InstructionRefreshNotRunning)
	}
	if outcome, err := mgr.RefreshOrchestratorInstructions(ctx, "missing"); err != nil || outcome != InstructionRefreshNotRunning {
		t.Fatalf("missing session outcome = %q (%v), want %q", outcome, err, InstructionRefreshNotRunning)
	}
	if len(chat.released) != 0 || len(chat.started) != 0 {
		t.Fatalf("released %v, started %d; want nothing relaunched", chat.released, len(chat.started))
	}
}

// refreshTUIAgent records the standing instructions each TUI relaunch gets.
type refreshTUIAgent struct {
	transitionSurfaceAgent
	mu       *sync.Mutex
	restored *[]ports.RestoreConfig
	// noNativeRestore makes every native resume unavailable, as when the
	// provider lost the conversation's files.
	noNativeRestore bool
}

func (a refreshTUIAgent) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	a.mu.Lock()
	*a.restored = append(*a.restored, cfg)
	a.mu.Unlock()
	if a.noNativeRestore {
		return nil, false, nil
	}
	return a.transitionSurfaceAgent.GetRestoreCommand(ctx, cfg)
}

func newTUIRefreshManager(t *testing.T) (*Manager, *transitionStore, *transitionRuntime, *modeLadoSource, func() []ports.RestoreConfig) {
	t.Helper()
	mgr, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(mgr)
	mgr.clock = func() time.Time { return instructionRefreshNow }
	mgr.startupBackgroundReconcileOnce.Do(func() { close(mgr.startupBackgroundReconcileDone) })
	var mu sync.Mutex
	var restored []ports.RestoreConfig
	mgr.agents = singleAgent{agent: refreshTUIAgent{mu: &mu, restored: &restored}}
	source := &modeLadoSource{}
	mgr.SetLadoPrompt(source.section)
	store.projects["proj"] = domain.ProjectRecord{ID: "proj", Path: "/repo", Config: testRoleAgents()}
	rec := store.sessions["session-1"]
	rec.Kind = domain.KindOrchestrator
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: instructionRefreshNow.Add(-time.Minute)}
	rec.FirstSignalAt = instructionRefreshNow.Add(-time.Hour)
	store.sessions[rec.ID] = rec
	runtime.runtimeOccupied = true
	runtime.outputForCall = func(int) string { return idleTerminalOutput }
	return mgr, store, runtime, source, func() []ports.RestoreConfig {
		mu.Lock()
		defer mu.Unlock()
		return append([]ports.RestoreConfig(nil), restored...)
	}
}

// settleTUI makes a relaunched TUI agent report its native conversation
// again and go quiet, as its hooks do.
func settleTUI(store *transitionStore, id domain.SessionID) {
	rec := store.sessions[id]
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: instructionRefreshNow.Add(-time.Minute)}
	rec.FirstSignalAt = instructionRefreshNow.Add(-30 * time.Second)
	rec.Metadata.AgentSessionID = "native-1"
	rec.Metadata.AgentSessionIDLaunchID = rec.Metadata.RuntimeLaunchID
	store.sessions[id] = rec
}

func TestLadoModeChangeRefreshesTheLiveTUIOrchestrator(t *testing.T) {
	mgr, store, runtime, source, restored := newTUIRefreshManager(t)
	const id = domain.SessionID("session-1")

	if waiting := changeLadoMode(t, mgr, source, "proj", true); waiting != 0 {
		t.Fatalf("waiting = %d after turning LADO on, want 0", waiting)
	}
	configs := restored()
	if len(configs) != 1 {
		t.Fatalf("restores = %d, want the orchestrator relaunched once", len(configs))
	}
	if !strings.Contains(configs[0].SystemPrompt, ladoOrchestratorSection) {
		t.Fatalf("relaunch prompt lacks LADO's section:\n%s", configs[0].SystemPrompt)
	}
	if got := configs[0].Session.Metadata[ports.MetadataKeyAgentSessionID]; got != "native-1" {
		t.Fatalf("relaunch resumed %q, want the orchestrator's native conversation", got)
	}
	after := store.sessions[id]
	if after.IsTerminated || after.Activity.State == domain.ActivityExited || after.Metadata.RuntimeLaunchID == "old-tui-generation" {
		t.Fatalf("session after refresh = %+v, want the same session on a new launch", after)
	}
	if got := strings.Join(*runtime.log, ","); !strings.Contains(got, "stop:tui:runtime-1") || !strings.HasSuffix(got, "start:tui") {
		t.Fatalf("runtime log = %s, want the old terminal stopped and a new one started", got)
	}

	settleTUI(store, id)
	if waiting := changeLadoMode(t, mgr, source, "proj", false); waiting != 0 {
		t.Fatalf("waiting = %d after turning LADO off, want 0", waiting)
	}
	configs = restored()
	if len(configs) != 2 {
		t.Fatalf("restores = %d, want the orchestrator relaunched again", len(configs))
	}
	if strings.Contains(configs[1].SystemPrompt, ladoOrchestratorSection) {
		t.Fatalf("prompt after turning LADO off still has its section:\n%s", configs[1].SystemPrompt)
	}
	if got := configs[1].Session.Metadata[ports.MetadataKeyAgentSessionID]; got != "native-1" {
		t.Fatalf("relaunch resumed %q, want the orchestrator's native conversation", got)
	}
}

// A native resume that fails after the stop must not leave the project
// without an orchestrator: the refresh launches it the way Resume would.
func TestLadoModeChangeRelaunchesATUIOrchestratorWhoseConversationCannotResume(t *testing.T) {
	mgr, store, runtime, source, restored := newTUIRefreshManager(t)
	const id = domain.SessionID("session-1")
	agent := mgr.agents.(singleAgent).agent.(refreshTUIAgent)
	agent.noNativeRestore = true
	mgr.agents = singleAgent{agent: agent}

	if waiting := changeLadoMode(t, mgr, source, "proj", true); waiting != 0 {
		t.Fatalf("waiting = %d after turning LADO on, want 0", waiting)
	}
	configs := restored()
	if len(configs) != 2 {
		t.Fatalf("restores = %d, want the native resume and then the Resume fallback", len(configs))
	}
	if !strings.Contains(configs[1].SystemPrompt, ladoOrchestratorSection) {
		t.Fatalf("fallback launch prompt lacks LADO's section:\n%s", configs[1].SystemPrompt)
	}
	after := store.sessions[id]
	if after.IsTerminated || after.Activity.State == domain.ActivityExited {
		t.Fatalf("session after refresh = %+v, want it running in the same session", after)
	}
	if got := strings.Join(*runtime.log, ","); !strings.Contains(got, "stop:tui:runtime-1") || !strings.HasSuffix(got, "start:tui") {
		t.Fatalf("runtime log = %s, want the old terminal stopped and a new one started", got)
	}
}

func TestLadoModeChangeWaitsForAQuietTUIOrchestrator(t *testing.T) {
	for name, tc := range map[string]struct {
		screen string
		change func(*domain.SessionRecord)
	}{
		"draft in the composer": {screen: draftTerminalOutput},
		"pending decision":      {screen: decisionTerminalOutput},
		"turn on screen":        {screen: activeTerminalOutput},
		"turn in progress": {screen: idleTerminalOutput, change: func(rec *domain.SessionRecord) {
			rec.Activity = domain.Activity{State: domain.ActivityActive, LastActivityAt: instructionRefreshNow}
		}},
		"activity just now": {screen: idleTerminalOutput, change: func(rec *domain.SessionRecord) {
			rec.Activity.LastActivityAt = instructionRefreshNow.Add(-time.Second)
		}},
		"no hook since launch": {screen: idleTerminalOutput, change: func(rec *domain.SessionRecord) {
			rec.FirstSignalAt = time.Time{}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			mgr, store, runtime, source, restored := newTUIRefreshManager(t)
			runtime.outputForCall = func(int) string { return tc.screen }
			if tc.change != nil {
				rec := store.sessions["session-1"]
				tc.change(&rec)
				store.sessions[rec.ID] = rec
			}
			if waiting := changeLadoMode(t, mgr, source, "proj", true); waiting != 1 {
				t.Fatalf("waiting = %d, want the orchestrator kept queued", waiting)
			}
			if len(restored()) != 0 || len(*runtime.log) != 0 {
				t.Fatalf("restores %d, runtime log %v; want the orchestrator untouched", len(restored()), *runtime.log)
			}

			// Once it is quiet the queued refresh goes through.
			runtime.outputForCall = func(int) string { return idleTerminalOutput }
			settleTUI(store, "session-1")
			if waiting, err := mgr.RefreshPendingInstructions(context.Background()); err != nil || waiting != 0 {
				t.Fatalf("RefreshPendingInstructions once quiet = %d (%v), want 0 waiting", waiting, err)
			}
			if configs := restored(); len(configs) != 1 || !strings.Contains(configs[0].SystemPrompt, ladoOrchestratorSection) {
				t.Fatalf("restores = %d, want one with LADO's section", len(configs))
			}
		})
	}
}

func TestRefreshTUIOrchestratorNeedsAReadableScreen(t *testing.T) {
	mgr, _, runtime, _, restored := newTUIRefreshManager(t)
	mgr.runtime = &unstyledTransitionRuntime{runtime: runtime}

	outcome, err := mgr.RefreshOrchestratorInstructions(context.Background(), "session-1")
	if err != nil || outcome != InstructionRefreshUnsupported {
		t.Fatalf("outcome = %q (%v), want %q", outcome, err, InstructionRefreshUnsupported)
	}
	if len(restored()) != 0 || len(*runtime.log) != 0 {
		t.Fatalf("restores %d, runtime log %v; want the orchestrator untouched", len(restored()), *runtime.log)
	}
}
