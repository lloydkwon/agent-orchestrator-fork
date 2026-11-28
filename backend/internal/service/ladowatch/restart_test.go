package ladowatch

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// cutOff is the error AO leaves on a turn whose controller ended under it.
const cutOff = "controller ended before the turn completed"

// restartedMidTurn shows the worker as the daemon's startup restore leaves
// it: its turn settled as failed at at, resting, and reported as started
// again with its turn cut off.
func (f *fixture) restartedMidTurn(at time.Time) {
	f.endTurn(domain.TurnStateFailed, cutOff, at)
	f.rest(at)
	f.now = at
	f.svc.SessionRestored(worker1, true)
}

// A worker the daemon started again after a reboot, whose turn was cut off,
// is told once to check its state and go on.
func TestRestartedWorkerWhoseTurnWasCutOffIsToldToGoOn(t *testing.T) {
	f := newFixture(t)
	f.restartedMidTurn(t0)

	f.tick(t0.Add(time.Minute))

	if !slices.Equal(f.msgs.texts(), []string{domain.LadoWakeRestartResumeText}) {
		t.Fatalf("sent %v, want the restart message once", f.msgs.texts())
	}
	events := f.store.audited("lado.nudge_sent")
	if len(events) != 1 {
		t.Fatalf("audits = %+v", f.store.audits)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != "restart_resume" || payload["task"] != string(task1) {
		t.Fatalf("audit payload = %v", payload)
	}
	if !slices.Equal(f.store.alertKinds(), []domain.LadoAlertKind{domain.LadoAlertRestartResumed}) {
		t.Fatalf("alerts = %v", f.store.alertKinds())
	}

	// The message goes once: later ticks send nothing more.
	f.tick(t0.Add(2 * time.Minute))
	f.tick(t0.Add(3 * time.Minute))
	if len(f.msgs.texts()) != 1 || f.store.auditCount("lado.nudge_sent") != 1 {
		t.Fatalf("the restart message went again: %v", f.msgs.texts())
	}
}

// A worker that had finished its turn before the reboot was waiting, not
// working: LADO starts it again and says nothing.
func TestRestartedWorkerThatWasWaitingGetsNoMessage(t *testing.T) {
	f := newFixture(t)
	f.svc.SessionRestored(worker1, false)

	f.tick(t0.Add(time.Minute))

	if len(f.msgs.texts()) != 0 || len(f.store.alerts) != 0 {
		t.Fatalf("sent %v with alerts %v, want nothing", f.msgs.texts(), f.store.alertKinds())
	}
}

// The message waits until the cut-off turn is settled, and goes then.
func TestRestartMessageWaitsForTheCutOffTurnToSettle(t *testing.T) {
	f := newFixture(t)
	f.work(t0)
	f.svc.SessionRestored(worker1, true)

	f.tick(t0.Add(time.Minute))
	if len(f.msgs.texts()) != 0 {
		t.Fatalf("sent %v while the turn still shows running", f.msgs.texts())
	}

	f.endTurn(domain.TurnStateFailed, cutOff, t0.Add(90*time.Second))
	f.rest(t0.Add(90 * time.Second))
	f.tick(t0.Add(2 * time.Minute))
	if !slices.Equal(f.msgs.texts(), []string{domain.LadoWakeRestartResumeText}) {
		t.Fatalf("sent %v once the turn settled", f.msgs.texts())
	}
}

// LADO never speaks for the owner, and only where the project lets it wake
// its workers.
func TestRestartMessageKeepsTheWakeRules(t *testing.T) {
	for name, arrange := range map[string]func(f *fixture){
		"a question to the owner": func(f *fixture) { f.store.reports[worker1] = domain.ReportNeedsInput },
		"a done report":           func(f *fixture) { f.store.reports[worker1] = domain.ReportDone },
		"waking is off": func(f *fixture) {
			settings := domain.LadoWatchDefaults()
			settings.WakeEnabled = false
			f.store.settings[proj1] = settings
		},
		"no messenger": func(f *fixture) { f.svc.deps.Messenger = nil },
		// The owner pressed Stop on the worker's newest turn: LADO does not
		// start it again.
		"the owner stopped the turn": func(f *fixture) { f.endTurn(domain.TurnStateInterrupted, "", t0) },
		// A worker at its usage limit is resumed by the limit watch, at the
		// reset: the restart message would only fail there again.
		"a usage limit": func(f *fixture) { f.limitAt(t0, sessionLimit) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.restartedMidTurn(t0)
			arrange(f)

			f.tick(t0.Add(time.Minute))
			f.tick(t0.Add(2 * time.Minute))

			if slices.Contains(f.msgs.texts(), domain.LadoWakeRestartResumeText) {
				t.Fatalf("sent %v", f.msgs.texts())
			}
		})
	}
}

// A message that did not go is tried again on the next tick.
func TestRestartMessageThatDidNotGoIsTriedAgain(t *testing.T) {
	f := newFixture(t)
	f.restartedMidTurn(t0)
	f.msgs.fail = errors.New("the controller is not ready")

	f.now = t0.Add(time.Minute)
	if err := f.svc.Tick(t.Context()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(f.msgs.texts()) != 0 || f.store.auditCount("lado.nudge_sent") != 0 {
		t.Fatalf("a failed send was recorded: %v", f.store.audits)
	}

	f.msgs.fail = nil
	f.tick(t0.Add(2 * time.Minute))
	if !slices.Equal(f.msgs.texts(), []string{domain.LadoWakeRestartResumeText}) {
		t.Fatalf("sent %v on the next tick", f.msgs.texts())
	}
}

// A worker that never settles is not told to go on long after the restart:
// the stall watch takes over.
func TestRestartMessageExpires(t *testing.T) {
	f := newFixture(t)
	f.work(t0)
	f.svc.SessionRestored(worker1, true)
	f.tick(t0.Add(time.Minute))

	late := t0.Add(domain.LadoWakeRestartWindow + time.Minute)
	f.endTurn(domain.TurnStateFailed, cutOff, late)
	f.rest(late)
	f.tick(late.Add(time.Minute))

	if len(f.msgs.texts()) != 0 {
		t.Fatalf("sent %v past the restart window", f.msgs.texts())
	}
}
