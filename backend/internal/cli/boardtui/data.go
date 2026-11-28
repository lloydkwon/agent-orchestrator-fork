// Package boardtui 는 `ao board` 의 터미널 화면이다. daemon 이 주는 프로젝트·세션
// 목록을 앱의 칸반과 같은 규칙으로 나눠 보여 줄 뿐, 세션을 바꾸지 않는다.
package boardtui

import (
	"sort"
	"strings"
	"time"
)

// 칸반 열. daemon 이 세션마다 정해 보내는 kanbanColumn 값과 같다.
const (
	ColumnBuilding    = "building"
	ColumnValidating  = "validating"
	ColumnNeedsReview = "needs_review"
	ColumnReady       = "ready"
	ColumnArchive     = "archive"
)

// Columns 는 보드에 그리는 열의 순서다(앱의 boardKanbanColumnOrder 와 같다).
var Columns = [4]string{ColumnBuilding, ColumnValidating, ColumnNeedsReview, ColumnReady}

// Project 는 GET /api/v1/projects 응답에서 화면에 쓰는 필드만 옮긴 것이다.
type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	FolderMissing bool   `json:"folderMissing"`
}

// Activity 는 세션의 에이전트 활동 상태다.
type Activity struct {
	State          string    `json:"state"`
	LastActivityAt time.Time `json:"lastActivityAt"`
}

// PR 은 세션에 묶인 pull request 요약이다.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
	CI     string `json:"ci"`
	Review string `json:"review"`
}

