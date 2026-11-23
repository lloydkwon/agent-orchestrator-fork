package ladotask

// Plans from the issue itself (LADO fork): the developer skips the plan
// worker and its review, and LADO writes the plan from the issue and pins it
// at once. The developer still approves that plan in the desktop app, and
// the run is verified and integrated like any other.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const (
	// issuePlanBodyBytes caps the issue body the plan quotes, well inside
	// domain.LadoPlanMaxBytes; the worker gets the whole pinned issue anyway.
	issuePlanBodyBytes = 256 << 10
	// issuePlanMaxCriteria caps the checklist items taken as acceptance
	// criteria.
	issuePlanMaxCriteria = 50
)

// issueChecklistItem matches a Markdown task list item, done or not.
var issueChecklistItem = regexp.MustCompile(`^\s*[-*+]\s+\[[ xX]\]\s+(\S.*)$`)

// IssuePlanInput asks for a plan from the task's issue.
type IssuePlanInput struct {
	TaskID domain.LadoTaskID
	// IssueSHA256 is the hash of the issue's title and body as the developer
	// read them (issueTextSHA256): the plan is made only from that text.
	IssueSHA256 string
}

// PlanFromIssue pins the task's issue with its trusted comments, as
// RequestPlan does, and answers the request at once with a plan LADO writes
// from the issue (issuePlanContent): no plan worker, critic or orchestrator
// is involved. It refuses when the issue's title or body is not the one the
// developer read, and while the task waits for or runs a worker. The request
// replaces the task's open plan worker requests. It returns the pinned
// revision, which waits for the developer's approval like any other.
func (s *Service) PlanFromIssue(ctx context.Context, in IssuePlanInput) (domain.LadoPlanRevision, error) {
	if strings.TrimSpace(in.IssueSHA256) == "" {
		return domain.LadoPlanRevision{}, apierr.Invalid("LADO_ISSUE_SHA_REQUIRED", "Send the sha256 of the issue text you read with the request.", nil)
	}
	task, err := s.openTask(ctx, in.TaskID)
	if err != nil {
		return domain.LadoPlanRevision{}, err
	}
	// Approving the plan would start a second worker, or replace a queued one.
	if err := approvable(s.withPolicy(ctx, task)); err != nil {
		return domain.LadoPlanRevision{}, err
	}
	base, err := s.issuePlanBase(ctx, task)
	if err != nil {
		return domain.LadoPlanRevision{}, err
	}
	if s.deps.Policy == nil {
		return domain.LadoPlanRevision{}, apierr.Unavailable("LADO_ISSUE_PLAN_RULES_UNREADABLE", "LADO could not read the rules that apply to this task. Try again.")
	}
	summary, err := s.deps.Policy.Summary(ctx, task)
	if err != nil {
		s.deps.Log.Warn("LADO could not read the rules for a plan from the issue", "task", task.ID, "err", err)
		return domain.LadoPlanRevision{}, apierr.Unavailable("LADO_ISSUE_PLAN_RULES_UNREADABLE", "LADO could not read the rules that apply to this task. Try again.")
	}
	layout, err := s.repoLayout(ctx, task)
	if err != nil {
		s.deps.Log.Warn("LADO could not find where a plan from the issue runs", "task", task.ID, "err", err)
		return domain.LadoPlanRevision{}, apierr.Unavailable("LADO_ISSUE_PLAN_REPO_UNKNOWN", "LADO could not find the repository that holds this task's issue.")
	}
	issue, comments, err := s.readIssueWithComments(ctx, task)
	if err != nil {
		return domain.LadoPlanRevision{}, err
	}
	if issueTextSHA256(issue) != strings.TrimSpace(in.IssueSHA256) {
		return domain.LadoPlanRevision{}, apierr.Conflict("LADO_ISSUE_CHANGED_SINCE_READ",
			"The issue changed since you read it. Read it again, then run it.", map[string]any{"issue": string(task.IssueRef)})
	}
	snapshot, _ := domain.NewLadoIssueSnapshot(task.IssueRef, issue.Title, issue.Body, comments)
	snapshotSum, err := s.deps.Blobs.Put(snapshot.CanonicalJSON())
	if err != nil {
		s.deps.Log.Error("LADO could not pin an issue snapshot", "task", task.ID, "err", err)
		return domain.LadoPlanRevision{}, apierr.Internal("LADO_SNAPSHOT_SAVE_FAILED", "Failed to save the issue snapshot")
	}
	// Once the request is stored, a dropped connection must not leave it
	// waiting for a plan; the plan loop fails one that a crash left behind.
	ctx = context.WithoutCancel(ctx)
	reqID := domain.LadoPlanRequestID("lado-plan-request-" + s.deps.NewID())
	req, err := s.deps.Store.CreateLadoPlanRequest(ctx, domain.LadoPlanRequest{
		ID:                  reqID,
		TaskID:              task.ID,
		Kind:                domain.LadoPlanRequestIssue,
		SessionID:           domain.LadoPlanRequestIssueSession(reqID),
		IssueSnapshotSHA256: snapshotSum,
		RequestedAt:         s.deps.Clock().UTC(),
	})
	if err != nil {
		return domain.LadoPlanRevision{}, apierr.Internal("LADO_PLAN_REQUEST_SAVE_FAILED", "Failed to save the plan request")
	}
	for _, older := range s.openWorkerRequests(ctx, task.ID, req.ID) {
		s.failWorker(ctx, older, domain.LadoPlanRequestReplaced, "a plan from the issue replaced it")
	}
	content := issuePlanContent(task, issue, base, summary.Ceiling, layout)
	sum := sha256.Sum256(content)
	rev, err := s.pinContent(ctx, task, req, content, domain.LadoPlanRevision{
		SessionID:       req.SessionID,
		Path:            domain.LadoPlanPath(task.ID),
		ArtifactVersion: 1,
		Source:          domain.LadoPlanRevisionSourceIssue,
		SHA256:          hex.EncodeToString(sum[:]),
	})
	if err != nil {
		s.deps.Log.Error("LADO could not pin a plan from the issue", "task", task.ID, "err", err)
		// The request would otherwise wait for a plan forever.
		s.failRequest(ctx, req, s.deps.Clock().UTC(), "LADO could not pin the plan from the issue")
		return domain.LadoPlanRevision{}, apierr.Internal("LADO_ISSUE_PLAN_SAVE_FAILED", "Failed to save the plan from the issue")
	}
	return rev, nil
}

