package boardtui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// fakeLoader 는 부를 때마다 snaps 를 차례로 돌려주고 부른 횟수를 센다.
type fakeLoader struct {
	snaps []Snapshot
	errs  []error
	calls int
}

func (f *fakeLoader) load(context.Context) (Snapshot, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return Snapshot{}, f.errs[i]
	}
	if i >= len(f.snaps) {
		i = len(f.snaps) - 1
	}
	return f.snaps[i], nil
}

func demoSnapshot() Snapshot {
	orchestrator := worker("demo-1", "demo", ColumnBuilding, "working", 9)
	orchestrator.Kind = "orchestrator"
	needsInput := worker("demo-3", "demo", ColumnValidating, "needs_input", 2)
	needsInput.DisplayName = "입력을 기다리는 일"
	needsInput.PRs = []PR{{Number: 12, URL: "https://example.test/pr/12", State: "open", CI: "failing"}}
	return Snapshot{
		Projects: []Project{{ID: "demo", Name: "demo"}, {ID: "water", Name: "water"}},
		Sessions: []Session{
			orchestrator,
			worker("demo-2", "demo", ColumnBuilding, "working", 5),
			needsInput,
			worker("demo-4", "demo", ColumnReady, "mergeable", 1),
			worker("water-1", "water", ColumnNeedsReview, "review_pending", 3),
		},
	}
}

func newTestModel(t *testing.T, loader *fakeLoader, opts ...func(*Options)) Model {
	t.Helper()
	o := Options{
		Load:         loader.load,
		Labels:       LabelsFor("ko_KR.UTF-8"),
		Now:          func() time.Time { return at(10) },
		PollInterval: time.Millisecond,
		Debounce:     time.Millisecond,
	}
	for _, opt := range opts {
		opt(&o)
	}
	m := New(o)
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	return m
}

// start 는 Init 의 명령을 실행해 첫 스냅숏을 받은 모델을 준다. 주기 Tick 은
// 넘기지 않는다(넘기면 그 Tick 이 연 읽기를 시험이 실행하지 않은 채 남는다).
func start(t *testing.T, m Model) Model {
	t.Helper()
	for _, msg := range drain(m.Init()) {
		if _, ok := msg.(snapshotMsg); ok {
			m = send(t, m, msg)
		}
	}
	return m
}

func send(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	model, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return model
}

func sendCmd(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// drain 은 명령을 실행해 나온 메시지를 모은다. 묶음 명령은 펼친다. 시험의
// Tick 은 1ms 라 바로 끝난다.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func screen(m Model) string { return ansi.Strip(m.Render()) }

func key(code rune) tea.KeyPressMsg {
	if code >= 'a' && code <= 'z' {
		return tea.KeyPressMsg{Code: code, Text: string(code)}
	}
	return tea.KeyPressMsg{Code: code}
}

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func TestProjectsScreenShowsCountsPerProject(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))

	out := screen(m)
	for _, want := range []string{"LADO 보드", "프로젝트", "개발 중", "준비 완료", "사람 대기", "demo", "water", "작업 중"} {
		if !strings.Contains(out, want) {
			t.Errorf("projects screen lacks %q:\n%s", want, out)
		}
	}
	row := lineContaining(out, "demo ")
	if fields := strings.Fields(row); len(fields) < 6 || fields[1] != "demo" || fields[2] != "1" || fields[3] != "1" || fields[4] != "0" || fields[5] != "1" {
		t.Errorf("demo row = %q, want counts 1 1 0 1", row)
	}
}

func TestNarrowProjectsScreenKeepsAttentionColumn(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))

	for _, width := range []int{60, 50} {
		m = send(t, m, tea.WindowSizeMsg{Width: width, Height: 20})
		out := screen(m)
		head := lineAt(out, 2)
		if strings.Contains(head, "…") || !strings.Contains(head, "대기") {
			t.Errorf("width %d header = %q, want short titles that fit and keep «대기»", width, head)
		}
		row := strings.Fields(lineContaining(out, "demo "))
		if len(row) < 7 || row[6] != "1" {
			t.Errorf("width %d demo row = %q, want five counts ending in attention 1", width, row)
		}
		for i, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("width %d line %d is %d cells wide: %q", width, i, w, line)
			}
		}
	}
}

