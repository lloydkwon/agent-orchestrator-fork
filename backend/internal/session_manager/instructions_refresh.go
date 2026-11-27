package sessionmanager

// LADO fork (#4): an orchestrator's standing instructions are built when its
// agent launches (buildSystemPrompt), so an orchestrator that was already
// running when its project's LADO mode changed keeps the instructions of the
// old mode. The daemon queues the project's live orchestrators when the mode
// changes and relaunches each one in its own AO session once it is quiet:
// the agent resumes the same native conversation with instructions built
// afresh. A turn in progress, a pending decision or a draft in a TUI composer
// keeps the orchestrator queued for a later try.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// instructionRefreshQuiet is how long an orchestrator must have been idle,
	// and its terminal free of typing, before it is relaunched.
	instructionRefreshQuiet = 5 * time.Second
	// instructionRefreshSurfaceSamples is how many idle screens with an empty
	// composer, taken a poll interval apart, prove a TUI quiet.
	instructionRefreshSurfaceSamples = 3
	// instructionRefreshStepLimit bounds the waits before an orchestrator is
	// stopped: admitted input draining and the chat provider's checks.
	instructionRefreshStepLimit = 10 * time.Second
)

// InstructionRefreshOutcome is the result of one try to relaunch an
// orchestrator with its standing instructions built afresh.
type InstructionRefreshOutcome string

const (
	// InstructionRefreshDone means the agent was relaunched in the same
	// session and conversation.
	InstructionRefreshDone InstructionRefreshOutcome = "refreshed"
	// InstructionRefreshNotRunning means no agent runs, or the queued launch
	// was already replaced: the next launch builds the instructions anyway.
	InstructionRefreshNotRunning InstructionRefreshOutcome = "not-running"
	// InstructionRefreshBusy means the agent is working, waiting for a
	// decision, holding a draft or being changed by another operation.
	InstructionRefreshBusy InstructionRefreshOutcome = "busy"
	// InstructionRefreshUnsupported means the agent cannot be relaunched
	// without risking its conversation or a draft, so it keeps its
	// instructions until it next launches.
	InstructionRefreshUnsupported InstructionRefreshOutcome = "unsupported"
)

// instructionRefreshQueue holds the projects whose LADO mode changed and the
// orchestrators waiting for a relaunch, each with the launch generation it
// was queued for. The zero value is ready to use.
type instructionRefreshQueue struct {
	mu       sync.Mutex
	projects map[domain.ProjectID]struct{}
	sessions map[domain.SessionID]string
}

func (q *instructionRefreshQueue) noteProject(projectID domain.ProjectID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.projects == nil {
		q.projects = map[domain.ProjectID]struct{}{}
	}
	q.projects[projectID] = struct{}{}
}

func (q *instructionRefreshQueue) takeProjects() []domain.ProjectID {
	q.mu.Lock()
	defer q.mu.Unlock()
	projects := make([]domain.ProjectID, 0, len(q.projects))
	for projectID := range q.projects {
		projects = append(projects, projectID)
	}
	q.projects = nil
	sort.Slice(projects, func(i, j int) bool { return projects[i] < projects[j] })
	return projects
}

func (q *instructionRefreshQueue) addSession(id domain.SessionID, generation string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.sessions == nil {
		q.sessions = map[domain.SessionID]string{}
	}
	q.sessions[id] = generation
}

// waiting returns the queued orchestrators in a stable order.
func (q *instructionRefreshQueue) waiting() []domain.SessionID {
	q.mu.Lock()
	defer q.mu.Unlock()
	ids := make([]domain.SessionID, 0, len(q.sessions))
	for id := range q.sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (q *instructionRefreshQueue) generation(id domain.SessionID) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	generation, ok := q.sessions[id]
	return generation, ok
}

// finish drops id unless it was queued again for another generation since
// the try read it.
func (q *instructionRefreshQueue) finish(id domain.SessionID, generation string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if current, ok := q.sessions[id]; ok && current == generation {
		delete(q.sessions, id)
	}
}

// agentGeneration names the agent launch an orchestrator runs: the runtime
// launch of a TUI, the controller generation of a Chat. Empty means none.
func agentGeneration(rec domain.SessionRecord) string {
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		return strings.TrimSpace(rec.Metadata.ControllerGeneration)
	}
	return strings.TrimSpace(rec.Metadata.RuntimeLaunchID)
}