// issuePlanBase is the branch the task's worker starts from, which the plan
// names as base_ref.
func (s *Service) issuePlanBase(ctx context.Context, task domain.LadoTask) (string, error) {
	if s.deps.Queue == nil {
		return "", apierr.Unavailable("LADO_ISSUE_PLAN_BASE_UNKNOWN", "LADO cannot tell which branch the worker starts from in this daemon.")
	}
	base, err := s.deps.Queue.WorkerBase(ctx, task)
	if err != nil || base.Branch == "" {
		s.deps.Log.Warn("LADO could not resolve the base of a plan from the issue", "task", task.ID, "err", err)
		return "", apierr.Unavailable("LADO_ISSUE_PLAN_BASE_UNKNOWN", "LADO could not tell which branch the worker starts from. Check the project's default branch, then try again.")
	}
	return base.Branch, nil
}

// issuePlanFront is the front matter of a plan from the issue, in the order
// of the plan format (planFormat).
type issuePlanFront struct {
	Task               string          `yaml:"task"`
	Issue              string          `yaml:"issue"`
	BaseRef            string          `yaml:"base_ref"`
	Steps              []string        `yaml:"steps"`
	AcceptanceCriteria []string        `yaml:"acceptance_criteria"`
	AffectedPaths      []string        `yaml:"affected_paths"`
	TestCommands       []string        `yaml:"test_commands"`
	Risk               string          `yaml:"risk"`
	PermissionCeiling  string          `yaml:"permission_ceiling"`
	Review             issuePlanReview `yaml:"review"`
	PlanSource         string          `yaml:"plan_source"`
}

