package ladophone

// What the phone shows about an approval, made in Go from facts.

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

// maxExampleChars bounds one value quoted in a line.
const maxExampleChars = 120

// summary is an approval's title and lines.
type summary struct {
	Title     string
	Lines     []string
	Next      string
	AgentText string
}

func planSummary(view ladotask.PlanView, rules ruleFacts) summary {
	rev := view.Revision
	return summary{
		Title: "이 계획을 승인할까요?",
		Lines: []string{
			oneLine(view.Task.Issue.Title),
			fmt.Sprintf("%s, 리비전 %d", oneLine(string(view.Task.IssueRef)), rev.Number),
			"sha256 " + short(rev.SHA256, 12),
			fmt.Sprintf("규칙: %d개 켜짐, 권한 상한 %s", rules.on, rules.ceiling),
			"규칙 sha256 " + short(rules.sha256, 12),
		},
		AgentText: view.Content,
	}
}

func integrationSummary(view ladoqueue.ExecutionView, prs []domain.LadoExecutionTrackedPR, results []ladoverify.ApprovePRResult) summary {
	sum := summary{Title: "통합을 승인할까요?", Lines: []string{"작업: " + oneLine(view.Task.Issue.Title)}}
	if len(prs) == 1 {
		sum.Title = fmt.Sprintf("통합을 승인할까요? PR #%d → %s", prs[0].PR.Number, excerpt(prs[0].PR.TargetBranch, maxExampleChars))
	}
	for _, r := range results {
		sum.Lines = append(sum.Lines, "head: "+short(r.Verification.HeadSHA, 12))
	}
	return sum
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// oneLine is s without control characters.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}

// excerpt is oneLine of s cut to limit runes.
func excerpt(s string, limit int) string {
	runes := []rune(oneLine(s))
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return string(runes)
}
