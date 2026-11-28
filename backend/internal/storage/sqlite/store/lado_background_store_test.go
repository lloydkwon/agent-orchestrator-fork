package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// LADO fork (2026-11-27): a session's live background tasks are recorded,
// rewritten only when they change, forgotten on nil, listed only for live
// sessions, and each change publishes a lado_background session event.
func TestLadoSessionBackgroundRecordsListsAndPublishes(t *testing.T) {
	s, raw := openLadoQueueStore(t)
	seedProject(t, s, "mer")
	ctx := context.Background()
	idle := sampleRecord("mer")
	idle.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now().UTC()}
	sess, err := s.CreateSession(ctx, idle)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	at := time.Date(2026, 11, 27, 3, 1, 8, 0, time.UTC)
	events := func() int {
		var n int
		if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM change_log WHERE session_id = ? AND json_extract(payload, '$.kind') = 'lado_background'`, string(sess.ID)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if _, ok, err := s.LadoSessionBackground(ctx, sess.ID); ok || err != nil {
		t.Fatalf("before any report: ok=%v err=%v, want unknown", ok, err)
	}
	long := strings.Repeat("가", 200)
	tasks := []domain.BackgroundTask{{Type: "shell", Description: "wait_event.sh 7000\n" + long, StartedAt: at}}
	if err := s.SetLadoSessionBackground(ctx, sess.ID, tasks, at); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LadoSessionBackground(ctx, sess.ID)
	if err != nil || !ok || len(got.Tasks) != 1 || got.Tasks[0].Type != "shell" || !got.Tasks[0].StartedAt.Equal(at) || !got.Since().Equal(at) {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if d := got.Tasks[0].Description; strings.Contains(d, "\n") || len([]rune(d)) > domain.BackgroundTaskDescriptionMax {
		t.Fatalf("description kept as %q", d)
	}
	if n := events(); n != 1 {
		t.Fatalf("events after the first report = %d, want 1", n)
	}
	// The same list again writes and publishes nothing.
	if err := s.SetLadoSessionBackground(ctx, sess.ID, tasks, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := events(); n != 1 {
		t.Fatalf("events after the same report = %d, want 1", n)
	}
	// None run: an empty list, known.
	if err := s.SetLadoSessionBackground(ctx, sess.ID, []domain.BackgroundTask{}, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.LadoSessionBackground(ctx, sess.ID); !ok || got.Running() {
		t.Fatalf("after none run: %+v ok=%v", got, ok)
	}
	if live, err := s.ListLiveLadoSessionBackground(ctx); err != nil || len(live) != 1 {
		t.Fatalf("live = %+v err=%v", live, err)
	}
	// Forgotten: unknown again.
	if err := s.SetLadoSessionBackground(ctx, sess.ID, nil, at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LadoSessionBackground(ctx, sess.ID); ok {
		t.Fatal("still known after it was forgotten")
	}
	if n := events(); n != 3 {
		t.Fatalf("events = %d, want 3 (insert, change, delete)", n)
	}
	// A reading of the conversation file goes in only while the bridge has
	// not reported; the bridge's report replaces it, and a later reading does
	// not overwrite the bridge.
	if err := s.SetLadoSessionBackgroundReading(ctx, sess.ID, tasks, at); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.LadoSessionBackground(ctx, sess.ID); !ok || got.Source != domain.BackgroundSourceTranscript || !got.Running() {
		t.Fatalf("reading = %+v ok=%v", got, ok)
	}
	if err := s.SetLadoSessionBackground(ctx, sess.ID, []domain.BackgroundTask{}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLadoSessionBackgroundReading(ctx, sess.ID, tasks, at); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.LadoSessionBackground(ctx, sess.ID); got.Source != domain.BackgroundSourceBridge || got.Running() {
		t.Fatalf("a reading overwrote the bridge: %+v", got)
	}
	// Forgetting a reading leaves the bridge's report; the bridge going quiet
	// leaves a reading.
	if err := s.ForgetLadoSessionBackgroundReading(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LadoSessionBackground(ctx, sess.ID); !ok {
		t.Fatal("forgetting a reading dropped the bridge's report")
	}
	if err := s.SetLadoSessionBackground(ctx, sess.ID, nil, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLadoSessionBackgroundReading(ctx, sess.ID, tasks, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLadoSessionBackground(ctx, sess.ID, nil, at); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.LadoSessionBackground(ctx, sess.ID); !ok || got.Source != domain.BackgroundSourceTranscript {
		t.Fatalf("the bridge going quiet dropped a reading: %+v ok=%v", got, ok)
	}
	if err := s.ForgetLadoSessionBackgroundReading(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	// An ended session is not listed.
	if err := s.SetLadoSessionBackground(ctx, sess.ID, tasks, at); err != nil {
		t.Fatal(err)
	}
	ended := sess
	ended.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: at}
	if err := s.UpdateSession(ctx, ended); err != nil {
		t.Fatal(err)
	}
	if live, err := s.ListLiveLadoSessionBackground(ctx); err != nil || len(live) != 0 {
		t.Fatalf("live after the session ended = %+v err=%v", live, err)
	}
}
