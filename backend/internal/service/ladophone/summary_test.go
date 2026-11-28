package ladophone

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

// domainStringConsts returns the string constants of a domain source file
// by name, and the values whose line comment starts with prefix.
func domainStringConsts(t *testing.T, file, prefix string) (map[string]string, map[string]bool) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "domain", file), nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	values, marked := map[string]string{}, map[string]bool{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				values[name.Name] = value
				if vs.Comment != nil && strings.HasPrefix(strings.TrimSpace(vs.Comment.Text()), prefix) {
					marked[value] = true
				}
			}
		}
	}
	return values, marked
}

// confirmCodes are every verification code that needs confirming: those
// lado_verify.go marks "// confirm", and every code a non-test source file
// of the backend puts in an item with the confirm outcome.
func confirmCodes(t *testing.T) map[string]bool {
	t.Helper()
	values, codes := domainStringConsts(t, "lado_verify.go", "confirm")
	if len(codes) < 15 {
		t.Fatalf("found %d confirm codes in lado_verify.go, want at least the 15 of #97", len(codes))
	}
	used := regexp.MustCompile(`LadoVerifyConfirm,\s*(?:domain\.)?(LadoCode\w+)`)
	scanned := 0
	err := filepath.WalkDir(filepath.Join("..", ".."), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, m := range used.FindAllStringSubmatch(string(data), -1) {
			value, ok := values[m[1]]
			if !ok {
				t.Errorf("%s puts %s in a confirm item; it is not a code of lado_verify.go", path, m[1])
				continue
			}
			codes[value] = true
		}
		return nil
	})
	if err != nil || scanned < 100 {
		t.Fatalf("scanned %d source files (%v)", scanned, err)
	}
	return codes
}

func TestSummaryCoversEveryConfirmCode(t *testing.T) {
	want := confirmCodes(t)
	var handled []string
	for code := range confirmLineFor {
		handled = append(handled, code)
	}
	for code := range want {
		if !slices.Contains(handled, code) {
			t.Errorf("confirm code %q has no line of its own on the phone", code)
		}
	}
	for _, code := range handled {
		if !want[code] {
			t.Errorf("the phone words %q, which is no confirm code", code)
		}
	}
	for code := range want {
		lines := confirmLines(domain.LadoVerifyItem{Outcome: domain.LadoVerifyConfirm, Code: code, Details: []byte(`{}`)}, "")
		if len(lines) == 0 || strings.HasPrefix(lines[0], "그 밖에 확인할 것") || strings.Contains(lines[0], "{{") {
			t.Errorf("%s with no details = %q", code, lines)
		}
	}
	// A code from a newer daemon still names what is confirmed.
	if got := confirmLines(domain.LadoVerifyItem{Outcome: domain.LadoVerifyConfirm, Code: "brand_new\u202e"}, ""); !slices.Equal(got, []string{"그 밖에 확인할 것: brand_new"}) {
		t.Errorf("unknown code = %q", got)
	}

	// Each part a base branch's rules can lack has its words.
	gitlab, _ := domainStringConsts(t, "lado_verify.go", "")
	parts := []string{ports.LadoBranchRulePullRequest, ladoverify.MissingIntegrationCheck, ladoverify.MissingIntegrationCheckSource,
		ports.LadoBranchRuleNonFastForward, ports.LadoBranchRuleDeletion}
	for name, value := range gitlab {
		if strings.HasPrefix(name, "LadoGitLabMissing") {
			parts = append(parts, value)
		}
	}
	if len(parts) != 11 {
		t.Fatalf("protection parts = %v", parts)
	}
	for _, part := range parts {
		if got := protectionPart(part); got == part || got == "" {
			t.Errorf("protection part %q reads %q", part, got)
		}
	}
}