// Session 은 GET /api/v1/sessions 응답에서 화면에 쓰는 필드만 옮긴 것이다.
type Session struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"projectId"`
	DisplayName   string    `json:"displayName"`
	Kind          string    `json:"kind"`
	Harness       string    `json:"harness"`
	Model         string    `json:"model"`
	Mode          string    `json:"mode"`
	Status        string    `json:"status"`
	DisplayStatus string    `json:"displayStatus"`
	KanbanColumn  string    `json:"kanbanColumn"`
	IsTerminated  bool      `json:"isTerminated"`
	Activity      *Activity `json:"activity"`
	PRs           []PR      `json:"prs"`
	CreatedAt     time.Time `json:"createdAt"`
	LastEventAt   time.Time `json:"lastEventAt"`
}

// Snapshot 은 한 번 읽어 온 daemon 상태다.
type Snapshot struct {
	Projects []Project
	Sessions []Session
}

// Board 는 한 프로젝트의 칸반이다. Columns 의 순서는 패키지 변수 Columns 와 같다.
type Board struct {
	Columns [4][]Session
}

// ProjectSummary 는 프로젝트 목록 한 줄이다.
type ProjectSummary struct {
	Project      Project
	Counts       [4]int
	Attention    int
	Archived     int
	Orchestrator *Session
	LastActivity time.Time
	// Unlisted 는 프로젝트 목록에 없지만 살아 있는 세션이 남은 프로젝트다.
	Unlisted bool
}

// Live 는 보드에 오르는 세션 수다.
func (p ProjectSummary) Live() int {
	total := 0
	for _, n := range p.Counts {
		total += n
	}
	return total
}

// IsOrchestrator 는 앱의 isOrchestratorSession 과 같은 판정이다.
func IsOrchestrator(s Session) bool {
	return s.Kind == "orchestrator" || strings.HasSuffix(s.ID, "-orchestrator")
}

// IsArchived 는 앱의 isArchivedSession 과 같은 판정이다. 끝난 런타임은 SCM 결과가
// merged 여도 보관으로 본다.
func IsArchived(s Session) bool {
	return s.KanbanColumn == ColumnArchive || s.IsTerminated || s.Status == "terminated"
}

// NeedsAttention 은 사람이 봐야 하는 세션이다. 앱의 attentionZone 이 "action" 인
// 상태와, 에이전트가 입력·결정을 기다리는 활동 상태를 함께 센다.
func NeedsAttention(s Session) bool {
	switch s.Status {
	case "needs_input", "exited", "no_signal", "ci_failed", "changes_requested", "commented", "unknown":
		return true
	}
	if s.Activity != nil {
		switch s.Activity.State {
		case "waiting_input", "blocked":
			return true
		}
	}
	return false
}

// LastSeen 은 세션의 가장 최근 활동 시각이다.
func LastSeen(s Session) time.Time {
	latest := s.LastEventAt
	if s.Activity != nil && s.Activity.LastActivityAt.After(latest) {
		latest = s.Activity.LastActivityAt
	}
	return latest
}

// columnIndex 는 보드에 오른 세션이 놓일 열 번호다.
func columnIndex(s Session) int {
	// 앱 보드와 같이, 신호가 끊긴 세션은 «개발 중» 열에 둔다.
	if s.Status == "no_signal" || s.DisplayStatus == "No signal" {
		return 0
	}
	for i, column := range Columns {
		if s.KanbanColumn == column {
			return i
		}
	}
	return fallbackColumn(s.Status)
}

// fallbackColumn 은 kanbanColumn 이 없거나 이 판본이 모르는 값일 때의 열이다. 앱의
// toKanbanColumn 처럼 상태의 주의 구역으로 정한다(daemon 이 열을 보내기 전의 배치).
func fallbackColumn(status string) int {
	switch status {
	case "merged", "approved", "mergeable":
		return 3
	case "needs_input", "exited", "no_signal", "ci_failed", "changes_requested", "commented", "unknown":
		return 2
	case "review_pending", "pr_open", "draft":
		return 1
	default:
		return 0
	}
}

func onBoard(s Session) bool {
	return !IsOrchestrator(s) && !IsArchived(s)
}

// BuildBoard 는 projectID 의 살아 있는 워커 세션을 열로 나눈다. 열 안에서는
// 최근 활동이 앞에 온다.
func BuildBoard(snap Snapshot, projectID string) Board {
	var board Board
	for _, s := range snap.Sessions {
		if s.ProjectID != projectID || !onBoard(s) {
			continue
		}
		i := columnIndex(s)
		board.Columns[i] = append(board.Columns[i], s)
	}
	for i := range board.Columns {
		sortNewestFirst(board.Columns[i])
	}
	return board
}

func sortNewestFirst(sessions []Session) {
	sort.SliceStable(sessions, func(i, j int) bool {
		return LastSeen(sessions[i]).After(LastSeen(sessions[j]))
	})
}

// Summarize 는 프로젝트 목록 순서대로 한 줄씩 요약한다. 목록에 없는 프로젝트라도
// 살아 있는 세션이 있으면 끝에 덧붙인다.
func Summarize(snap Snapshot) []ProjectSummary {
	summaries := make([]ProjectSummary, 0, len(snap.Projects))
	index := make(map[string]int, len(snap.Projects))
	for _, p := range snap.Projects {
		index[p.ID] = len(summaries)
		summaries = append(summaries, ProjectSummary{Project: p})
	}
	// 목록에 없는 프로젝트의 행은 살아 있는 세션이 있을 때만 만든다. 행을 먼저 다
	// 만든 뒤 집계해야 그보다 앞에 온 보관 세션도 센다.
	for _, s := range snap.Sessions {
		if _, ok := index[s.ProjectID]; ok || IsArchived(s) || s.ProjectID == "" {
			continue
		}
		index[s.ProjectID] = len(summaries)
		summaries = append(summaries, ProjectSummary{Project: Project{ID: s.ProjectID, Name: s.ProjectID}, Unlisted: true})
	}
	for _, s := range snap.Sessions {
		if i, ok := index[s.ProjectID]; ok {
			summaries[i] = addToSummary(summaries[i], s)
		}
	}
	return summaries
}

func addToSummary(sum ProjectSummary, s Session) ProjectSummary {
	if seen := LastSeen(s); seen.After(sum.LastActivity) {
		sum.LastActivity = seen
	}
	if IsOrchestrator(s) {
		if !IsArchived(s) && (sum.Orchestrator == nil || s.CreatedAt.After(sum.Orchestrator.CreatedAt)) {
			orchestrator := s
			sum.Orchestrator = &orchestrator
		}
		return sum
	}
	if IsArchived(s) {
		sum.Archived++
		return sum
	}
	if NeedsAttention(s) {
		sum.Attention++
	}
	sum.Counts[columnIndex(s)]++
	return sum
}
