package ladophone

// What the phone shows about an approval. Go makes the lines from facts, in
// the words the desktop's confirmation uses (frontend/src/main/
// lado-presence.ts: approvePlan, integrationDetail, integrationsDetail,
// headLines, nextLines, confirmLine, protectionPart; the Korean of
// renderer/i18n/ko.json lado.presence.*). Text an agent or a host wrote
// (titles, branches, paths, commit subjects, errors) is cleaned as the
// desktop's UNSAFE_TEXT cleans it, so direction and control characters
// cannot fake a line; the plan itself keeps its line breaks and is shown
// apart as the worker's text. The phone puts all of it in with textContent.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

const (
	// maxExamples values a line quotes, and maxExampleChars runes each.
	maxExamples     = 3
	maxExampleChars = 120
	// maxDBFilesShown SQL files a head lists.
	maxDBFilesShown = 20
)

// unsafeText is the desktop's UNSAFE_TEXT: control characters, and the
// marks, isolates and overrides that change the direction of text.
var unsafeText = regexp.MustCompile(`[\x00-\x1f\x7f-\x9f\x{061c}\x{200e}\x{200f}\x{2028}\x{2029}\x{202a}-\x{202e}\x{2066}-\x{2069}]+`)

// summary is an approval's title, lines (an empty line parts blocks), what
// happens next, and the worker's text kept apart.
type summary struct {
	Title     string
	Lines     []string
	Next      string
	AgentText string
}

// jsSpace is a space as JavaScript's \s and trim() see it.
func jsSpace(r rune) bool {
	return unicode.IsSpace(r) || r == '\ufeff'
}

func validUTF8(s string) string {
	return strings.ToValidUTF8(s, "\ufffd")
}

// oneLine is s with each run of unsafe characters made one space, trimmed.
func oneLine(s string) string {
	return strings.TrimFunc(unsafeText.ReplaceAllString(validUTF8(s), " "), jsSpace)
}

// excerpt is s as one line of at most limit runes: unsafe characters and
// runs of spaces become one space; a longer text ends in "…".
func excerpt(s string, limit int) string {
	flat := strings.Join(strings.FieldsFunc(unsafeText.ReplaceAllString(validUTF8(s), " "), jsSpace), " ")
	runes := []rune(flat)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return flat
}

// agentBlock is text an agent wrote that keeps its lines: line breaks and
// tabs stay, any other unsafe character becomes a space.
func agentBlock(s string) string {
	s = strings.ReplaceAll(validUTF8(s), "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unsafeText.MatchString(string(r)) {
			return ' '
		}
		return r
	}, s)
}