func TestConfirmLinesAsTheDesktopWordsThem(t *testing.T) {
	item := func(kind domain.LadoVerifyKind, code string, details any) domain.LadoVerifyItem {
		return mustItem(t, kind, domain.LadoVerifyConfirm, code, details)
	}
	for _, c := range []struct {
		item    domain.LadoVerifyItem
		protErr string
		want    []string
	}{
		{item(domain.LadoVerifyPaths, domain.LadoCodeOutsidePlan, domain.LadoPathsDetails{OutsidePlan: []string{"a.go", "b.go", "c.go", "d.go"}, OutsideCount: 9}), "",
			[]string{"계획 밖 변경 경로 (9): a.go, b.go, c.go, …"}},
		{item(domain.LadoVerifyCommitMessages, domain.LadoCodeCommitMessagesUnclear, domain.LadoCommitMessagesDetails{
			Commits: []domain.LadoVerifyCommit{{SHA: "0123456789ab", Subject: "wip", Result: "unclear"}, {SHA: "fedcba987654", Subject: "ok", Result: "ok"}},
			PRTitle: domain.LadoVerifyText{Text: "stuff", Result: "unclear"}}), "",
			[]string{"LADO가 판단하지 못한 커밋 메시지 (2): 0123456 wip, PR 제목: stuff"}},
		{item(domain.LadoVerifyBaseBranch, domain.LadoCodeBaseMoved, domain.LadoBaseBranchDetails{Branch: "main", Moved: []domain.LadoReflogEntry{{Commit: "abcdef123456", Subject: "local"}}}), "",
			[]string{"원격에 없는 로컬 기준 브랜치 이동 (1): abcdef1 local"}},
		{item(domain.LadoVerifyBaseBranch, domain.LadoCodeReflogOff, domain.LadoBaseBranchDetails{Branch: "main"}), "",
			[]string{"로컬 기준 브랜치에 reflog가 없어 LADO가 이동 여부를 알 수 없습니다: main"}},
		{item(domain.LadoVerifyBaseBranch, domain.LadoCodeAlreadyOnBase, domain.LadoBaseBranchDetails{Branch: "main"}), "",
			[]string{"이 head는 이미 원격 기준 브랜치(main)에 있습니다. LADO를 거치지 않고 들어간 작업입니다."}},
		{item(domain.LadoVerifyBaseBranch, domain.LadoCodeBaseDeleted, domain.LadoBaseBranchDetails{Branch: "dev", DispatchCommit: "1234567890"}), "",
			[]string{"실행 시작 때 있던 로컬 브랜치가 지금은 없습니다: dev (그때 커밋 1234567)"}},
		{item(domain.LadoVerifyBaseBranch, domain.LadoCodeBaseAdvanced, domain.LadoBaseBranchDetails{Branch: "main", AdvancedTip: "9876543210"}), "",
			[]string{"이 head가 갈라진 뒤 원격 main가 9876543로 움직였습니다. 겹치는 파일은 없습니다."}},
		{item(domain.LadoVerifyBaseBranch, domain.LadoCodeBaseNotDefault, domain.LadoBaseBranchDetails{Branch: "release", DefaultBranch: "main"}), "",
			[]string{"PR 대상은 release, 실행 기준은 release이지만 PR 저장소의 기본 브랜치는 main입니다."}},
		{item(domain.LadoVerifyProtection, domain.LadoCodeUnprotected, domain.LadoProtectionDetails{Missing: []string{"pull_request", ladoverify.MissingIntegrationCheckSource}}), "",
			[]string{"기준 브랜치 규칙에 없는 것 (2): PR 필수, 모든 출처의 lado/integration",
				"규칙이 lado/integration을 한 앱에서만 받아 LADO의 상태로는 채울 수 없습니다. 이 검사의 출처를 “Any source”(모든 출처)로 바꾸세요."}},
		{item(domain.LadoVerifyProtection, domain.LadoCodeProtectionUnknown, domain.LadoProtectionDetails{ErrorClass: "http_404"}), "the host answered 404",
			[]string{"LADO가 기준 브랜치 규칙을 읽지 못했습니다: the host answered 404"}},
		{item(domain.LadoVerifyProtection, domain.LadoCodeProtectionUnknown, domain.LadoProtectionDetails{ErrorClass: "rate_limited"}), "",
			[]string{"LADO가 기준 브랜치 규칙을 읽지 못했습니다: rate_limited"}},
		{item(domain.LadoVerifyProtection, domain.LadoCodeUnprotectedGitLab, nil), "", []string{"LADO는 GitLab의 브랜치 보호를 읽지 않습니다."}},
		{item(domain.LadoVerifyReview, domain.LadoCodeReviewInconclusive, domain.LadoReviewDetails{Rounds: 3}), "", []string{"리뷰 결론 없음 (3회)"}},
		{item(domain.LadoVerifyCommands, domain.LadoCodeNoTests, nil), "", []string{"이 head를 시험한 검사 명령도 필수 검사도 없습니다."}},
		{item(domain.LadoVerifyDBChanges, domain.LadoCodeDBChangeAppliedModified, domain.LadoDBChangesDetails{Modified: []domain.LadoDBChangeModified{{Path: "db/1.sql"}}}), "",
			[]string{"LADO가 이미 적용한 SQL 파일을 바꾸거나 지움 (1). LADO는 다시 실행하지 않습니다: db/1.sql"}},
		{item(domain.LadoVerifyDBChanges, domain.LadoCodeDBChangeUnplanned, nil), "", []string{"SQL 파일을 더하지만 계획의 change_kinds에 db_schema가 없습니다."}},
	} {
		if got := confirmLines(c.item, c.protErr); !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.item.Code, got, c.want)
		}
	}
}

