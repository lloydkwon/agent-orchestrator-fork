package ntfy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// sendTimeout bounds one publish: a slow server must not hold the daemon.
const sendTimeout = 5 * time.Second

// AlertPollInterval is how often the dispatcher reads new LADO alerts.
const AlertPollInterval = 30 * time.Second

// alertLookback is how far back each poll reads. Each producer stamps its
// alerts with its own clock, so an alert can be stamped before one read
// earlier; the ids, which grow as rows go in, mark what was read.
const alertLookback = 30 * time.Minute

// ntfy priorities: 4 is high, 3 the default.
const (
	priorityHigh    = 4
	priorityDefault = 3
)

// Subscriber is AO's notification hub.
type Subscriber interface {
	Subscribe(projectID domain.ProjectID) (<-chan domain.NotificationEvent, func())
}

// Alerts reads LADO's alerts raised at or after a time, oldest first, with
// their task's project and issue (*store.Store).
type Alerts interface {
	ListLadoAlertsSince(ctx context.Context, since time.Time) ([]domain.LadoAlert, error)
}

// Tasks finds the LADO task of a worker session, for its issue number
// (*store.Store).
type Tasks interface {
	LadoExecution(ctx context.Context, sessionID domain.SessionID) (domain.LadoExecution, bool, error)
	LadoTask(ctx context.Context, id domain.LadoTaskID) (domain.LadoTask, bool, error)
}

// Deps wires a Dispatcher. Alerts and Tasks may be nil: then no LADO alert
// goes out, and messages carry no issue number.
type Deps struct {
	Alerts Alerts
	Tasks  Tasks
	Client *http.Client
	Log    *slog.Logger
}

// Dispatcher publishes the notifications and alerts that need the owner.
type Dispatcher struct {
	cfg  Config
	deps Deps

	mu sync.Mutex
	// lastID is the newest alert the poll read; started is false before the
	// first poll, which only marks where to start.
	started bool
	lastID  domain.LadoAlertID
}

// New returns a Dispatcher publishing to cfg.
func New(cfg Config, deps Deps) *Dispatcher {
	if deps.Client == nil {
		deps.Client = &http.Client{Timeout: sendTimeout}
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Dispatcher{cfg: cfg, deps: deps}
}

// Run publishes until ctx ends. Alerts raised before it started are not
// sent: the owner saw them, or they are old news.
func (d *Dispatcher) Run(ctx context.Context, sub Subscriber) {
	events, unsubscribe := sub.Subscribe("")
	defer unsubscribe()
	ticker := time.NewTicker(AlertPollInterval)
	defer ticker.Stop()
	d.pollAlerts(ctx, time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			d.handle(ctx, event)
		case now := <-ticker.C:
			d.pollAlerts(ctx, now.UTC())
		}
	}
}

// handle publishes a new notification that needs the owner.
func (d *Dispatcher) handle(ctx context.Context, event domain.NotificationEvent) {
	if event.Kind != domain.NotificationCreated {
		return
	}
	rec := event.Record
	issue := d.sessionIssue(ctx, rec.SessionID)
	switch rec.Type {
	case domain.NotificationNeedsInput:
		d.publish(ctx, message{
			Title:    headline("💬", string(rec.ProjectID), issue, "답을 기다림"),
			Message:  fmt.Sprintf("%s 세션이 사람의 답이나 승인을 기다립니다", rec.SessionID),
			Priority: priorityHigh,
		})
	case domain.NotificationReadyToMerge:
		d.publish(ctx, message{
			Title:    headline("🔀", string(rec.ProjectID), issue, "병합 준비"),
			Message:  fmt.Sprintf("%s 세션의 PR 이 병합을 기다립니다", rec.SessionID),
			Priority: priorityDefault,
		})
	}
}

