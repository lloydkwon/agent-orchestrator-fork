package boardtui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	defaultPollInterval = 5 * time.Second
	defaultDebounce     = 500 * time.Millisecond
	loadTimeout         = 15 * time.Second
)

// Loader 는 daemon 에서 프로젝트·세션을 한 번 읽는다.
type Loader func(ctx context.Context) (Snapshot, error)

// Options 는 화면을 만드는 설정이다.
type Options struct {
	Load   Loader
	Labels Labels
	Now    func() time.Time
	// ProjectID 가 있으면 처음부터 그 프로젝트의 칸반을 연다.
	ProjectID string
	// PollInterval 은 실시간 연결이 끊겼을 때 다시 읽는 간격이다.
	PollInterval time.Duration
	// Debounce 는 변경 알림을 모았다가 한 번 읽기까지 기다리는 시간이다.
	Debounce time.Duration
	// Static 은 한 번만 그려 출력하는 --print 용이다. 머리줄에 연결 상태를 쓰지 않는다.
	Static bool
}

// ChangedMsg 는 daemon 이 무언가 바뀌었다고 알렸다는 뜻이다.
type ChangedMsg struct{}

// LiveMsg 는 실시간 이벤트 연결이 붙었는지 끊겼는지 알린다.
type LiveMsg struct{ Connected bool }

type snapshotMsg struct {
	snap Snapshot
	err  error
}

// SnapshotLoaded 는 이미 읽어 둔 스냅숏을 화면에 넣는 메시지다(--print 용).
func SnapshotLoaded(snap Snapshot) tea.Msg { return snapshotMsg{snap: snap} }

type reloadMsg struct{}

type tickMsg time.Time

type screenKind int

const (
	screenProjects screenKind = iota
	screenBoard
)

// Model 은 bubbletea 모델이다. 값으로 다루며 Update 는 바뀐 사본을 돌려준다.
type Model struct {
	opts Options

	width, height int

	snap      Snapshot
	summaries []ProjectSummary
	board     Board
	loaded    bool
	loadErr   error
	live      bool

	loading         bool
	reloadScheduled bool
	dirty           bool

	screen        screenKind
	projectCursor int
	projectOffset int
	projectID     string
	column        int
	rows          [4]int
	offsets       [4]int
}

// New 는 화면 모델을 만든다.
func New(opts Options) Model {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.Debounce <= 0 {
		opts.Debounce = defaultDebounce
	}
	m := Model{opts: opts, loading: true}
	if opts.ProjectID != "" {
		m.screen = screenBoard
		m.projectID = opts.ProjectID
	}
	return m
}

// Init 은 첫 읽기와 주기 Tick 을 시작한다.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), m.tickCmd())
}

func (m Model) loadCmd() tea.Cmd {
	load := m.opts.Load
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		snap, err := load(ctx)
		return snapshotMsg{snap: snap, err: err}
	}
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.opts.PollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// startLoad 는 읽기가 돌고 있지 않으면 읽기를 시작하고, 돌고 있으면 끝난 뒤에
// 한 번 더 읽도록 표시한다.
func (m Model) startLoad() (Model, tea.Cmd) {
	if m.loading {
		m.dirty = true
		return m, nil
	}
	m.loading = true
	return m, m.loadCmd()
}

// Update 는 메시지를 처리한다.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.clamp(), nil
	case snapshotMsg:
		return m.applySnapshot(msg)
	case ChangedMsg:
		if m.reloadScheduled {
			return m, nil
		}
		m.reloadScheduled = true
		return m, tea.Tick(m.opts.Debounce, func(time.Time) tea.Msg { return reloadMsg{} })
	case reloadMsg:
		m.reloadScheduled = false
		return m.startLoad()
	case LiveMsg:
		m.live = msg.Connected
		return m, nil
	case tickMsg:
		next := m.tickCmd()
		if m.live {
			return m, next
		}
		var load tea.Cmd
		m, load = m.startLoad()
		return m, tea.Batch(load, next)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		return m.handleClick(msg.X, msg.Y), nil
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			return m.moveRow(-1), nil
		case tea.MouseWheelDown:
			return m.moveRow(1), nil
		}
	}
	return m, nil
}

func (m Model) applySnapshot(msg snapshotMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.err != nil {
		m.loadErr = msg.err
	} else {
		m.loadErr = nil
		m.loaded = true
		m.snap = msg.snap
		m = m.replaceSummaries(Summarize(msg.snap))
		m = m.rebuildBoard()
	}
	m = m.clamp()
	if m.dirty {
		m.dirty = false
		return m.startLoad()
	}
	return m, nil
}

// replaceSummaries 는 프로젝트 목록을 바꾸되, 커서는 행 번호가 아니라 고른 프로젝트를
// 따라가게 한다. 그 프로젝트가 사라졌으면 같은 행 번호에 둔다.
func (m Model) replaceSummaries(next []ProjectSummary) Model {
	var keep string
	if m.projectCursor >= 0 && m.projectCursor < len(m.summaries) {
		keep = m.summaries[m.projectCursor].Project.ID
	}
	m.summaries = next
	for i, sum := range next {
		if sum.Project.ID == keep {
			m.projectCursor = i
			break
		}
	}
	return m
}