// unsafeRune is what the desktop's UNSAFE_TEXT matches.
func unsafeRune(r rune) bool {
	return r <= 0x1f || (r >= 0x7f && r <= 0x9f) || r == 0x061c || r == 0x200e || r == 0x200f || r == 0x2028 || r == 0x2029 ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

func TestSummaryStripsBidiControl(t *testing.T) {
	const evil = "fix\u202elogin\u2066 \u200fthing\u061c\x1b[31m\nnext\u2028line\u0085x\x00y\xff"
	clean := func(what, s string) {
		t.Helper()
		for _, r := range s {
			if unsafeRune(r) {
				t.Errorf("%s keeps %U in %q", what, r, s)
			}
		}
	}
	if got := oneLine(evil); got != "fix login   thing [31m next line x y\ufffd" {
		t.Errorf("oneLine = %q", got)
	}
	if got := excerpt(evil, 12); got != "fix login th…" {
		t.Errorf("excerpt = %q", got)
	}

	rev := domain.LadoPlanRevision{ID: "rev-2", Number: 2, SHA256: testPlanSHA}
	view := ladotask.PlanView{
		Task:     domain.LadoTask{IssueRef: "acme/app#1\u202e", Issue: domain.LadoObservedIssue{Title: evil}},
		Revision: rev, Latest: true,
		Content: "# Plan\u202e\r\nline two\tok\u2066\n\x07end",
	}
	sum := planSummary(view, ruleFacts{sha256: testPolicySHA, on: 1, ceiling: domain.PermissionModeAuto})
	clean("plan title", sum.Title)
	for _, line := range sum.Lines {
		clean("plan line", line)
	}
	// The plan keeps its lines and tabs, nothing else of the kind.
	if sum.AgentText != "# Plan \nline two\tok \n end" {
		t.Errorf("plan text = %q", sum.AgentText)
	}

	exec := domain.LadoExecution{SessionID: "s", PR: domain.LadoExecutionPR{URL: testPRURL, Number: 7, HeadSHA: testHead, TargetBranch: "ma\u202ein"}}
	prs := []domain.LadoExecutionTrackedPR{{PR: exec.PR, Repo: "r\u2067epo"}}
	items := []domain.LadoVerifyItem{
		mustItem(t, domain.LadoVerifyPaths, domain.LadoVerifyConfirm, domain.LadoCodeOutsidePlan, domain.LadoPathsDetails{OutsidePlan: []string{"a\u202e.go", "b\n.go"}}),
		mustItem(t, domain.LadoVerifyCommitMessages, domain.LadoVerifyConfirm, domain.LadoCodeCommitMessagesUnclear, domain.LadoCommitMessagesDetails{
			Commits: []domain.LadoVerifyCommit{{SHA: "0123456789", Subject: "do\u2066 it\r\n", Result: "body_unclear"}}}),
	}
	isum := integrationSummary(ladoqueue.ExecutionView{Execution: exec, Task: domain.LadoTask{Issue: domain.LadoObservedIssue{Title: evil}}}, prs,
		[]ladoverify.ApprovePRResult{verifiedRecord(t, "s", testPRURL, testHead, items...)})
	clean("integration title", isum.Title)
	clean("integration next", isum.Next)
	for _, line := range isum.Lines {
		clean("integration line", line)
	}
}

func TestPlanSummaryLines(t *testing.T) {
	rev := domain.LadoPlanRevision{ID: "rev-2", Number: 2, SHA256: testPlanSHA}
	view := ladotask.PlanView{Task: domain.LadoTask{IssueRef: "acme/app#12", Issue: domain.LadoObservedIssue{Title: "로그인"}}, Revision: rev, Content: "# 계획\n"}
	sum := planSummary(view, ruleFacts{sha256: testPolicySHA, on: 2, ceiling: domain.PermissionModeAcceptEdits, blocking: 1})
	want := []string{
		"로그인", "acme/app#12, 리비전 2", "sha256 aaaaaaaaaaaa", "", "LADO는 승인한 계획으로만 작업을 시작합니다.", "",
		"규칙: 2개 켜짐, 권한 상한 편집 수락", "규칙 sha256 bbbbbbbbbbbb", "차단 문제 1개: 해결할 때까지 작업은 차단됨 상태입니다.",
	}
	if sum.Title != "이 계획을 승인할까요?" || !slices.Equal(sum.Lines, want) || sum.AgentText != "# 계획\n" {
		t.Fatalf("plan summary = %q\n%q", sum.Title, sum.Lines)
	}
	for mode, word := range map[domain.PermissionMode]string{domain.PermissionModeAuto: "자동", domain.PermissionModeBypassPermissions: "권한 우회", "": "—"} {
		if got := planSummary(view, ruleFacts{ceiling: mode}).Lines[6]; got != "규칙: 0개 켜짐, 권한 상한 "+word {
			t.Errorf("ceiling %q = %q", mode, got)
		}
	}
}

func TestIntegrationSummaryLines(t *testing.T) {
	checks := mustItem(t, domain.LadoVerifyRequiredChecks, domain.LadoVerifyPass, "", nil)
	info := mustItem(t, domain.LadoVerifyEvidence, domain.LadoVerifyInfo, domain.LadoCodeNotRequired, nil)
	outside := mustItem(t, domain.LadoVerifyPaths, domain.LadoVerifyConfirm, domain.LadoCodeOutsidePlan, domain.LadoPathsDetails{OutsidePlan: []string{"docs/a.md"}, OutsideCount: 1})
	unprotected := mustItem(t, domain.LadoVerifyProtection, domain.LadoVerifyConfirm, domain.LadoCodeUnprotected, domain.LadoProtectionDetails{Missing: []string{"deletion"}})
	pr := domain.LadoExecutionPR{URL: testPRURL, Number: 7, HeadSHA: testHead, TargetBranch: "main"}
	view := ladoqueue.ExecutionView{Execution: domain.LadoExecution{SessionID: "s", PR: pr}, Task: domain.LadoTask{Issue: domain.LadoObservedIssue{Title: "버그"}}}
	one := integrationSummary(view, []domain.LadoExecutionTrackedPR{{PR: pr}},
		[]ladoverify.ApprovePRResult{verifiedRecord(t, "s", testPRURL, testHead, checks, outside, unprotected, info)})
	wantLines := []string{"작업: 버그", "head: cccccccccccc", "검증 항목 3개 중 1개 통과", "계획 밖 변경 경로 (1): docs/a.md", "기준 브랜치 규칙에 없는 것 (1): 삭제 차단"}
	wantNext := "승인하면 LADO가 이 head에 lado/integration 상태를 게시합니다. LADO는 직접 병합하지 않습니다. 이 저장소는 보호 안 됨 상태입니다. LADO는 승인 없는 병합을 감지만 합니다."
	if one.Title != "통합을 승인할까요? PR #7 → main" || !slices.Equal(one.Lines, wantLines) || one.Next != wantNext {
		t.Fatalf("one PR:\n%q\n%q\n%q", one.Title, one.Lines, one.Next)
	}

	// Two PRs, one approved already, one with SQL files of both phases.
	pr2 := domain.LadoExecutionPR{URL: "https://github.com/acme/api/pull/3", Number: 3, HeadSHA: strings.Repeat("e", 40), TargetBranch: "dev"}
	db := mustItem(t, domain.LadoVerifyDBChanges, domain.LadoVerifyPass, "", domain.LadoDBChangesDetails{Files: []domain.LadoDBChangeFileRef{
		{Path: "db/1.sql", BlobSHA: "b1", Phase: domain.LadoDBChangePreMerge}, {Path: "db/2.sql", BlobSHA: "b2", Phase: domain.LadoDBChangeAfterDeploy}}})
	tracked := []domain.LadoExecutionTrackedPR{{PR: pr, Repo: "app"}, {PR: pr2, Repo: "api", Integration: domain.LadoExecutionIntegration{HeadApproved: true}}}
	many := integrationSummary(view, tracked, []ladoverify.ApprovePRResult{
		verifiedRecord(t, "s", testPRURL, testHead, checks),
		verifiedRecord(t, "s", pr2.URL, pr2.HeadSHA, checks, db),
	})
	wantMany := []string{
		"작업: 버그", "",
		"app의 PR #7 → main", "head: cccccccccccc", "검증 항목 1개 중 1개 통과", "",
		"api의 PR #3 → dev", "head: eeeeeeeeeeee", "검증 항목 2개 중 2개 통과", "병합 전 SQL: db/1.sql", "배포 뒤 SQL: db/2.sql", "이 head는 이미 승인되어 LADO가 그대로 둡니다.",
	}
	wantManyNext := "승인하면 LADO가 각 PR을 병합할 수 있는지 확인하고, 병합 전 SQL을 경로 순서로 적용한 뒤 lado/integration을 게시하고 적힌 순서대로 직접 병합합니다. 파일 하나라도 실패하면 병합하지 않습니다. 배포 뒤 SQL은 병합 뒤 앱에서 적용할 때까지 기다립니다."
	if many.Title != "이 작업의 PR 2개 통합을 승인할까요?" || !slices.Equal(many.Lines, wantMany) || many.Next != wantManyNext {
		t.Fatalf("two PRs:\n%q\n%q\n%q", many.Title, many.Lines, many.Next)
	}
}
