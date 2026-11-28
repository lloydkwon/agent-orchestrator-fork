package boardtui

import (
	"testing"
	"time"
)

var baseTime = time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return baseTime.Add(time.Duration(minutes) * time.Minute) }

func worker(id, project, column, status string, lastMinute int) Session {
	return Session{
		ID:           id,
		ProjectID:    project,
		Kind:         "worker",
		Status:       status,
		KanbanColumn: column,
		CreatedAt:    at(0),
		LastEventAt:  at(lastMinute),
	}
}

func TestBuildBoardPlacesOnlyLiveWorkersInDaemonColumns(t *testing.T) {
	orchestrator := worker("demo-1", "demo", ColumnBuilding, "working", 9)
	orchestrator.Kind = "orchestrator"
	legacyOrchestrator := worker("demo-orchestrator", "demo", ColumnBuilding, "working", 9)
	legacyOrchestrator.Kind = ""
	terminated := worker("demo-5", "demo", ColumnReady, "merged", 3)
	terminated.IsTerminated = true
	snap := Snapshot{Sessions: []Session{
		orchestrator,
		legacyOrchestrator,
		worker("demo-2", "demo", ColumnBuilding, "working", 1),
		worker("demo-3", "demo", ColumnValidating, "ci_failed", 2),
		worker("demo-4", "demo", ColumnArchive, "terminated", 4),
		terminated,
		worker("demo-6", "demo", ColumnNeedsReview, "review_pending", 5),
		worker("demo-7", "demo", ColumnReady, "mergeable", 6),
		worker("other-1", "other", ColumnBuilding, "working", 7),
	}}

	board := BuildBoard(snap, "demo")

	want := map[string][]string{
		ColumnBuilding:    {"demo-2"},
		ColumnValidating:  {"demo-3"},
		ColumnNeedsReview: {"demo-6"},
		ColumnReady:       {"demo-7"},
	}
	for i, column := range Columns {
		got := ids(board.Columns[i])
		if !equalStrings(got, want[column]) {
			t.Errorf("column %s = %v, want %v", column, got, want[column])
		}
	}
}

func TestBuildBoardMovesNoSignalToBuilding(t *testing.T) {
	noSignal := worker("demo-1", "demo", ColumnNeedsReview, "no_signal", 1)
	noSignalDisplay := worker("demo-2", "demo", ColumnReady, "working", 2)
	noSignalDisplay.DisplayStatus = "No signal"
	future := worker("demo-3", "demo", "shipping", "working", 3)
	snap := Snapshot{Sessions: []Session{noSignal, noSignalDisplay, future}}

	board := BuildBoard(snap, "demo")

	if got := ids(board.Columns[0]); !equalStrings(got, []string{"demo-3", "demo-2", "demo-1"}) {
		t.Errorf("building = %v, want [demo-3 demo-2 demo-1] (no signal and an unknown column of a working session go to building, newest first)", got)
	}
}

func TestBuildBoardSortsNewestActivityFirst(t *testing.T) {
	older := worker("demo-1", "demo", ColumnBuilding, "working", 1)
	newer := worker("demo-2", "demo", ColumnBuilding, "working", 2)
	newer.LastEventAt = time.Time{}
	newer.Activity = &Activity{State: "active", LastActivityAt: at(8)}
	snap := Snapshot{Sessions: []Session{older, newer}}

	board := BuildBoard(snap, "demo")

	if got := ids(board.Columns[0]); !equalStrings(got, []string{"demo-2", "demo-1"}) {
		t.Errorf("building = %v, want newest activity first", got)
	}
}

func TestSummarizeCountsColumnsAttentionAndOrchestrator(t *testing.T) {
	deadOrchestrator := worker("demo-1", "demo", ColumnArchive, "terminated", 1)
	deadOrchestrator.Kind = "orchestrator"
	deadOrchestrator.IsTerminated = true
	liveOrchestrator := worker("demo-9", "demo", ColumnBuilding, "working", 2)
	liveOrchestrator.Kind = "orchestrator"
	liveOrchestrator.CreatedAt = at(5)
	waiting := worker("demo-3", "demo", ColumnValidating, "working", 30)
	waiting.Activity = &Activity{State: "waiting_input", LastActivityAt: at(30)}
	snap := Snapshot{
		Projects: []Project{{ID: "demo", Name: "Demo"}, {ID: "empty", Name: "Empty", FolderMissing: true}},
		Sessions: []Session{
			deadOrchestrator,
			liveOrchestrator,
			worker("demo-2", "demo", ColumnBuilding, "needs_input", 10),
			waiting,
			worker("demo-4", "demo", ColumnReady, "mergeable", 20),
			worker("demo-5", "demo", ColumnArchive, "terminated", 40),
			worker("demo-6", "demo", "shipping", "working", 15),
		},
	}

	got := Summarize(snap)

	if len(got) != 2 {
		t.Fatalf("summaries = %d, want 2", len(got))
	}
	demo := got[0]
	if demo.Project.ID != "demo" {
		t.Fatalf("first summary = %q, want demo (project list order)", demo.Project.ID)
	}
	wantCounts := [4]int{2, 1, 0, 1}
	if demo.Counts != wantCounts {
		t.Errorf("counts = %v, want %v", demo.Counts, wantCounts)
	}
	if demo.Attention != 2 || demo.Archived != 1 {
		t.Errorf("attention=%d archived=%d, want 2 1", demo.Attention, demo.Archived)
	}
	if demo.Orchestrator == nil || demo.Orchestrator.ID != "demo-9" {
		t.Errorf("orchestrator = %#v, want live demo-9", demo.Orchestrator)
	}
	if !demo.LastActivity.Equal(at(40)) {
		t.Errorf("last activity = %v, want %v", demo.LastActivity, at(40))
	}
	empty := got[1]
	if empty.Live() != 0 || empty.Orchestrator != nil || !empty.Project.FolderMissing {
		t.Errorf("empty summary = %#v", empty)
	}
}

func TestSummarizeKeepsLiveSessionsOfUnlistedProjects(t *testing.T) {
	snap := Snapshot{
		Projects: []Project{{ID: "demo", Name: "Demo"}},
		Sessions: []Session{
			worker("gone-1", "gone", ColumnBuilding, "working", 1),
			worker("old-1", "old", ColumnArchive, "terminated", 1),
		},
	}

	got := Summarize(snap)

	if len(got) != 2 || got[1].Project.ID != "gone" || !got[1].Unlisted {
		t.Fatalf("summaries = %#v, want demo then unlisted gone (archived-only old is skipped)", got)
	}
}

func TestNeedsAttention(t *testing.T) {
	cases := []struct {
		status   string
		activity string
		want     bool
	}{
		{"needs_input", "", true},
		{"exited", "", true},
		{"no_signal", "", true},
		{"ci_failed", "", true},
		{"changes_requested", "", true},
		{"commented", "", true},
		{"unknown", "", true},
		{"working", "blocked", true},
		{"working", "waiting_input", true},
		{"working", "active", false},
		{"review_pending", "", false},
		{"mergeable", "", false},
	}
	for _, tc := range cases {
		s := Session{Status: tc.status}
		if tc.activity != "" {
			s.Activity = &Activity{State: tc.activity}
		}
		if got := NeedsAttention(s); got != tc.want {
			t.Errorf("NeedsAttention(%s/%s) = %v, want %v", tc.status, tc.activity, got, tc.want)
		}
	}
}

func ids(sessions []Session) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