// rebuildBoard 는 지금 고른 세션을 될 수 있으면 그대로 둔 채 칸반을 다시 만든다.
func (m Model) rebuildBoard() Model {
	if m.projectID == "" {
		m.board = Board{}
		return m
	}
	var keep string
	if s := m.selected(); s != nil {
		keep = s.ID
	}
	firstBuild := !m.loadedBoard()
	m.board = BuildBoard(m.snap, m.projectID)
	if keep != "" {
		for c := range m.board.Columns {
			for r, s := range m.board.Columns[c] {
				if s.ID == keep {
					m.column, m.rows[c] = c, r
					return m
				}
			}
		}
	}
	// 사용자가 고른 열은 비어 있어도 그대로 둔다. 처음 열 때만 비지 않은 열로 간다.
	if firstBuild {
		m.column = m.firstNonEmptyColumn()
	}
	return m
}

func (m Model) loadedBoard() bool {
	for _, column := range m.board.Columns {
		if len(column) > 0 {
			return true
		}
	}
	return false
}

func (m Model) firstNonEmptyColumn() int {
	for i, column := range m.board.Columns {
		if len(column) > 0 {
			return i
		}
	}
	return 0
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		return m.startLoad()
	case "up", "k":
		return m.moveRow(-1), nil
	case "down", "j":
		return m.moveRow(1), nil
	}
	if m.screen == screenProjects {
		switch msg.String() {
		case "enter", "right", "l":
			return m.openProject(m.projectCursor), nil
		case "home", "g":
			m.projectCursor = 0
			return m.clamp(), nil
		case "end", "G":
			m.projectCursor = len(m.summaries) - 1
			return m.clamp(), nil
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "backspace":
		return m.backToProjects(), nil
	case "left", "h", "shift+tab":
		return m.moveColumn(-1), nil
	case "right", "l", "tab":
		return m.moveColumn(1), nil
	}
	return m, nil
}

func (m Model) openProject(index int) Model {
	if index < 0 || index >= len(m.summaries) {
		return m
	}
	m.projectCursor = index
	m.projectID = m.summaries[index].Project.ID
	m.screen = screenBoard
	m.rows, m.offsets = [4]int{}, [4]int{}
	m.board = Board{}
	m.column = 0
	m = m.rebuildBoard()
	return m.clamp()
}

func (m Model) backToProjects() Model {
	for i, sum := range m.summaries {
		if sum.Project.ID == m.projectID {
			m.projectCursor = i
		}
	}
	m.screen = screenProjects
	m.projectID = ""
	m.board = Board{}
	return m.clamp()
}

func (m Model) moveRow(delta int) Model {
	if m.screen == screenProjects {
		m.projectCursor += delta
		return m.clamp()
	}
	m.rows[m.column] += delta
	return m.clamp()
}

// moveColumn 은 delta 방향의 다음 비지 않은 열로 간다. 없으면 그대로 둔다.
func (m Model) moveColumn(delta int) Model {
	for c := m.column + delta; c >= 0 && c < len(Columns); c += delta {
		if len(m.board.Columns[c]) > 0 {
			m.column = c
			return m.clamp()
		}
	}
	return m
}

// selected 는 칸반에서 고른 세션이다. 없으면 nil 이다.
func (m Model) selected() *Session {
	if m.screen != screenBoard {
		return nil
	}
	cards := m.board.Columns[m.column]
	row := m.rows[m.column]
	if row < 0 || row >= len(cards) {
		return nil
	}
	s := cards[row]
	return &s
}

// clamp 는 커서와 스크롤 위치를 목록 안으로 맞춘다.
func (m Model) clamp() Model {
	m.projectCursor = clampIndex(m.projectCursor, len(m.summaries))
	visible := m.projectRowsVisible()
	m.projectOffset = scrollInto(m.projectOffset, m.projectCursor, visible, len(m.summaries))
	cardsVisible := m.layout().cardsVisible
	for c := range m.board.Columns {
		n := len(m.board.Columns[c])
		m.rows[c] = clampIndex(m.rows[c], n)
		m.offsets[c] = scrollInto(m.offsets[c], m.rows[c], cardsVisible, n)
	}
	return m
}

func clampIndex(i, n int) int {
	if n == 0 || i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// scrollInto 는 cursor 가 [offset, offset+visible) 안에 들도록 offset 을 옮긴다.
// 높이 제한이 없으면(unbounded) 0 이고, 그릴 자리가 없으면(0) cursor 다.
func scrollInto(offset, cursor, visible, n int) int {
	if visible == unbounded || n <= visible {
		return 0
	}
	if visible == 0 {
		return cursor
	}
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+visible {
		offset = cursor - visible + 1
	}
	if offset > n-visible {
		offset = n - visible
	}
	if offset < 0 {
		offset = 0
	}
	return offset
}

// View 는 전체 화면(대체 화면 + 마우스 클릭)으로 그린다.
func (m Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
