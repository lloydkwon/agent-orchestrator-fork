package ladowatch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// fakeNotifier records the needs_input notifications LADO raises and
// resolves for a worker that waits on its owner.
type fakeNotifier struct {
	mu       sync.Mutex
	intents  []ports.NotificationIntent
	resolves []ports.NotificationResolution
}

func (n *fakeNotifier) Notify(_ context.Context, i ports.NotificationIntent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.intents = append(n.intents, i)
	return nil
}

func (n *fakeNotifier) Resolve(_ context.Context, r ports.NotificationResolution) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.resolves = append(n.resolves, r)
	return nil
}

func (n *fakeNotifier) counts() (int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.intents), len(n.resolves)
}

func (f *fixture) withNotifier() *fakeNotifier {
	n := &fakeNotifier{}
	f.svc.deps.Notifier = n
	return n
}

func TestQuietWorkerTellsTheOwnerOnceAfterTheAwaitTime(t *testing.T) {
	f := newFixture(t)
	n := f.withNotifier()
	wait := domain.LadoWatchDefaults().Await()
	if wait != 10*time.Minute {
		t.Fatalf("default await = %s, want 10m", wait)
	}
	f.tick(t0.Add(wait - time.Minute))
	if got, _ := n.counts(); got != 0 {
		t.Fatalf("told the owner too early: %+v", n.intents)
	}
	f.tick(t0.Add(wait))
	f.tick(t0.Add(wait + time.Minute))
	f.tick(t0.Add(wait + time.Hour))
	if got, _ := n.counts(); got != 1 {
		t.Fatalf("notified %d times, want once per turn: %+v", got, n.intents)
	}
	in := n.intents[0]
	if in.Type != domain.NotificationNeedsInput || in.SessionID != worker1 || in.ProjectID != proj1 || in.SourceKey == "" {
		t.Fatalf("intent = %+v", in)
	}
	// LADO tells the owner; it does not nudge the worker for it.
	if len(f.msgs.texts()) != 0 {
		t.Fatalf("sent the worker %v", f.msgs.texts())
	}
}

func TestNeedsInputReportTellsTheOwnerAtOnce(t *testing.T) {
	f := newFixture(t)
	n := f.withNotifier()
	f.store.reports[worker1] = domain.ReportNeedsInput
	f.tick(t0.Add(time.Minute))
	if got, _ := n.counts(); got != 1 {
		t.Fatalf("notified %d times, want 1", got)
	}
	// Even with the quiet wait off, a worker that asked is told of.
	f2 := newFixture(t)
	n2 := f2.withNotifier()
	settings := domain.LadoWatchDefaults()
	settings.AwaitMinutes = 0
	f2.store.settings[proj1] = settings
	f2.store.reports[worker1] = domain.ReportNeedsInput
	f2.tick(t0.Add(time.Minute))
	if got, _ := n2.counts(); got != 1 {
		t.Fatalf("with the wait off: notified %d times, want 1", got)
	}
}

func TestAwaitLeavesOutWorkersThatDoNotWaitOnTheOwner(t *testing.T) {
	wait := domain.LadoWatchDefaults().Await()
	cases := map[string]func(*fixture){
		"a done report":          func(f *fixture) { f.store.reports[worker1] = domain.ReportDone },
		"a running turn":         func(f *fixture) { f.work(t0) },
		"an unknown role":        func(f *fixture) { f.store.execs[0].Role = "reviewer" },
		"an unread conversation": func(f *fixture) { delete(f.convs.turns, worker1) },
		"a turn the owner stopped": func(f *fixture) {
			f.endTurn(domain.TurnStateInterrupted, "", t0)
		},
		"a usage limit": func(f *fixture) { f.limitAt(t0, sessionLimit) },
		"the wait off": func(f *fixture) {
			settings := domain.LadoWatchDefaults()
			settings.AwaitMinutes = 0
			f.store.settings[proj1] = settings
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			n := f.withNotifier()
			change(f)
			f.tick(t0.Add(wait + time.Minute))
			if got, _ := n.counts(); got != 0 {
				t.Fatalf("notified %+v", n.intents)
			}
		})
	}
}