// pollAlerts publishes the LADO alerts raised since the last poll that need
// the owner. The first poll only marks where to start.
func (d *Dispatcher) pollAlerts(ctx context.Context, now time.Time) {
	if d.deps.Alerts == nil {
		return
	}
	alerts, err := d.deps.Alerts.ListLadoAlertsSince(ctx, now.Add(-alertLookback))
	if err != nil {
		d.deps.Log.Warn("ntfy could not read the LADO alerts", "err", err)
		return
	}
	d.mu.Lock()
	started, lastID := d.started, d.lastID
	d.mu.Unlock()
	newest := lastID
	for _, a := range alerts {
		if a.ID <= lastID {
			continue
		}
		newest = max(newest, a.ID)
		if !started {
			continue
		}
		if m, ok := alertMessage(a); ok {
			d.publish(ctx, m)
		}
	}
	d.mu.Lock()
	d.started, d.lastID = true, newest
	d.mu.Unlock()
}

// alertMessage is the message of a LADO alert that needs the owner; ok is
// false for an alert that only informs.
func alertMessage(a domain.LadoAlert) (message, bool) {
	issue := issueNumber(a.IssueRef)
	project := string(a.ProjectID)
	who := string(a.SessionID)
	if who == "" {
		who = "작업"
	}
	switch a.Kind {
	case domain.LadoAlertStalled:
		return message{
			Title:    headline("⏸", project, issue, "워커 멈춤"),
			Message:  fmt.Sprintf("%s 세션이 보고 없이 %d분 넘게 멈췄습니다", who, a.Value),
			Priority: priorityHigh,
		}, true
	case domain.LadoAlertLimited:
		return message{
			Title:    headline("⛔", project, issue, "계정 한도"),
			Message:  fmt.Sprintf("%s 세션이 사용 한도에 걸렸습니다. 새 워커는 대기합니다", who),
			Priority: priorityHigh,
		}, true
	case domain.LadoAlertAgentUnavailable:
		return message{
			Title:    headline("🚫", project, issue, "에이전트 시작 불가"),
			Message:  "LADO 가 워커를 띄울 에이전트를 시작할 수 없습니다",
			Priority: priorityHigh,
		}, true
	case domain.LadoAlertBackgroundOverdue:
		return message{
			Title:    headline("💻", project, issue, "백그라운드 명령 지연"),
			Message:  fmt.Sprintf("%s 세션의 백그라운드 명령이 %d분째 끝나지 않습니다", who, a.Value),
			Priority: priorityHigh,
		}, true
	case domain.LadoAlertConflictUnresolved:
		return message{
			Title:    headline("⚠", project, issue, "충돌 해결 실패"),
			Message:  fmt.Sprintf("LADO 가 PR 충돌을 %d번 연달아 풀지 못했습니다", a.Value),
			Priority: priorityHigh,
		}, true
	}
	return message{}, false
}

// sessionIssue is the issue number of the session's LADO task, or "".
func (d *Dispatcher) sessionIssue(ctx context.Context, id domain.SessionID) string {
	if d.deps.Tasks == nil || id == "" {
		return ""
	}
	exec, ok, err := d.deps.Tasks.LadoExecution(ctx, id)
	if err != nil || !ok {
		return ""
	}
	task, ok, err := d.deps.Tasks.LadoTask(ctx, exec.TaskID)
	if err != nil || !ok {
		return ""
	}
	return issueNumber(task.IssueRef)
}

// issueNumber is "#13" of "gitlab:group/repo#13", or "".
func issueNumber(ref domain.IssueID) string {
	s := string(ref)
	i := strings.LastIndex(s, "#")
	if i < 0 || i == len(s)-1 {
		return ""
	}
	return s[i:]
}

func headline(mark, project, issue, what string) string {
	parts := []string{mark}
	if project != "" {
		parts = append(parts, project)
	}
	if issue != "" {
		parts = append(parts, issue)
	}
	return strings.Join(parts, " ") + " · " + what
}

// message is one ntfy JSON publish.
type message struct {
	Topic    string `json:"topic"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
}

// publish sends m once; a failure is logged, not retried.
func (d *Dispatcher) publish(ctx context.Context, m message) {
	m.Topic = d.cfg.Topic
	body, err := json.Marshal(m)
	if err != nil {
		d.deps.Log.Warn("ntfy could not encode a message", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.Server+"/", bytes.NewReader(body))
	if err != nil {
		d.deps.Log.Warn("ntfy could not build a request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.deps.Client.Do(req)
	if err != nil {
		d.deps.Log.Warn("ntfy publish failed", "err", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		d.deps.Log.Warn("ntfy publish refused", "status", resp.StatusCode)
	}
}