// NoteLadoModeChanged queues the project's live orchestrators for a relaunch
// with standing instructions built afresh. It does no I/O, so the request
// that changed the mode does not wait; RefreshPendingInstructions resolves
// the project into its orchestrators.
func (m *Manager) NoteLadoModeChanged(projectID domain.ProjectID) {
	if strings.TrimSpace(string(projectID)) == "" {
		return
	}
	m.instructionRefresh.noteProject(projectID)
}

// RefreshPendingInstructions resolves the noted projects into their live
// orchestrators, then tries each queued orchestrator once. An orchestrator
// that is busy stays queued for the next call; any other outcome drops it.
// It returns how many orchestrators still wait.
func (m *Manager) RefreshPendingInstructions(ctx context.Context) (int, error) {
	var errs []error
	for _, projectID := range m.instructionRefresh.takeProjects() {
		if err := m.queueProjectOrchestrators(ctx, projectID); err != nil {
			// Keep the project so the next call reads it again.
			m.instructionRefresh.noteProject(projectID)
			errs = append(errs, err)
		}
	}
	waiting := 0
	for _, id := range m.instructionRefresh.waiting() {
		generation, ok := m.instructionRefresh.generation(id)
		if !ok {
			continue
		}
		outcome, err := m.refreshOrchestratorInstructions(ctx, id, generation)
		if err != nil {
			errs = append(errs, fmt.Errorf("refresh instructions of %s: %w", id, err))
		}
		switch outcome {
		case InstructionRefreshBusy:
			waiting++
			continue
		case InstructionRefreshDone:
			m.logger.Info("orchestrator relaunched with refreshed instructions", "sessionID", id)
		case InstructionRefreshUnsupported:
			m.logger.Warn("orchestrator keeps its instructions until it next launches", "sessionID", id)
		}
		m.instructionRefresh.finish(id, generation)
	}
	return waiting, errors.Join(errs...)
}

func (m *Manager) queueProjectOrchestrators(ctx context.Context, projectID domain.ProjectID) error {
	recs, err := m.store.ListSessions(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list orchestrators of %s: %w", projectID, err)
	}
	for _, rec := range recs {
		if rec.Kind != domain.KindOrchestrator || rec.IsTerminated || rec.Activity.State == domain.ActivityExited {
			continue
		}
		if generation := agentGeneration(rec); generation != "" {
			m.instructionRefresh.addSession(rec.ID, generation)
		}
	}
	return nil
}

// RefreshOrchestratorInstructions tries once to relaunch a live orchestrator
// in the same AO session and native conversation, so that its standing
// instructions are built afresh. It never interrupts a turn: an orchestrator
// that is not quiet reports InstructionRefreshBusy.
func (m *Manager) RefreshOrchestratorInstructions(ctx context.Context, id domain.SessionID) (InstructionRefreshOutcome, error) {
	return m.refreshOrchestratorInstructions(ctx, id, "")
}

// refreshOrchestratorInstructions is RefreshOrchestratorInstructions for the
// launch generation an orchestrator was queued with; empty takes any.
func (m *Manager) refreshOrchestratorInstructions(ctx context.Context, id domain.SessionID, generation string) (InstructionRefreshOutcome, error) {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return InstructionRefreshBusy, err
	}
	if !ok || rec.IsTerminated || rec.Activity.State == domain.ActivityExited {
		return InstructionRefreshNotRunning, nil
	}
	if rec.Kind != domain.KindOrchestrator {
		return InstructionRefreshUnsupported, nil
	}
	current := agentGeneration(rec)
	if current == "" || generation != "" && current != generation {
		return InstructionRefreshNotRunning, nil
	}
	if m.launchInFlight(id) {
		return InstructionRefreshBusy, nil
	}
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		return m.refreshChatInstructions(ctx, rec)
	}
	return m.refreshTUIInstructions(ctx, id, current)
}