func TestAwaitResolvesOnceTheWorkerMovesAndTellsOfTheNextTurn(t *testing.T) {
	f := newFixture(t)
	n := f.withNotifier()
	wait := domain.LadoWatchDefaults().Await()
	f.tick(t0.Add(wait))

	// The owner answered: a new turn runs.
	back := t0.Add(wait + 5*time.Minute)
	f.work(back)
	f.tick(back.Add(time.Minute))
	if _, res := n.counts(); res != 1 || n.resolves[0].SessionID != worker1 || n.resolves[0].Type != domain.NotificationNeedsInput {
		t.Fatalf("resolves = %+v", n.resolves)
	}
	f.tick(back.Add(2 * time.Minute))
	if _, res := n.counts(); res != 1 {
		t.Fatalf("resolved again while nothing was open: %+v", n.resolves)
	}

	// The next turn ends with a question: the owner is told again.
	end := back.Add(3 * time.Minute)
	f.endTurn(domain.TurnStateCompleted, "", end)
	f.rest(end)
	f.tick(end.Add(wait))
	if got, _ := n.counts(); got != 2 {
		t.Fatalf("notified %d times, want 2", got)
	}
	if n.intents[0].SourceKey == n.intents[1].SourceKey {
		t.Fatalf("two turns share a source key %q", n.intents[0].SourceKey)
	}
}

func TestAwaitWithoutANotifierStaysQuiet(t *testing.T) {
	f := newFixture(t)
	f.tick(t0.Add(time.Hour))
	if len(f.msgs.texts()) != 0 {
		t.Fatalf("sent %v", f.msgs.texts())
	}
}

// A worker AO shows waiting_input or blocked already rang AO's own
// notification; the watch neither adds one nor resolves AO's.
func TestAwaitLeavesWaitingSessionsToAO(t *testing.T) {
	f := newFixture(t)
	n := f.withNotifier()
	rec := f.record(worker1)
	rec.Activity = domain.Activity{State: domain.ActivityWaitingInput, LastActivityAt: t0}
	f.sessions.set(rec)
	f.store.reports[worker1] = domain.ReportNeedsInput
	f.tick(t0.Add(time.Hour))
	if got, res := n.counts(); got != 0 || res != 0 {
		t.Fatalf("intents %+v, resolves %+v", n.intents, n.resolves)
	}

	// Told of a quiet wait, the worker then asks for a permission: AO's
	// blocked notification stays open.
	f2 := newFixture(t)
	n2 := f2.withNotifier()
	f2.tick(t0.Add(domain.LadoWatchDefaults().Await()))
	rec2 := f2.record(worker1)
	rec2.Activity = domain.Activity{State: domain.ActivityBlocked, LastActivityAt: t0.Add(11 * time.Minute)}
	f2.sessions.set(rec2)
	f2.tick(t0.Add(12 * time.Minute))
	if got, res := n2.counts(); got != 1 || res != 0 {
		t.Fatalf("intents %+v, resolves %+v; want one intent and no resolve", n2.intents, n2.resolves)
	}
}

// A report belongs to the turn it was made in: a needs-input report of an
// earlier turn does not ring at once, and a done report of an earlier turn
// does not keep the owner from hearing of the newest one.
func TestAwaitReadsOnlyThisTurnsReport(t *testing.T) {
	wait := domain.LadoWatchDefaults().Await()
	for _, state := range []domain.ReportState{domain.ReportNeedsInput, domain.ReportDone} {
		t.Run(string(state), func(t *testing.T) {
			f := newFixture(t)
			n := f.withNotifier()
			f.store.reports[worker1] = state
			f.store.reportsAt[worker1] = t0.Add(-time.Hour)
			f.tick(t0.Add(time.Minute))
			if got, _ := n.counts(); got != 0 {
				t.Fatalf("an earlier turn's report rang at once: %+v", n.intents)
			}
			f.tick(t0.Add(wait))
			if got, _ := n.counts(); got != 1 {
				t.Fatalf("notified %d times after the await time, want 1", got)
			}
		})
	}
}

// After a restart the watch remembers nothing: its first look resolves a
// wait an earlier run told of and that is over, once.
func TestAwaitFirstLookResolvesAnOverWait(t *testing.T) {
	f := newFixture(t)
	n := f.withNotifier()
	f.store.reports[worker1] = domain.ReportDone
	f.tick(t0.Add(time.Minute))
	f.tick(t0.Add(2 * time.Minute))
	if got, res := n.counts(); got != 0 || res != 1 {
		t.Fatalf("intents %+v, resolves %+v; want one resolve", n.intents, n.resolves)
	}
}

// A worker that worked and ended between two ticks has its wait resolved.
func TestAwaitResolvesForAnEndedSession(t *testing.T) {
	f := newFixture(t)
	n := f.withNotifier()
	f.tick(t0.Add(domain.LadoWatchDefaults().Await()))
	rec := f.record(worker1)
	rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: t0.Add(11 * time.Minute)}
	f.sessions.set(rec)
	f.tick(t0.Add(12 * time.Minute))
	f.tick(t0.Add(13 * time.Minute))
	if got, res := n.counts(); got != 1 || res != 1 {
		t.Fatalf("intents %+v, resolves %+v; want one of each", n.intents, n.resolves)
	}
}
