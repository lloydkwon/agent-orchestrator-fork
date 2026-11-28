package boardtui

// 독립 검토(2026-11-28)에서 나온 경우들의 회귀 시험.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestUserPickedEmptyColumnSurvivesReload(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader, func(o *Options) { o.ProjectID = "demo" }))
	m = send(t, m, tea.WindowSizeMsg{Width: 60, Height: 24})
	tabs := lineAt(screen(m), 2)
	x := strings.Index(tabs, "리뷰 중")
	m = send(t, m, click(ansi.StringWidth(tabs[:x]), 2))
	if m.column != 2 {
		t.Fatalf("click on empty tab: column=%d, want 2", m.column)
	}

	m = send(t, m, snapshotMsg{snap: demoSnapshot()})

	if m.column != 2 {
		t.Errorf("after the same snapshot reloads, column=%d, want 2 (a picked empty tab stays)", m.column)
	}
}

func TestProjectCursorFollowsProjectIDOnReload(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))
	m = send(t, m, key(tea.KeyDown))
	next := demoSnapshot()
	next.Projects = append([]Project{{ID: "alpha", Name: "alpha"}}, next.Projects...)

	m = send(t, m, snapshotMsg{snap: next})

	if got := m.summaries[m.projectCursor].Project.ID; got != "water" {
		t.Errorf("cursor is on %q after a project was added before it, want water", got)
	}
	m = send(t, m, key(tea.KeyEnter))
	if m.projectID != "water" {
		t.Errorf("Enter opened %q, want water", m.projectID)
	}
}

func TestNarrowPrintListsEveryColumn(t *testing.T) {
	m := New(Options{Labels: LabelsFor("en"), Now: func() time.Time { return at(10) }, ProjectID: "demo", Static: true})
	m = send(t, m, tea.WindowSizeMsg{Width: 60, Height: 0})
	m = send(t, m, SnapshotLoaded(demoSnapshot()))

	out := ansi.Strip(m.Render())

	for _, want := range []string{"Building 1", "Validating 1", "In review 0", "Ready 1", "demo-2", "입력을 기다리는 일", "demo-4"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow --print lacks %q:\n%s", want, out)
		}
	}
}

func TestShortWindowClickOutsideDrawnCardsSelectsNothing(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader, func(o *Options) { o.ProjectID = "demo" }))
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 9})
	lines := strings.Split(screen(m), "\n")
	if len(lines) != 9 {
		t.Fatalf("height 9 screen has %d lines", len(lines))
	}
	col := m.layout().columns[1].start + 2
	for y := listTop; y < len(lines); y++ {
		if strings.Contains(lines[y], "입력을 기다리는 일") {
			continue
		}
		if s := send(t, m, click(col, y)).selected(); s != nil && s.ID == "demo-3" {
			t.Errorf("click on y=%d (%q) selected demo-3, which is not drawn there", y, lines[y])
		}
	}
}

func TestUnlistedProjectCountsArchivedSessionsInAnyOrder(t *testing.T) {
	snap := Snapshot{Sessions: []Session{
		worker("gone-1", "gone", ColumnArchive, "terminated", 9),
		worker("gone-2", "gone", ColumnBuilding, "working", 2),
	}}

	got := Summarize(snap)

	if len(got) != 1 || got[0].Archived != 1 || !got[0].LastActivity.Equal(at(9)) {
		t.Fatalf("unlisted summary = %#v, want archived 1 and last activity from the archived session", got)
	}
}

func manyCards(n int) Snapshot {
	s := Snapshot{Projects: []Project{{ID: "demo", Name: "demo"}}}
	for i := 0; i < n; i++ {
		s.Sessions = append(s.Sessions, worker(fmt.Sprintf("demo-%02d", i), "demo", ColumnBuilding, "working", 100-i))
	}
	s.Sessions = append(s.Sessions, worker("demo-v", "demo", ColumnValidating, "working", 1))
	return s
}

func TestScrolledClickSelectsTheDrawnCard(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{manyCards(20)}}
	m := start(t, newTestModel(t, loader, func(o *Options) { o.ProjectID = "demo" }))
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	for i := 0; i < 10; i++ {
		m = send(t, m, key(tea.KeyDown))
	}
	if m.offsets[0] == 0 {
		t.Fatalf("building column did not scroll")
	}
	out := screen(m)
	for slot := 0; slot < m.layout().cardsVisible; slot++ {
		y := listTop + slot*cardHeight
		drawn := strings.Fields(strings.Trim(lineAt(out, y), "│┃ "))
		got := send(t, m, click(3, y)).selected()
		if len(drawn) == 0 || got == nil || got.ID != drawn[0] {
			t.Errorf("slot %d: drawn %q, click selected %v", slot, drawn, got)
		}
	}
	if got := send(t, m, click(m.layout().columns[1].start+3, listTop)).selected(); got == nil || got.ID != "demo-v" {
		t.Errorf("click on validating slot 0 selected %v, want demo-v (each column scrolls on its own)", got)
	}
}

func TestSelectionFollowsCardThatMovesColumn(t *testing.T) {
	moved := demoSnapshot()
	for i := range moved.Sessions {
		if moved.Sessions[i].ID == "demo-3" {
			moved.Sessions[i].KanbanColumn = ColumnReady
			moved.Sessions[i].LastEventAt = at(50)
		}
	}
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader, func(o *Options) { o.ProjectID = "demo" }))
	m = send(t, m, key(tea.KeyRight))

	m = send(t, m, snapshotMsg{snap: moved})

	if s := m.selected(); s == nil || s.ID != "demo-3" || m.column != 3 {
		t.Errorf("selection = %v in column %d, want demo-3 in ready", s, m.column)
	}
}

func TestScrolledProjectRowClickOpensTheDrawnProject(t *testing.T) {
	s := Snapshot{}
	for i := 0; i < 30; i++ {
		s.Projects = append(s.Projects, Project{ID: fmt.Sprintf("p%02d", i), Name: fmt.Sprintf("p%02d", i)})
	}
	loader := &fakeLoader{snaps: []Snapshot{s}}
	m := start(t, newTestModel(t, loader))
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 12})
	for i := 0; i < 20; i++ {
		m = send(t, m, key(tea.KeyDown))
	}
	y := listTop + 2
	fields := strings.Fields(strings.TrimPrefix(lineAt(screen(m), y), "›"))

	m = send(t, m, click(4, y))

	if len(fields) == 0 || m.projectID != fields[0] {
		t.Errorf("clicked row %q opened %q", fields, m.projectID)
	}
}

func TestUnknownColumnFallsBackLikeTheApp(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"working", 0},
		{"idle", 0},
		{"review_pending", 1},
		{"pr_open", 1},
		{"ci_failed", 2},
		{"needs_input", 2},
		{"mergeable", 3},
		{"merged", 3},
	}
	for _, tc := range cases {
		for _, column := range []string{"", "shipping"} {
			s := worker("demo-1", "demo", column, tc.status, 1)
			board := BuildBoard(Snapshot{Sessions: []Session{s}}, "demo")
			if got := ids(board.Columns[tc.want]); !equalStrings(got, []string{"demo-1"}) {
				t.Errorf("status %s, column %q: want it in %s, board = %#v", tc.status, column, Columns[tc.want], board.Columns)
			}
		}
	}
}