type issuePlanReview struct {
	Required bool `yaml:"required"`
}

// issuePlanContent writes the plan file for a plan from the issue: the issue
// is the work, its checklist the acceptance criteria, and the repository
// that holds it (the whole project outside a workspace) the affected paths,
// so only the protected paths and the project's checks hold the worker back.
// The worker starts from base and may run in ceiling at most; the plan asks
// for no AO review, no parts and no test commands of its own.
func issuePlanContent(task domain.LadoTask, issue domain.Issue, base string, ceiling domain.PermissionMode, layout repoLayout) []byte {
	affected := "**"
	if folder := strings.Trim(layout.relative, "/"); layout.workspace && folder != "" {
		affected = folder + "/**"
	}
	body := strings.TrimSpace(strings.ReplaceAll(issue.Body, domain.LadoTaskMarker(task.ID), ""))
	criteria := issueChecklist(body)
	if len(criteria) == 0 {
		criteria = []string{"이슈 본문에 적힌 요구가 모두 충족된다."}
	}
	front, _ := yaml.Marshal(issuePlanFront{
		Task:               string(task.ID),
		Issue:              string(task.IssueRef),
		BaseRef:            base,
		Steps:              []string{"이슈 " + string(task.IssueRef) + " 의 본문에 적힌 대로 구현한다."},
		AcceptanceCriteria: criteria,
		AffectedPaths:      []string{affected},
		TestCommands:       []string{},
		Risk:               string(domain.LadoPlanRiskMedium),
		PermissionCeiling:  string(issuePlanCeiling(ceiling)),
		Review:             issuePlanReview{Required: false},
		PlanSource:         domain.LadoPlanSourceIssue,
	})
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n\n")
	// A title on one line cannot add headings of its own.
	b.WriteString("# " + strings.Join(strings.Fields(issue.Title), " ") + "\n\n")
	b.WriteString("개발자가 계획 검토 없이 바로 진행하기로 해서 LADO가 이슈 내용으로 이 계획을 만들었다. " +
		"계획 워커와 검수자는 거치지 않았다. 할 일은 아래 이슈 본문이 설명하고, 결정이 필요하면 소유자에게 묻는다.\n\n")
	// The body is the tracker's text, not LADO's: it sits under the trust
	// boundary in a fence no backtick run inside it can close, as the pinned
	// issue the orchestrator reads does.
	b.WriteString(issueTrustBoundary + "\n\n")
	b.WriteString("## 이슈 본문\n\n")
	if body == "" {
		b.WriteString("(본문 없음: 제목이 할 일이다.)\n")
	} else {
		if len(body) > issuePlanBodyBytes {
			body = truncateUTF8(body, issuePlanBodyBytes) + "\n\n(본문이 길어 여기서 잘랐다. 전체는 고정된 이슈에 있다.)"
		}
		writeFenced(&b, body)
	}
	return []byte(b.String())
}

// issueTextSHA256 is the hash of an issue's title and body, as LADO records
// what it last read of an issue (observedIssue). A plan from the issue binds
// to it, since only the title and the body go into that plan.
func issueTextSHA256(issue domain.Issue) string {
	return domain.LadoIssueSnapshot{ID: domain.LadoIssueRef(issue.ID), Title: issue.Title, Body: issue.Body}.SHA256()
}

// issuePlanCeiling is the plan's permission ceiling: the rules' ceiling, or
// accept-edits when the rules name none.
func issuePlanCeiling(ceiling domain.PermissionMode) domain.PermissionMode {
	if ceiling == "" {
		return domain.PermissionModeAcceptEdits
	}
	return ceiling
}

// issueChecklist returns the items of the body's Markdown task lists outside
// code fences, in order and without duplicates.
func issueChecklist(body string) []string {
	var items []string
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		match := issueChecklistItem.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		item := strings.TrimSpace(match[1])
		if item == "" || slices.Contains(items, item) {
			continue
		}
		items = append(items, item)
		if len(items) == issuePlanMaxCriteria {
			break
		}
	}
	return items
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for cut != "" && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