// examples is up to maxExamples values as one line; ", …" says more exist.
func examples(values []string, count int) string {
	shown := make([]string, 0, maxExamples)
	for _, v := range values[:min(len(values), maxExamples)] {
		shown = append(shown, excerpt(v, maxExampleChars))
	}
	line := strings.Join(shown, ", ")
	if count > len(shown) {
		line += ", …"
	}
	return line
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// orDash is s, or "—" when it is empty.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ceilingWords are the permission modes as the desktop names them.
var ceilingWords = map[domain.PermissionMode]string{
	domain.PermissionModeAcceptEdits:       "편집 수락",
	domain.PermissionModeAuto:              "자동",
	domain.PermissionModeBypassPermissions: "권한 우회",
}

func ceilingWord(mode domain.PermissionMode) string {
	if word, ok := ceilingWords[mode]; ok {
		return word
	}
	return orDash(excerpt(string(mode), maxExampleChars))
}

// planSummary is the desktop's plan approval confirmation.
func planSummary(view ladotask.PlanView, rules ruleFacts) summary {
	rev := view.Revision
	lines := []string{
		oneLine(view.Task.Issue.Title),
		fmt.Sprintf("%s, 리비전 %d", oneLine(string(view.Task.IssueRef)), rev.Number),
		"sha256 " + short(rev.SHA256, 12),
		"",
		"LADO는 승인한 계획으로만 작업을 시작합니다.",
		"",
		fmt.Sprintf("규칙: %d개 켜짐, 권한 상한 %s", rules.on, ceilingWord(rules.ceiling)),
		"규칙 sha256 " + short(rules.sha256, 12),
	}
	if rules.blocking > 0 {
		lines = append(lines, fmt.Sprintf("차단 문제 %d개: 해결할 때까지 작업은 차단됨 상태입니다.", rules.blocking))
	}
	return summary{Title: "이 계획을 승인할까요?", Lines: lines, AgentText: agentBlock(view.Content)}
}

// integrationSummary is the desktop's integration approval confirmation:
// the task, each PR's head, how many items passed, its SQL files and one
// line for each thing to confirm, then what happens next. results are the
// dry run of prs, matched by URL.
func integrationSummary(view ladoqueue.ExecutionView, prs []domain.LadoExecutionTrackedPR, results []ladoverify.ApprovePRResult) summary {
	task := "작업: " + oneLine(view.Task.Issue.Title)
	records := make([]domain.LadoVerification, 0, len(results))
	for _, r := range results {
		records = append(records, r.Verification)
	}
	if len(prs) == 1 {
		pr := prs[0].PR
		sum := summary{Title: fmt.Sprintf("통합을 승인할까요? PR #%d → %s", pr.Number, orDash(oneLine(pr.TargetBranch))), Lines: []string{task}}
		if len(records) > 0 {
			sum.Lines = append(sum.Lines, headLines(records[0])...)
		}
		sum.Next = nextLine(records, false)
		return sum
	}
	sum := summary{Title: fmt.Sprintf("이 작업의 PR %d개 통합을 승인할까요?", len(prs)), Lines: []string{task}}
	for _, pr := range prs {
		sum.Lines = append(sum.Lines, "", fmt.Sprintf("%s의 PR #%d → %s",
			orDash(excerpt(pr.Repo, maxExampleChars)), pr.PR.Number, orDash(excerpt(pr.PR.TargetBranch, maxExampleChars))))
		if at := slices.IndexFunc(results, func(r ladoverify.ApprovePRResult) bool { return r.PRURL == pr.PR.URL }); at >= 0 {
			sum.Lines = append(sum.Lines, headLines(results[at].Verification)...)
		}
		if pr.Integration.HeadApproved {
			sum.Lines = append(sum.Lines, "이 head는 이미 승인되어 LADO가 그대로 둡니다.")
		}
	}
	sum.Next = nextLine(records, true)
	return sum
}

// headLines are a head's lines: the head, how many of its items passed, the
// SQL files it names and one line for each item to confirm.
func headLines(v domain.LadoVerification) []string {
	passed, total := 0, 0
	for _, item := range v.Items {
		if item.Outcome == domain.LadoVerifyInfo {
			continue
		}
		total++
		if item.Outcome == domain.LadoVerifyPass {
			passed++
		}
	}
	lines := []string{"head: " + short(strings.ToLower(v.HeadSHA), 12), fmt.Sprintf("검증 항목 %d개 중 %d개 통과", total, passed)}
	lines = append(lines, dbFileLines(v)...)
	for _, item := range v.Items {
		if item.Outcome == domain.LadoVerifyConfirm {
			lines = append(lines, confirmLines(item, v.ProtectionError)...)
		}
	}
	return lines
}

// dbFiles are the SQL files of a head's db_changes item, in its order.
func dbFiles(v domain.LadoVerification) []domain.LadoDBChangeFileRef {
	d, ok, err := v.DBChanges()
	if !ok || err != nil {
		return nil
	}
	return d.Files
}

func dbFileLines(v domain.LadoVerification) []string {
	files := dbFiles(v)
	var lines []string
	for _, f := range files[:min(len(files), maxDBFilesShown)] {
		phase := "병합 전 SQL: "
		if f.Phase == domain.LadoDBChangeAfterDeploy {
			phase = "배포 뒤 SQL: "
		}
		lines = append(lines, phase+excerpt(f.Path, maxExampleChars))
	}
	if len(files) > maxDBFilesShown {
		lines = append(lines, fmt.Sprintf("… 외 SQL 파일 %d개", len(files)-maxDBFilesShown))
	}
	return lines
}

// nextLine says what happens once approved. LADO posts its status unless
// every head's rules are GitLab's unread kind; pre-merge SQL makes LADO
// apply it and merge itself; after-deploy SQL waits for the desktop; a
// branch without protection lets a merge skip the approval.
func nextLine(records []domain.LadoVerification, many bool) string {
	unprotected, posts, preMerge, afterDeploy := false, false, false, false
	for _, v := range records {
		code := ""
		if at := slices.IndexFunc(v.Items, func(item domain.LadoVerifyItem) bool { return item.Kind == domain.LadoVerifyProtection }); at >= 0 {
			code = v.Items[at].Code
		}
		unprotected = unprotected || code == domain.LadoCodeUnprotected || code == domain.LadoCodeUnprotectedGitLab
		posts = posts || code != domain.LadoCodeUnprotectedGitLab
		for _, f := range dbFiles(v) {
			preMerge = preMerge || f.Phase == domain.LadoDBChangePreMerge
			afterDeploy = afterDeploy || f.Phase == domain.LadoDBChangeAfterDeploy
		}
	}
	pick := func(single, several string) string {
		if many {
			return several
		}
		return single
	}
	var next []string
	switch {
	case preMerge:
		next = append(next, pick(
			"승인하면 LADO가 PR을 병합할 수 있는지 확인하고, 병합 전 SQL을 경로 순서로 적용한 뒤 lado/integration을 게시하고 직접 병합합니다. 파일 하나라도 실패하면 병합하지 않습니다.",
			"승인하면 LADO가 각 PR을 병합할 수 있는지 확인하고, 병합 전 SQL을 경로 순서로 적용한 뒤 lado/integration을 게시하고 적힌 순서대로 직접 병합합니다. 파일 하나라도 실패하면 병합하지 않습니다."))
	case posts:
		next = append(next, pick(
			"승인하면 LADO가 이 head에 lado/integration 상태를 게시합니다. LADO는 직접 병합하지 않습니다.",
			"승인하면 LADO가 각 head에 lado/integration 상태를 게시합니다. LADO는 직접 병합하지 않습니다. 적힌 순서대로 병합하세요."))
	}
	if afterDeploy {
		next = append(next, "배포 뒤 SQL은 병합 뒤 앱에서 적용할 때까지 기다립니다.")
	}
	if unprotected {
		next = append(next, pick(
			"이 저장소는 보호 안 됨 상태입니다. LADO는 승인 없는 병합을 감지만 합니다.",
			"이 저장소 중 일부는 보호 안 됨 상태입니다. 그곳에서는 LADO가 승인 없는 병합을 감지만 합니다."))
	}
	return strings.Join(next, " ")
}

// confirmLines words one item to confirm, with how many there are and up
// to three examples. A code from a newer daemon is named as it is.
func confirmLines(item domain.LadoVerifyItem, protectionError string) []string {
	if line, ok := confirmLineFor[item.Code]; ok {
		return line(item, protectionError)
	}
	return []string{"그 밖에 확인할 것: " + excerpt(item.Code, maxExampleChars)}
}

// details decodes an item's details into the kind's struct; details it
// cannot read leave the zero value, so the line still names the code.
func details[T any](item domain.LadoVerifyItem) T {
	var d T
	if len(item.Details) > 0 {
		_ = json.Unmarshal(item.Details, &d)
	}
	return d
}

func lineOf(line string) []string { return []string{line} }

// confirmLineFor words each code that needs confirming
// (TestSummaryCoversEveryConfirmCode keeps it complete).
var confirmLineFor = map[string]func(item domain.LadoVerifyItem, protectionError string) []string{
	domain.LadoCodeOutsidePlan: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoPathsDetails](item)
		count := d.OutsideCount
		if count == 0 {
			count = len(d.OutsidePlan)
		}
		return lineOf(fmt.Sprintf("계획 밖 변경 경로 (%d): %s", count, examples(d.OutsidePlan, count)))
	},
	domain.LadoCodeCommitMessagesUnclear: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoCommitMessagesDetails](item)
		var commits []string
		for _, c := range d.Commits {
			if c.Result == "unclear" || c.Result == "body_unclear" {
				commits = append(commits, short(c.SHA, 7)+" "+c.Subject)
			}
		}
		if d.PRTitle.Result == "unclear" {
			commits = append(commits, "PR 제목: "+d.PRTitle.Text)
		}
		return lineOf(fmt.Sprintf("LADO가 판단하지 못한 커밋 메시지 (%d): %s", len(commits), examples(commits, len(commits))))
	},
	domain.LadoCodeBaseMoved: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoBaseBranchDetails](item)
		moved := make([]string, 0, len(d.Moved))
		for _, m := range d.Moved {
			moved = append(moved, short(m.Commit, 7)+" "+m.Subject)
		}
		return lineOf(fmt.Sprintf("원격에 없는 로컬 기준 브랜치 이동 (%d): %s", len(moved), examples(moved, len(moved))))
	},
	domain.LadoCodeReflogOff: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoBaseBranchDetails](item)
		return lineOf("로컬 기준 브랜치에 reflog가 없어 LADO가 이동 여부를 알 수 없습니다: " + excerpt(d.Branch, maxExampleChars))
	},
	domain.LadoCodeAlreadyOnBase: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoBaseBranchDetails](item)
		return lineOf("이 head는 이미 원격 기준 브랜치(" + excerpt(d.Branch, maxExampleChars) + ")에 있습니다. LADO를 거치지 않고 들어간 작업입니다.")
	},
	domain.LadoCodeBaseDeleted: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoBaseBranchDetails](item)
		return lineOf(fmt.Sprintf("실행 시작 때 있던 로컬 브랜치가 지금은 없습니다: %s (그때 커밋 %s)",
			excerpt(d.Branch, maxExampleChars), short(excerpt(d.DispatchCommit, maxExampleChars), 7)))
	},
	domain.LadoCodeBaseAdvanced: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoBaseBranchDetails](item)
		return lineOf(fmt.Sprintf("이 head가 갈라진 뒤 원격 %s가 %s로 움직였습니다. 겹치는 파일은 없습니다.",
			excerpt(d.Branch, maxExampleChars), short(excerpt(d.AdvancedTip, maxExampleChars), 7)))
	},
	domain.LadoCodeBaseNotDefault: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoBaseBranchDetails](item)
		branch := excerpt(d.Branch, maxExampleChars)
		dispatch := excerpt(d.DispatchBranch, maxExampleChars)
		if dispatch == "" {
			dispatch = branch
		}
		return lineOf(fmt.Sprintf("PR 대상은 %s, 실행 기준은 %s이지만 PR 저장소의 기본 브랜치는 %s입니다.", branch, dispatch, excerpt(d.DefaultBranch, maxExampleChars)))
	},
	domain.LadoCodeUnprotected: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoProtectionDetails](item)
		parts := make([]string, 0, len(d.Missing))
		for _, part := range d.Missing {
			parts = append(parts, protectionPart(part))
		}
		lines := lineOf(fmt.Sprintf("기준 브랜치 규칙에 없는 것 (%d): %s", len(parts), examples(parts, len(parts))))
		if slices.Contains(d.Missing, ladoverify.MissingIntegrationCheckSource) {
			lines = append(lines, "규칙이 lado/integration을 한 앱에서만 받아 LADO의 상태로는 채울 수 없습니다. 이 검사의 출처를 “Any source”(모든 출처)로 바꾸세요.")
		}
		return lines
	},
	domain.LadoCodeProtectionUnknown: func(item domain.LadoVerifyItem, protectionError string) []string {
		reason := protectionError
		if reason == "" {
			reason = details[domain.LadoProtectionDetails](item).ErrorClass
		}
		return lineOf("LADO가 기준 브랜치 규칙을 읽지 못했습니다: " + excerpt(reason, maxExampleChars))
	},
	domain.LadoCodeUnprotectedGitLab: func(domain.LadoVerifyItem, string) []string {
		return lineOf("LADO는 GitLab의 브랜치 보호를 읽지 않습니다.")
	},
	domain.LadoCodeReviewInconclusive: func(item domain.LadoVerifyItem, _ string) []string {
		return lineOf(fmt.Sprintf("리뷰 결론 없음 (%d회)", details[domain.LadoReviewDetails](item).Rounds))
	},
	domain.LadoCodeNoTests: func(domain.LadoVerifyItem, string) []string {
		return lineOf("이 head를 시험한 검사 명령도 필수 검사도 없습니다.")
	},
	domain.LadoCodeDBChangeAppliedModified: func(item domain.LadoVerifyItem, _ string) []string {
		d := details[domain.LadoDBChangesDetails](item)
		paths := make([]string, 0, len(d.Modified))
		for _, m := range d.Modified {
			paths = append(paths, m.Path)
		}
		return lineOf(fmt.Sprintf("LADO가 이미 적용한 SQL 파일을 바꾸거나 지움 (%d). LADO는 다시 실행하지 않습니다: %s", len(paths), examples(paths, len(paths))))
	},
	domain.LadoCodeDBChangeUnplanned: func(domain.LadoVerifyItem, string) []string {
		return lineOf("SQL 파일을 더하지만 계획의 change_kinds에 db_schema가 없습니다.")
	},
}

// protectionParts word what a base branch's rules lack, as the desktop
// names it (lado.protection.part.*).
var protectionParts = map[string]string{
	"pull_request":                            "PR 필수",
	ladoverify.MissingIntegrationCheck:        "lado/integration 필수",
	ladoverify.MissingIntegrationCheckSource:  "모든 출처의 lado/integration",
	"non_fast_forward":                        "강제 푸시 차단",
	"deletion":                                "삭제 차단",
	domain.LadoGitLabMissingProtectedBranch:   "보호 브랜치",
	domain.LadoGitLabMissingPushAccess:        "푸시 허용 없음(No one)",
	domain.LadoGitLabMissingForcePush:         "강제 푸시 차단",
	domain.LadoGitLabMissingPipelineMustPass:  "파이프라인 성공 필수",
	domain.LadoGitLabMissingSkippedPipeline:   "건너뛴 파이프라인을 성공으로 보지 않음",
	domain.LadoGitLabMissingIntegrationStatus: "LADO의 lado/integration 상태 게시",
}

// protectionPart words one part; a part from a newer daemon reads as the
// daemon names it.
func protectionPart(part string) string {
	if word, ok := protectionParts[part]; ok {
		return word
	}
	return part
}