// refreshChatInstructions stops a quiet Chat provider the way hibernation
// does and resumes it straight away; the resume builds the instructions.
func (m *Manager) refreshChatInstructions(ctx context.Context, rec domain.SessionRecord) (InstructionRefreshOutcome, error) {
	if m.chat == nil || rec.HibernatedAt != nil || !m.chat.HasLiveChatController(rec.ID) {
		return InstructionRefreshNotRunning, nil
	}
	releaser, ok := m.chat.(interface {
		ReleaseIdleChat(context.Context, domain.SessionID, time.Duration) (bool, error)
	})
	if !ok {
		return InstructionRefreshUnsupported, nil
	}
	if !rec.EligibleForChatRelease() {
		return InstructionRefreshBusy, nil
	}
	released, err := m.releaseIdleChat(ctx, rec.ID, releaser.ReleaseIdleChat)
	if err != nil || !released {
		return InstructionRefreshBusy, err
	}
	// A send or a newly opened view may wake the session first; this joins it.
	if err := m.WakeHibernatedChat(ctx, rec.ID); err != nil {
		// The session sleeps until a send or a view wakes it, with the new
		// instructions.
		return InstructionRefreshNotRunning, fmt.Errorf("resume chat with refreshed instructions: %w", err)
	}
	return InstructionRefreshDone, nil
}

func (m *Manager) releaseIdleChat(
	ctx context.Context,
	id domain.SessionID,
	release func(context.Context, domain.SessionID, time.Duration) (bool, error),
) (bool, error) {
	operationCtx, cancel := context.WithTimeout(ctx, instructionRefreshStepLimit)
	defer cancel()
	// The hibernate operation lets explicit operations wait for the stop
	// instead of failing, as they do for hibernation.
	if err := m.beginAgentOperation(operationCtx, id, agentOperationHibernate); err != nil {
		if errors.Is(err, errAgentOperationInProgress) {
			return false, nil
		}
		return false, err
	}
	defer m.endAgentOperation(id, agentOperationHibernate)
	if active, err := m.hasActiveInterfaceTransition(operationCtx, id); err != nil || active {
		return false, err
	}
	return release(operationCtx, id, instructionRefreshQuiet)
}

// refreshTUIInstructions stops a quiet TUI agent and resumes its native
// conversation under one exclusive operation, so no other operation or AO
// delivery comes between the stop and the resume. Terminal typing is dropped
// meanwhile.
func (m *Manager) refreshTUIInstructions(ctx context.Context, id domain.SessionID, launchID string) (InstructionRefreshOutcome, error) {
	if err := m.authorizeLadoRestore(ctx, id); err != nil {
		return InstructionRefreshUnsupported, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, instructionRefreshStepLimit)
	err := m.beginAgentOperation(operationCtx, id, agentOperationResume)
	cancel()
	if err != nil {
		if errors.Is(err, errAgentOperationInProgress) || errors.Is(err, context.DeadlineExceeded) {
			return InstructionRefreshBusy, nil
		}
		return InstructionRefreshBusy, err
	}
	defer m.endAgentOperation(id, agentOperationResume)
	if active, err := m.hasActiveInterfaceTransition(ctx, id); err != nil || active {
		return InstructionRefreshBusy, err
	}
	rec, err := m.getRecord(ctx, id)
	if err != nil {
		return InstructionRefreshBusy, err
	}
	if rec.IsTerminated || rec.Activity.State == domain.ActivityExited || agentGeneration(rec) != launchID {
		return InstructionRefreshNotRunning, nil
	}
	if !m.tuiQuietForRefresh(rec, time.Time{}) {
		return InstructionRefreshBusy, nil
	}
	var inspector ports.TerminalSurfaceInspector
	if agent, ok := m.agents.Agent(rec.Harness); ok {
		inspector, _ = agent.(ports.TerminalSurfaceInspector)
	}
	styled, _ := m.runtime.(ports.StyledTerminalOutputReader)
	handle := runtimeHandle(rec.Metadata)
	if inspector == nil || styled == nil || handle.ID == "" {
		// Without a screen to read, a draft in the composer cannot be told
		// apart from an empty one; a relaunch would lose it.
		return InstructionRefreshUnsupported, nil
	}
	releaseHarness, err := m.beginHarnessUse(rec.Harness)
	if err != nil {
		return InstructionRefreshBusy, nil
	}
	defer releaseHarness()

	lastInputAt, releaseInput := m.beginTerminalInputDrain(rec)
	if releaseInput != nil {
		defer releaseInput()
	}
	if !lastInputAt.IsZero() && lastInputAt.Add(instructionRefreshQuiet).After(m.clock()) {
		return InstructionRefreshBusy, nil
	}
	quiet, quietOK, err := m.tuiSurfaceQuiet(ctx, rec, handle, inspector, styled, lastInputAt)
	if err != nil || !quietOK {
		return InstructionRefreshBusy, err
	}
	nativeID, err := m.handoffNativeConversationID(ctx, quiet)
	if err != nil {
		if errors.Is(err, ErrInterfaceHandoffUnsupported) {
			return InstructionRefreshUnsupported, nil
		}
		// The native id of this launch is not proven yet; try again later
		// rather than resume a conversation that may not be this one.
		m.logger.Debug("refresh instructions: native conversation not proven", "sessionID", id, "error", err)
		return InstructionRefreshBusy, nil
	}

	exited, err := m.stopAgentKeepingSession(ctx, quiet)
	if err != nil {
		return InstructionRefreshBusy, fmt.Errorf("stop agent: %w", err)
	}
	// A conversation that never started has nothing to keep.
	requireNativeHistory := nativeID != ""
	const operation = "refresh orchestrator instructions"
	_, err = m.resumeAgentRecordWithReservedGeneration(ctx, operation, exited, false, requireNativeHistory, "")
	if err == nil {
		return InstructionRefreshDone, nil
	}
	if !requireNativeHistory {
		return InstructionRefreshNotRunning, err
	}
	m.logger.Warn("refresh instructions: native resume failed, resuming as Resume does", "sessionID", id, "error", err)
	// The agent is stopped. Resume it the way the Resume action would rather
	// than leave the project without an orchestrator.
	again, err := m.getRecord(ctx, id)
	if err != nil {
		return InstructionRefreshNotRunning, err
	}
	if again.IsTerminated || again.Activity.State != domain.ActivityExited {
		return InstructionRefreshNotRunning, nil
	}
	if _, err := m.resumeAgentRecordWithPolicy(ctx, operation, again, false, false); err != nil {
		return InstructionRefreshNotRunning, err
	}
	return InstructionRefreshDone, nil
}

