package boardtui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 색은 터미널 테마를 따르도록 ANSI 16색 번호를 쓴다.
var (
	columnColors = [4]string{"12", "11", "13", "10"}
	styleDim     = lipgloss.NewStyle().Faint(true)
	styleBold    = lipgloss.NewStyle().Bold(true)
	styleAlert   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleLive    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleTabOn   = lipgloss.NewStyle().Reverse(true).Bold(true)
)

func columnStyle(i int) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(columnColors[i]))
}

// Render 는 화면 전체를 글로 그린다. 높이가 정해져 있으면 도움말을 맨 아래 둔다.
func (m Model) Render() string {
	w := m.screenWidth()
	lines := []string{m.headerLine(w), ""}
	var body []string
	bottom := make([]string, 0, 1)
	switch {
	case !m.loaded && m.loadErr == nil:
		body = []string{"  " + m.opts.Labels.Loading}
	case m.screen == screenProjects:
		body = m.projectLines(w)
	default:
		body, bottom = m.boardLines(w)
	}
	help := m.opts.Labels.HelpProjects
	if m.screen == screenBoard {
		help = m.opts.Labels.HelpBoard
	}
	bottom = append(bottom, styleDim.Render(fit(help, w)))
	lines = append(lines, body...)
	if m.height > 0 {
		for len(lines)+len(bottom) < m.height {
			lines = append(lines, "")
		}
		if keep := m.height - len(bottom); keep >= 0 && len(lines) > keep {
			lines = lines[:keep]
		}
	}
	return strings.Join(append(lines, bottom...), "\n")
}