func TestEnterOpensBoardAndEscReturns(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))

	m = send(t, m, key(tea.KeyEnter))
	out := screen(m)
	if !strings.Contains(out, "← 프로젝트") || !strings.Contains(out, "demo-2") || strings.Contains(out, "water-1") {
		t.Fatalf("after Enter, want demo board only:\n%s", out)
	}
	if strings.Contains(out, "demo-1") {
		t.Errorf("orchestrator demo-1 must not be a card:\n%s", out)
	}

	m = send(t, m, key(tea.KeyEscape))
	if out := screen(m); !strings.Contains(out, "사람 대기") || strings.Contains(out, "← 프로젝트") {
		t.Fatalf("after Esc, want projects screen:\n%s", out)
	}
}

func TestClickingProjectRowOpensItsBoard(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))

	y := lineIndex(screen(m), "water ")
	if y < 0 {
		t.Fatalf("water row missing")
	}
	m = send(t, m, click(5, y))

	if out := screen(m); !strings.Contains(out, "water-1") || strings.Contains(out, "demo-2") {
		t.Fatalf("click on water row should open water board:\n%s", out)
	}
}

func TestBoardArrowKeysSkipEmptyColumns(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader, func(o *Options) { o.ProjectID = "demo" }))

	if got := m.selected(); got == nil || got.ID != "demo-2" {
		t.Fatalf("initial selection = %v, want demo-2", got)
	}
	m = send(t, m, key(tea.KeyRight))
	if got := m.selected(); got == nil || got.ID != "demo-3" {
		t.Fatalf("after →, selection = %v, want demo-3", got)
	}
	m = send(t, m, key(tea.KeyRight))
	if got := m.selected(); got == nil || got.ID != "demo-4" {
		t.Fatalf("after →→, selection = %v, want demo-4 (empty «리뷰 중» skipped)", got)
	}
	m = send(t, m, key(tea.KeyRight))
	if got := m.selected(); got == nil || got.ID != "demo-4" {
		t.Fatalf("→ at the last column must stay, got %v", got)
	}
	if out := screen(m); !strings.Contains(out, "demo-4") {
		t.Fatalf("detail should name the selected card:\n%s", out)
	}
}

func TestClickingCardSelectsItAndShowsDetail(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader, func(o *Options) { o.ProjectID = "demo" }))

	out := screen(m)
	y := lineIndex(out, "입력을 기다리는 일")
	x := strings.Index(lineAt(out, y), "입력을")
	if y < 0 || x < 0 {
		t.Fatalf("card not drawn:\n%s", out)
	}
	m = send(t, m, click(ansi.StringWidth(lineAt(out, y)[:x]), y))

	if got := m.selected(); got == nil || got.ID != "demo-3" {
		t.Fatalf("selection = %v, want demo-3", got)
	}
	out = screen(m)
	for _, want := range []string{"demo-3", "입력 필요", "PR #12", "https://example.test/pr/12"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail lacks %q:\n%s", want, out)
		}
	}
}

func TestNarrowBoardUsesTabsAndClickSwitchesColumn(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader, func(o *Options) { o.ProjectID = "demo" }))
	m = send(t, m, tea.WindowSizeMsg{Width: 60, Height: 24})

	out := screen(m)
	if strings.Contains(out, "demo-4") {
		t.Fatalf("narrow board shows one column at a time:\n%s", out)
	}
	tabs := lineAt(out, 2)
	x := strings.Index(tabs, "준비 완료")
	if x < 0 {
		t.Fatalf("tab row lacks «준비 완료»: %q", tabs)
	}
	m = send(t, m, click(ansi.StringWidth(tabs[:x]), 2))

	if got := m.selected(); got == nil || got.ID != "demo-4" {
		t.Fatalf("after tab click, selection = %v, want demo-4", got)
	}
}

