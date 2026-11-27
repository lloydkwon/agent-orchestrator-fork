package ladowatch

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// runInBackground shows the worker resting on a background task started at.
func (f *fixture) runInBackground(at time.Time) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	f.store.background[worker1] = domain.SessionBackgroundTasks{SessionID: worker1, UpdatedAt: at,
		Tasks: []domain.BackgroundTask{{Type: domain.BackgroundTaskShell, Description: "wait_event.sh 7000", StartedAt: at}}}
}

// LADO fork (2026-11-27): a worker resting on its own background work is not
// stalled; past three times the stall time LADO alerts once, without a nudge.
func TestWorkerRestingOnBackgroundWorkIsNotStalled(t *testing.T) {
	f := newFixture(t)
	f.runInBackground(t0)
	stall := domain.LadoWatchDefaults().Stall()
	f.tick(t0.Add(stall))
	f.tick(t0.Add(2 * stall))
	if len(f.msgs.texts()) != 0 || len(f.store.alerts) != 0 || f.watch().StallState != domain.LadoStallNone {
		t.Fatalf("sent %v, alerts %v, stall %+v", f.msgs.texts(), f.store.alertKinds(), f.watch().LadoWatchStall)
	}
	overdue := domain.LadoWatchDefaults().BackgroundOverdue()
	if overdue != 6*time.Hour {
		t.Fatalf("default overdue = %s, want 6h", overdue)
	}
	f.tick(t0.Add(overdue))
	f.tick(t0.Add(overdue + time.Hour))
	if !slices.Equal(f.store.alertKinds(), []domain.LadoAlertKind{domain.LadoAlertBackgroundOverdue}) || len(f.msgs.texts()) != 0 {
		t.Fatalf("alerts %v, sent %v; want one background_overdue and no nudge", f.store.alertKinds(), f.msgs.texts())
	}
	a := f.store.alerts[0]
	if a.Value != int64(overdue/time.Minute) || a.Threshold != int64(overdue/time.Minute) || a.SessionID != worker1 {
		t.Fatalf("alert = %+v", a)
	}
}

// Once the background work ends, or AO does not know of any, the stall watch
// judges as it always did.
func TestBackgroundWorkEndedOrUnknownStallsAsBefore(t *testing.T) {
	stall := domain.LadoWatchDefaults().Stall()
	f := newFixture(t)
	f.runInBackground(t0)
	f.tick(t0.Add(stall))
	f.store.mu.Lock()
	f.store.background[worker1] = domain.SessionBackgroundTasks{SessionID: worker1, Tasks: []domain.BackgroundTask{}}
	f.store.mu.Unlock()
	f.tick(t0.Add(stall + time.Minute))
	if len(f.msgs.texts()) != 1 {
		t.Fatalf("after the background work ended: sent %v, want the nudge", f.msgs.texts())
	}

	f2 := newFixture(t)
	f2.tick(t0.Add(stall))
	if len(f2.msgs.texts()) != 1 {
		t.Fatalf("without background facts: sent %v, want the nudge", f2.msgs.texts())
	}
}

// A worker resting on its background work does not wait on its owner, unless
// it asked in this turn.
func TestBackgroundWorkKeepsTheOwnerQuietUnlessTheWorkerAsked(t *testing.T) {
	wait := domain.LadoWatchDefaults().Await()
	f := newFixture(t)
	n := f.withNotifier()
	f.runInBackground(t0)
	f.tick(t0.Add(wait + time.Hour))
	if got, _ := n.counts(); got != 0 {
		t.Fatalf("notified %+v", n.intents)
	}

	f2 := newFixture(t)
	n2 := f2.withNotifier()
	f2.runInBackground(t0)
	f2.store.reports[worker1] = domain.ReportNeedsInput
	f2.tick(t0.Add(time.Minute))
	if got, _ := n2.counts(); got != 1 {
		t.Fatalf("a needs-input report notified %d times, want 1", got)
	}
}

// fakeBackground is the conversation-file reading of a worker whose bridge
// cannot report.
type fakeBackground struct {
	tasks []domain.BackgroundTask
	reads int
}

func (f *fakeBackground) Read(_ context.Context, id domain.SessionID, now time.Time) (domain.SessionBackgroundTasks, bool, error) {
	f.reads++
	return domain.SessionBackgroundTasks{SessionID: id, Tasks: f.tasks, Source: domain.BackgroundSourceTranscript, UpdatedAt: now}, true, nil
}

// A worker whose bridge cannot report is read from its conversation file; a
// bridge report is used as it is.
func TestBackgroundIsReadWhenTheBridgeCannotReport(t *testing.T) {
	stall := domain.LadoWatchDefaults().Stall()
	f := newFixture(t)
	reader := &fakeBackground{tasks: []domain.BackgroundTask{{Type: domain.BackgroundTaskShell, StartedAt: t0}}}
	f.svc.deps.Background = reader
	f.tick(t0.Add(stall))
	if reader.reads != 1 || len(f.msgs.texts()) != 0 {
		t.Fatalf("reads=%d sent %v; want the reading and no nudge", reader.reads, f.msgs.texts())
	}

	f2 := newFixture(t)
	reader2 := &fakeBackground{tasks: []domain.BackgroundTask{{Type: domain.BackgroundTaskShell, StartedAt: t0}}}
	f2.svc.deps.Background = reader2
	f2.store.background[worker1] = domain.SessionBackgroundTasks{SessionID: worker1, Tasks: []domain.BackgroundTask{}, Source: domain.BackgroundSourceBridge}
	f2.tick(t0.Add(stall))
	if reader2.reads != 0 || len(f2.msgs.texts()) != 1 {
		t.Fatalf("reads=%d sent %v; the bridge's report (none run) must stand", reader2.reads, f2.msgs.texts())
	}
}
