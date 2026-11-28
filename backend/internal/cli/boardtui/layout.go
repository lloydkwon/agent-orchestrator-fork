package boardtui

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
)

// 화면 배치. 그리기(view.go)와 클릭 판정(handleClick)이 같은 값을 쓴다.
//
//	0  머리줄
//	1  빈 줄
//	2  프로젝트 표 머리 / 칸반 열 이름(좁으면 탭)
//	3~ 프로젝트 행 / 카드(카드 하나에 cardHeight 줄)
//	   … 다른 열 · 상세 · 도움말(맨 아래)
const (
	listTop      = 3
	cardHeight   = 3
	detailLines  = 4
	footerLines  = 1
	narrowWidth  = 80
	columnGap    = 1
	defaultWidth = 120
	backHitPad   = 1
	// unbounded 는 높이 제한이 없다는 뜻이다(--print). 0 은 그릴 자리가 없다는 뜻이다.
	unbounded = -1
)

type span struct{ start, end int }

func (s span) contains(x int) bool { return x >= s.start && x < s.end }

type boardLayout struct {
	width        int
	narrow       bool
	columnWidth  int
	columns      [4]span
	tabs         [4]span
	tabText      [4]string
	cardsVisible int // unbounded 면 높이 제한 없음(--print)
}

func (m Model) screenWidth() int {
	if m.width > 0 {
		return m.width
	}
	return defaultWidth
}

// projectRowsVisible 은 프로젝트 표에 보이는 행 수다. 높이 제한이 없으면 unbounded 다.
func (m Model) projectRowsVisible() int {
	if m.height <= 0 {
		return unbounded
	}
	return max(0, m.height-listTop-1-footerLines)
}

func (m Model) layout() boardLayout {
	l := boardLayout{width: m.screenWidth()}
	l.narrow = l.width < narrowWidth
	if l.narrow {
		l.columnWidth = l.width
		x := 0
		for i := range Columns {
			l.tabText[i] = fmt.Sprintf(" %s %d ", m.opts.Labels.Columns[i], len(m.board.Columns[i]))
			w := ansi.StringWidth(l.tabText[i])
			l.tabs[i] = span{start: x, end: x + w}
			x += w + columnGap
		}
	} else {
		l.columnWidth = (l.width - columnGap*(len(Columns)-1)) / len(Columns)
		for i := range Columns {
			start := i * (l.columnWidth + columnGap)
			l.columns[i] = span{start: start, end: start + l.columnWidth}
		}
	}
	l.cardsVisible = unbounded
	if m.height > 0 {
		reserved := listTop + detailLines + footerLines
		l.cardsVisible = max(0, (m.height-reserved)/cardHeight)
	}
	return l
}

// columnAt 은 x 칸이 가리키는 열이다. 넓은 화면은 열 영역, 좁은 화면은 탭으로 본다.
func (l boardLayout) columnAt(x int) int {
	areas := l.columns
	if l.narrow {
		areas = l.tabs
	}
	for i, area := range areas {
		if area.contains(x) {
			return i
		}
	}
	return -1
}

// handleClick 은 왼쪽 클릭을 처리한다. 프로젝트 행을 누르면 그 칸반을 열고,
// 카드를 누르면 고르고, 열 이름·탭을 누르면 그 열로 간다.
func (m Model) handleClick(x, y int) Model {
	if m.screen == screenProjects {
		row := y - listTop
		if row < 0 {
			return m
		}
		if visible := m.projectRowsVisible(); visible != unbounded && row >= visible {
			return m
		}
		return m.openProject(m.projectOffset + row)
	}
	if y == 0 && x < ansi.StringWidth(m.opts.Labels.Back)+backHitPad {
		return m.backToProjects()
	}
	l := m.layout()
	if y == listTop-1 {
		if c := l.columnAt(x); c >= 0 {
			m.column = c
			return m.clamp()
		}
		return m
	}
	offset := y - listTop
	if offset < 0 || offset%cardHeight == cardHeight-1 {
		return m
	}
	slot := offset / cardHeight
	if l.cardsVisible != unbounded && slot >= l.cardsVisible {
		return m
	}
	column := m.column
	if !l.narrow {
		column = l.columnAt(x)
	}
	if column < 0 {
		return m
	}
	index := m.offsets[column] + slot
	if index >= len(m.board.Columns[column]) {
		return m
	}
	m.column = column
	m.rows[column] = index
	return m.clamp()
}