func (m Model) headerLine(w int) string {
	labels := m.opts.Labels
	var left string
	if m.screen == screenBoard {
		left = styleDim.Render(labels.Back) + "  " + styleBold.Render(m.projectID)
		if sum, ok := m.summaryFor(m.projectID); ok {
			left += styleDim.Render(" · " + labels.Orchestrator + " " + m.orchestratorText(sum))
			left += styleDim.Render(fmt.Sprintf(" · %s %d", labels.Archived, sum.Archived))
		}
	} else {
		left = styleBold.Render(labels.Title)
	}
	var right string
	switch {
	case m.loadErr != nil:
		right = styleAlert.Render(labels.LoadFailed + ": " + oneLine(m.loadErr.Error()))
	case m.opts.Static:
	case m.live:
		right = styleLive.Render(labels.Live)
	default:
		right = styleWarn.Render(labels.Polling)
	}
	right = strings.TrimSpace(right + "  " + m.opts.Now().Format("15:04"))
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 2 {
		return fit(left+"  "+right, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) summaryFor(projectID string) (ProjectSummary, bool) {
	for _, sum := range m.summaries {
		if sum.Project.ID == projectID {
			return sum, true
		}
	}
	return ProjectSummary{}, false
}

func (m Model) orchestratorText(sum ProjectSummary) string {
	if sum.Orchestrator == nil {
		return m.opts.Labels.None
	}
	return m.opts.Labels.StatusLabel(*sum.Orchestrator)
}

// 프로젝트 표의 칸. 폭이 모자라면 먼저 뒤쪽 칸(최근 활동, 오케스트레이터)을 빼고,
// 그래도 모자라면 짧은 제목(개발·검증·리뷰·준비·대기)으로 바꾼다. 열별 수와
// 사람 대기 칸은 끝까지 남긴다.
type tableColumn struct {
	title string
	width int
	cell  func(ProjectSummary) string
}

const (
	minNameWidth = 12
	rowPrefix    = 2
	cellGap      = 2
	keptColumns  = len(Columns) + 1 // 열별 수 + 사람 대기
)

func (m Model) tableColumns(w int) (nameWidth int, cols []tableColumn) {
	for _, short := range []bool{false, true} {
		all := m.buildTableColumns(short)
		for n := len(all); n >= keptColumns; n-- {
			if name := w - tableWidth(all[:n]); name >= minNameWidth {
				return name, all[:n]
			}
		}
	}
	kept := m.buildTableColumns(true)[:keptColumns]
	return max(minNameWidth, w-tableWidth(kept)), kept
}

func tableWidth(cols []tableColumn) int {
	used := rowPrefix
	for _, c := range cols {
		used += cellGap + c.width
	}
	return used
}

func (m Model) buildTableColumns(short bool) []tableColumn {
	labels := m.opts.Labels
	titles, attention := labels.Columns, labels.Attention
	if short {
		titles, attention = labels.ShortColumns, labels.ShortAttention
	}
	cols := make([]tableColumn, 0, keptColumns+2)
	for i := range Columns {
		cols = append(cols, tableColumn{title: titles[i], cell: func(s ProjectSummary) string {
			return countText(s.Counts[i], columnStyle(i))
		}})
	}
	cols = append(cols,
		tableColumn{title: attention, cell: func(s ProjectSummary) string { return countText(s.Attention, styleAlert) }},
		tableColumn{title: labels.Orchestrator, cell: func(s ProjectSummary) string {
			if s.Orchestrator == nil {
				return styleDim.Render(labels.None)
			}
			return m.orchestratorText(s)
		}},
		tableColumn{title: labels.LastActivity, cell: func(s ProjectSummary) string {
			return styleDim.Render(labels.Ago(m.opts.Now(), s.LastActivity))
		}},
	)
	for i := range cols {
		cols[i].width = max(ansi.StringWidth(cols[i].title), 3)
	}
	return cols
}

func countText(n int, style lipgloss.Style) string {
	if n == 0 {
		return styleDim.Render("0")
	}
	return style.Bold(true).Render(strconv.Itoa(n))
}

func (m Model) projectLines(w int) []string {
	if len(m.summaries) == 0 {
		return []string{"  " + m.opts.Labels.NoProjects}
	}
	nameWidth, cols := m.tableColumns(w)
	head := "  " + pad(styleDim.Render(m.opts.Labels.Project), nameWidth)
	for _, c := range cols {
		head += "  " + padLeft(styleDim.Render(c.title), c.width)
	}
	lines := []string{fit(head, w)}
	end := len(m.summaries)
	if visible := m.projectRowsVisible(); visible != unbounded {
		end = min(end, m.projectOffset+visible)
	}
	for i := m.projectOffset; i < end; i++ {
		lines = append(lines, fit(m.projectRow(m.summaries[i], i == m.projectCursor, nameWidth, cols), w))
	}
	return lines
}

func (m Model) projectRow(sum ProjectSummary, selected bool, nameWidth int, cols []tableColumn) string {
	name := sum.Project.Name
	if name == "" {
		name = sum.Project.ID
	}
	tag := ""
	switch {
	case sum.Unlisted:
		tag = " (" + m.opts.Labels.Unlisted + ")"
	case sum.Project.FolderMissing:
		tag = " (" + m.opts.Labels.FolderMissing + ")"
	}
	nameText := ansi.Truncate(name, nameWidth-ansi.StringWidth(tag), "…")
	prefix := "  "
	if selected {
		prefix = styleBold.Render("› ")
		nameText = styleBold.Render(nameText)
	}
	row := prefix + pad(nameText+styleWarn.Render(tag), nameWidth)
	for _, c := range cols {
		row += "  " + padLeft(c.cell(sum), c.width)
	}
	return row
}

// boardLines 는 칸반 몸통과, 맨 아래에 붙일 다른 열·상세 줄을 따로 돌려준다.
func (m Model) boardLines(w int) (body, bottom []string) {
	l := m.layout()
	if l.narrow {
		body = m.narrowBoard(l)
	} else {
		body = m.wideBoard(l)
	}
	return body, append(bottom, m.detailLines(w)...)
}

func (m Model) wideBoard(l boardLayout) []string {
	header := make([]string, len(Columns))
	for i := range Columns {
		text := fmt.Sprintf("%s %d%s", m.opts.Labels.Columns[i], len(m.board.Columns[i]), m.scrollHint(i, l))
		style := columnStyle(i).Bold(true)
		if i == m.column {
			style = style.Underline(true)
		}
		header[i] = pad(style.Render(ansi.Truncate(text, l.columnWidth, "…")), l.columnWidth)
	}
	lines := []string{strings.Join(header, " ")}
	for slot := 0; slot < m.slotCount(l); slot++ {
		parts := make([][cardHeight]string, len(Columns))
		for i := range Columns {
			parts[i] = m.cardLines(i, m.offsets[i]+slot, l.columnWidth)
		}
		for line := 0; line < cardHeight; line++ {
			row := make([]string, len(Columns))
			for i := range Columns {
				row[i] = parts[i][line]
			}
			lines = append(lines, strings.Join(row, " "))
		}
	}
	return lines
}

func (m Model) narrowBoard(l boardLayout) []string {
	if m.opts.Static {
		return m.stackedBoard(l)
	}
	tabs := ""
	for i := range Columns {
		if i > 0 {
			tabs += strings.Repeat(" ", columnGap)
		}
		style := columnStyle(i)
		if i == m.column {
			style = styleTabOn
		}
		tabs += style.Render(l.tabText[i])
	}
	lines := []string{fit(tabs, l.width)}
	for slot := 0; slot < m.slotCount(l); slot++ {
		card := m.cardLines(m.column, m.offsets[m.column]+slot, l.columnWidth)
		lines = append(lines, card[:]...)
	}
	return lines
}

// stackedBoard 는 좁은 폭의 --print 용이다. 탭 대신 열을 차례로 모두 적는다.
func (m Model) stackedBoard(l boardLayout) []string {
	var lines []string
	for i := range Columns {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, columnStyle(i).Bold(true).Render(fit(strings.TrimSpace(l.tabText[i]), l.width)))
		for index := 0; index < max(1, len(m.board.Columns[i])); index++ {
			card := m.cardLines(i, index, l.columnWidth)
			lines = append(lines, card[:cardHeight-1]...)
		}
	}
	return lines
}

// slotCount 는 그릴 카드 칸 수다. 높이 제한이 없으면 가장 긴 열만큼 그린다.
func (m Model) slotCount(l boardLayout) int {
	if l.cardsVisible != unbounded {
		return l.cardsVisible
	}
	longest := 1
	for i := range Columns {
		if l.narrow && i != m.column {
			continue
		}
		longest = max(longest, len(m.board.Columns[i]))
	}
	return longest
}

func (m Model) scrollHint(column int, l boardLayout) string {
	if l.cardsVisible == unbounded {
		return ""
	}
	hint := ""
	if m.offsets[column] > 0 {
		hint += " ↑"
	}
	if m.offsets[column]+l.cardsVisible < len(m.board.Columns[column]) {
		hint += " ↓"
	}
	return hint
}

func (m Model) cardLines(column, index, width int) [cardHeight]string {
	var out [cardHeight]string
	cards := m.board.Columns[column]
	blank := strings.Repeat(" ", width)
	for i := range out {
		out[i] = blank
	}
	if index >= len(cards) {
		if index == 0 {
			out[0] = pad(styleDim.Render(" "+m.opts.Labels.EmptyColumn), width)
		}
		return out
	}
	s := cards[index]
	selected := column == m.column && index == m.rows[column]
	bar := styleDim.Render("│ ")
	name := ansi.Truncate(cardName(s), width-2, "…")
	if selected {
		bar = columnStyle(column).Bold(true).Render("┃ ")
		name = styleBold.Render(name)
	}
	out[0] = pad(bar+name, width)
	out[1] = pad(bar+m.cardMeta(s, width-2), width)
	return out
}

func cardName(s Session) string {
	if name := oneLine(s.DisplayName); name != "" {
		return name
	}
	return s.ID
}

func (m Model) cardMeta(s Session, width int) string {
	status := m.opts.Labels.StatusLabel(s)
	parts := []string{status}
	if len(s.PRs) > 0 {
		parts = append(parts, fmt.Sprintf("PR #%d", s.PRs[0].Number))
	}
	parts = append(parts, m.opts.Labels.Ago(m.opts.Now(), LastSeen(s)))
	text := ansi.Truncate(strings.Join(parts, " · "), width, "…")
	if NeedsAttention(s) {
		return styleAlert.Render(text)
	}
	return styleDim.Render(text)
}

func (m Model) detailLines(w int) []string {
	lines := []string{styleDim.Render(strings.Repeat("─", w))}
	s := m.selected()
	if s == nil {
		message := m.opts.Labels.EmptyColumn
		if m.boardIsEmpty() {
			message = m.opts.Labels.NoSessions
		}
		return append(lines, styleDim.Render(fit(" "+message, w)), "", "")
	}
	facts := []string{styleBold.Render(s.ID), m.opts.Labels.StatusLabel(*s), m.opts.Labels.Columns[m.column]}
	if agent := strings.Trim(s.Harness+"/"+s.Model, "/"); agent != "" {
		facts = append(facts, agent)
	}
	if s.Mode != "" {
		facts = append(facts, s.Mode)
	}
	prs := m.opts.Labels.NoPR
	if len(s.PRs) > 0 {
		parts := make([]string, 0, len(s.PRs))
		for _, pr := range s.PRs {
			parts = append(parts, strings.Join(nonEmpty(fmt.Sprintf("PR #%d", pr.Number), pr.State, ciText(pr.CI), pr.URL), " · "))
		}
		prs = strings.Join(parts, " | ")
	}
	return append(lines,
		fit(" "+strings.Join(facts, " · "), w),
		fit(" "+cardName(*s), w),
		styleDim.Render(fit(" "+prs, w)),
	)
}

func (m Model) boardIsEmpty() bool {
	for _, column := range m.board.Columns {
		if len(column) > 0 {
			return false
		}
	}
	return true
}

func ciText(ci string) string {
	if ci == "" || ci == "none" {
		return ""
	}
	return "CI " + ci
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// oneLine 은 줄바꿈을 공백으로 바꾼다. 카드·머리줄은 한 줄이다.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// fit 은 화면 폭을 넘는 부분을 자른다.
func fit(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// pad 는 보이는 폭이 w 가 되도록 오른쪽을 공백으로 채운다(넘치면 자른다).
func pad(s string, w int) string {
	s = fit(s, w)
	if gap := w - ansi.StringWidth(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func padLeft(s string, w int) string {
	s = fit(s, w)
	if gap := w - ansi.StringWidth(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}
