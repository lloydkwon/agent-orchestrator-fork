package boardtui

import (
	"fmt"
	"strings"
	"time"
)

// Labels 는 화면 문구다. 앱 i18n(ko.json·en.json)의 같은 개념 이름을 따른다.
type Labels struct {
	Title          string
	Back           string
	Project        string
	Columns        [4]string
	ShortColumns   [4]string
	Attention      string
	ShortAttention string
	Orchestrator   string
	LastActivity   string
	Archived       string
	None           string
	Live           string
	Polling        string
	Loading        string
	LoadFailed     string
	Unlisted       string
	FolderMissing  string
	NoProjects     string
	NoSessions     string
	EmptyColumn    string
	NoPR           string
	HelpProjects   string
	HelpBoard      string
	Status         map[string]string
	agoNow         string
	agoUnits       [3]string
	agoSuffix      string
}

// LabelsFor 는 locale(LC_ALL·LANG 값)이 ko 로 시작하면 한국어, 아니면 영어 문구를 준다.
func LabelsFor(locale string) Labels {
	if strings.HasPrefix(strings.ToLower(locale), "ko") {
		return koreanLabels()
	}
	return englishLabels()
}

func koreanLabels() Labels {
	return Labels{
		Title:          "LADO 보드",
		Back:           "← 프로젝트",
		Project:        "프로젝트",
		Columns:        [4]string{"개발 중", "검증 중", "리뷰 중", "준비 완료"},
		ShortColumns:   [4]string{"개발", "검증", "리뷰", "준비"},
		Attention:      "사람 대기",
		ShortAttention: "대기",
		Orchestrator:   "오케스트레이터",
		LastActivity:   "최근 활동",
		Archived:       "보관",
		None:           "없음",
		Live:           "● 실시간",
		Polling:        "○ 연결 끊김 — 5초마다 다시 읽음",
		Loading:        "불러오는 중…",
		LoadFailed:     "읽기 실패",
		Unlisted:       "목록에 없음",
		FolderMissing:  "폴더 없음",
		NoProjects:     "등록된 프로젝트가 없습니다.",
		NoSessions:     "이 프로젝트에 살아 있는 세션이 없습니다.",
		EmptyColumn:    "비어 있음",
		NoPR:           "PR 없음",
		HelpProjects:   "↑↓ 이동 · Enter·클릭 열기 · r 새로고침 · q 끝내기",
		HelpBoard:      "←→ 열 · ↑↓ 카드 · 클릭 선택 · Esc 뒤로 · r 새로고침 · q 끝내기",
		Status:         koreanStatus,
		agoNow:         "방금",
		agoUnits:       [3]string{"분", "시간", "일"},
		agoSuffix:      " 전",
	}
}

func englishLabels() Labels {
	return Labels{
		Title:          "LADO board",
		Back:           "← Projects",
		Project:        "Project",
		Columns:        [4]string{"Building", "Validating", "In review", "Ready"},
		ShortColumns:   [4]string{"Bld", "Val", "Rev", "Rdy"},
		Attention:      "Attention",
		ShortAttention: "Attn",
		Orchestrator:   "Orchestrator",
		LastActivity:   "Last activity",
		Archived:       "archived",
		None:           "none",
		Live:           "● live",
		Polling:        "○ disconnected — polling every 5s",
		Loading:        "Loading…",
		LoadFailed:     "load failed",
		Unlisted:       "not in project list",
		FolderMissing:  "folder missing",
		NoProjects:     "No projects registered.",
		NoSessions:     "No live sessions in this project.",
		EmptyColumn:    "empty",
		NoPR:           "no PR",
		HelpProjects:   "↑↓ move · Enter/click open · r refresh · q quit",
		HelpBoard:      "←→ column · ↑↓ card · click select · Esc back · r refresh · q quit",
		Status:         englishStatus,
		agoNow:         "just now",
		agoUnits:       [3]string{"m", "h", "d"},
		agoSuffix:      " ago",
	}
}

// StatusLabel 은 상태 코드의 이름이다. 모르는 코드는 daemon 의 displayStatus, 그것도
// 없으면 코드를 그대로 쓴다.
func (l Labels) StatusLabel(s Session) string {
	if label, ok := l.Status[s.Status]; ok {
		return label
	}
	if s.DisplayStatus != "" {
		return s.DisplayStatus
	}
	return s.Status
}

// Ago 는 then 에서 now 까지 지난 시간을 짧게 쓴다. then 이 비었으면 "-" 다.
func (l Labels) Ago(now, then time.Time) string {
	if then.IsZero() {
		return "-"
	}
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return l.agoNow
	case d < time.Hour:
		return fmt.Sprintf("%d%s%s", int(d/time.Minute), l.agoUnits[0], l.agoSuffix)
	case d < 48*time.Hour:
		return fmt.Sprintf("%d%s%s", int(d/time.Hour), l.agoUnits[1], l.agoSuffix)
	default:
		return fmt.Sprintf("%d%s%s", int(d/(24*time.Hour)), l.agoUnits[2], l.agoSuffix)
	}
}

// 상태 이름. 앱 i18n 의 status.* 와 같다.
var koreanStatus = map[string]string{
	"working":           "작업 중",
	"idle":              "유휴",
	"needs_input":       "입력 필요",
	"exited":            "종료됨",
	"no_signal":         "신호 없음",
	"ci_failed":         "CI 실패",
	"changes_requested": "변경 요청됨",
	"commented":         "댓글 남김",
	"review_pending":    "리뷰 대기",
	"draft":             "초안 PR",
	"pr_open":           "PR 열림",
	"approved":          "승인됨",
	"mergeable":         "준비 완료",
	"merged":            "병합됨",
	"terminated":        "종료됨",
	"unknown":           "알 수 없는 상태",
}

var englishStatus = map[string]string{
	"working":           "Working",
	"idle":              "Idle",
	"needs_input":       "Input needed",
	"exited":            "Exited",
	"no_signal":         "No signal",
	"ci_failed":         "CI failed",
	"changes_requested": "Changes requested",
	"commented":         "Commented",
	"review_pending":    "Review pending",
	"draft":             "Draft PR",
	"pr_open":           "PR open",
	"approved":          "Approved",
	"mergeable":         "Ready",
	"merged":            "Merged",
	"terminated":        "Terminated",
	"unknown":           "Unknown status",
}