// tuiQuietForRefresh reports a ready TUI agent, its status checked since the
// daemon started, idle since a hook signal of its current launch, with no
// activity for instructionRefreshQuiet and none before terminal input
// accepted after lastInputAt.
func (m *Manager) tuiQuietForRefresh(rec domain.SessionRecord, lastInputAt time.Time) bool {
	return domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeTUI &&
		!rec.IsTaskPreparation && rec.HibernatedAt == nil &&
		rec.ProvisionState.WithDefault() == domain.SessionProvisionReady &&
		m.SessionStatusReadiness(rec) == "ready" &&
		!rec.FirstSignalAt.IsZero() &&
		tuiIdleAfterInput(rec, lastInputAt) &&
		!rec.Activity.LastActivityAt.IsZero() &&
		!rec.Activity.LastActivityAt.Add(instructionRefreshQuiet).After(m.clock())
}

// tuiSurfaceQuiet samples the terminal screen while terminal input is
// blocked: each sample needs the same launch still quiet, and a screen the
// agent's adapter reads as idle with an empty composer. It returns the last
// record read.
func (m *Manager) tuiSurfaceQuiet(
	ctx context.Context,
	rec domain.SessionRecord,
	handle ports.RuntimeHandle,
	inspector ports.TerminalSurfaceInspector,
	styled ports.StyledTerminalOutputReader,
	lastInputAt time.Time,
) (domain.SessionRecord, bool, error) {
	launchID := agentGeneration(rec)
	current := rec
	for sample := range instructionRefreshSurfaceSamples {
		if sample > 0 {
			timer := time.NewTimer(m.interfaceTransition.pollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return domain.SessionRecord{}, false, ctx.Err()
			case <-timer.C:
			}
			var err error
			if current, err = m.getRecord(ctx, rec.ID); err != nil {
				return domain.SessionRecord{}, false, err
			}
		}
		if current.IsTerminated || agentGeneration(current) != launchID || !m.tuiQuietForRefresh(current, lastInputAt) {
			return domain.SessionRecord{}, false, nil
		}
		output, err := styled.GetStyledOutput(ctx, handle, interfaceTransitionOutputLines)
		if err != nil {
			// An unreadable screen proves nothing; a later try reads it again.
			m.logger.Debug("refresh instructions: terminal screen unreadable", "sessionID", rec.ID, "error", err)
			return domain.SessionRecord{}, false, nil
		}
		observation := inspector.InspectTerminalSurface(output)
		if observation.Work != ports.TerminalSurfaceWorkIdle || observation.Composer != ports.TerminalComposerEmpty {
			return domain.SessionRecord{}, false, nil
		}
	}
	return current, true, nil
}