func TestChangesAreDebouncedIntoOneReload(t *testing.T) {
	second := demoSnapshot()
	second.Sessions = append(second.Sessions, worker("demo-9", "demo", ColumnBuilding, "working", 9))
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot(), second}}
	m := start(t, newTestModel(t, loader))
	m = send(t, m, LiveMsg{Connected: true})
	calls := loader.calls

	m, first := sendCmd(t, m, ChangedMsg{})
	m, again := sendCmd(t, m, ChangedMsg{})
	if first == nil || again != nil {
		t.Fatalf("first change should schedule one reload, second should join it (first=%v again=%v)", first != nil, again != nil)
	}
	for _, msg := range drain(first) {
		var cmd tea.Cmd
		m, cmd = sendCmd(t, m, msg)
		for _, next := range drain(cmd) {
			m = send(t, m, next)
		}
	}
	if loader.calls != calls+1 {
		t.Fatalf("loads = %d, want %d", loader.calls, calls+1)
	}
	m = send(t, m, key(tea.KeyEnter))
	if !strings.Contains(screen(m), "demo-9") {
		t.Fatalf("reloaded snapshot not shown:\n%s", screen(m))
	}
}

func TestChangeDuringLoadReloadsAgainAfterward(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))
	m = send(t, m, LiveMsg{Connected: true})

	m, load := sendCmd(t, m, reloadMsg{})
	if load == nil {
		t.Fatal("reload should start a load")
	}
	m, none := sendCmd(t, m, reloadMsg{})
	if none != nil {
		t.Fatal("a reload while loading must not start a second load")
	}
	msgs := drain(load)
	_, follow := sendCmd(t, m, msgs[0])
	if follow == nil {
		t.Fatal("a change seen during the load must reload once more")
	}
}

func TestPollingOnlyWhileDisconnected(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))
	m = send(t, m, LiveMsg{Connected: false})
	if !strings.Contains(screen(m), "연결 끊김") {
		t.Fatalf("header should say disconnected:\n%s", screen(m))
	}
	calls := loader.calls
	m, cmd := sendCmd(t, m, tickMsg(at(11)))
	for _, msg := range drain(cmd) {
		if _, ok := msg.(snapshotMsg); ok {
			m = send(t, m, msg)
		}
	}
	if loader.calls != calls+1 {
		t.Fatalf("disconnected tick loads = %d, want 1", loader.calls-calls)
	}

	m = send(t, m, LiveMsg{Connected: true})
	if !strings.Contains(screen(m), "실시간") {
		t.Fatalf("header should say live:\n%s", screen(m))
	}
	calls = loader.calls
	_, cmd = sendCmd(t, m, tickMsg(at(12)))
	for _, msg := range drain(cmd) {
		if _, ok := msg.(snapshotMsg); ok {
			t.Fatalf("a live tick must not load")
		}
	}
	if loader.calls != calls {
		t.Fatalf("live tick loaded %d times", loader.calls-calls)
	}
}

func TestLoadErrorKeepsLastSnapshot(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot(), demoSnapshot()}, errs: []error{nil, errors.New("daemon down")}}
	m := start(t, newTestModel(t, loader))
	m, cmd := sendCmd(t, m, key('r'))
	for _, msg := range drain(cmd) {
		m = send(t, m, msg)
	}

	out := screen(m)
	if !strings.Contains(out, "읽기 실패") || !strings.Contains(out, "daemon down") || !strings.Contains(out, "water") {
		t.Fatalf("want error in header and last rows kept:\n%s", out)
	}
}

func TestQuitKeys(t *testing.T) {
	loader := &fakeLoader{snaps: []Snapshot{demoSnapshot()}}
	m := start(t, newTestModel(t, loader))
	for _, k := range []tea.KeyPressMsg{key('q'), {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd := sendCmd(t, m, k)
		msgs := drain(cmd)
		if len(msgs) != 1 {
			t.Fatalf("%s: msgs = %v, want quit", k.String(), msgs)
		}
		if _, ok := msgs[0].(tea.QuitMsg); !ok {
			t.Fatalf("%s: got %T, want tea.QuitMsg", k.String(), msgs[0])
		}
	}
}

func lineAt(out string, y int) string {
	lines := strings.Split(out, "\n")
	if y < 0 || y >= len(lines) {
		return ""
	}
	return lines[y]
}

func lineIndex(out, needle string) int {
	for i, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

func lineContaining(out, needle string) string { return lineAt(out, lineIndex(out, needle)) }
