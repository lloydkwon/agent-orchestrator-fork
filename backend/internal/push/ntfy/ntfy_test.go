package ntfy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// published is one message the fake ntfy server got.
type published struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
}

type fakeServer struct {
	mu   sync.Mutex
	got  []published
	hits int
	srv  *httptest.Server
	fail bool
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		var p published
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		if f.fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		f.got = append(f.got, p)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) messages() []published {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]published(nil), f.got...)
}

type fakeAlerts struct {
	mu     sync.Mutex
	alerts []domain.LadoAlert
}

func (a *fakeAlerts) ListLadoAlertsSince(_ context.Context, since time.Time) ([]domain.LadoAlert, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []domain.LadoAlert
	for _, al := range a.alerts {
		if !al.CreatedAt.Before(since) {
			out = append(out, al)
		}
	}
	return out, nil
}

type fakeTasks struct{}

func (fakeTasks) LadoExecution(_ context.Context, id domain.SessionID) (domain.LadoExecution, bool, error) {
	if id == "water_ai-52" {
		return domain.LadoExecution{SessionID: id, TaskID: "task-13"}, true, nil
	}
	return domain.LadoExecution{}, false, nil
}

func (fakeTasks) LadoTask(_ context.Context, id domain.LadoTaskID) (domain.LadoTask, bool, error) {
	if id == "task-13" {
		return domain.LadoTask{ID: id, ProjectID: "water_ai", IssueRef: "gitlab:lado/water_ai#13"}, true, nil
	}
	return domain.LadoTask{}, false, nil
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newDispatcher(t *testing.T, srv *fakeServer, alerts *fakeAlerts) *Dispatcher {
	t.Helper()
	return New(Config{Server: srv.srv.URL, Topic: "test-topic"}, Deps{Alerts: alerts, Tasks: fakeTasks{}, Log: quiet, Client: srv.srv.Client()})
}

func TestOnlyNotificationsThatNeedTheOwnerGoOut(t *testing.T) {
	srv := newFakeServer(t)
	d := newDispatcher(t, srv, &fakeAlerts{})
	ctx := context.Background()
	secret := "PROMPT: deploy the prod database with password hunter2"
	for _, typ := range []domain.NotificationType{
		domain.NotificationNeedsInput, domain.NotificationReadyToMerge,
		domain.NotificationPRMerged, domain.NotificationReviewCompleted,
	} {
		d.handle(ctx, domain.NotificationEvent{Kind: domain.NotificationCreated, Record: domain.NotificationRecord{
			ID: "ntf-" + string(typ), Type: typ, SessionID: "water_ai-52", ProjectID: "water_ai",
			Title: secret, Body: secret,
		}})
	}
	// A resolution is not news for a phone.
	d.handle(ctx, domain.NotificationEvent{Kind: domain.NotificationResolved, Record: domain.NotificationRecord{
		ID: "ntf-x", Type: domain.NotificationNeedsInput, SessionID: "water_ai-52", ProjectID: "water_ai",
	}})
	got := srv.messages()
	if len(got) != 2 {
		t.Fatalf("published %d messages, want 2: %+v", len(got), got)
	}
	in := got[0]
	if in.Topic != "test-topic" || in.Priority != 4 || !strings.Contains(in.Title, "water_ai") || !strings.Contains(in.Title, "#13") || !strings.Contains(in.Message, "water_ai-52") {
		t.Fatalf("needs_input message = %+v", in)
	}
	for _, m := range got {
		if strings.Contains(m.Title+m.Message, "hunter2") || strings.Contains(m.Title+m.Message, "PROMPT") {
			t.Fatalf("a notification's own text went to the public server: %+v", m)
		}
	}
}

func TestOwnerAlertsGoOutOnceAndInformingOnesStay(t *testing.T) {
	srv := newFakeServer(t)
	start := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)
	alerts := &fakeAlerts{alerts: []domain.LadoAlert{
		{ID: 1, Kind: domain.LadoAlertStalled, SessionID: "water_ai-51", ProjectID: "water_ai", IssueRef: "gitlab:lado/water_ai#13", Value: 120, CreatedAt: start.Add(-time.Hour)},
	}}
	d := newDispatcher(t, srv, alerts)
	ctx := context.Background()
	d.pollAlerts(ctx, start)
	if n := len(srv.messages()); n != 0 {
		t.Fatalf("an alert from before the daemon started went out: %+v", srv.messages())
	}
	alerts.mu.Lock()
	alerts.alerts = append(alerts.alerts,
		domain.LadoAlert{ID: 2, Kind: domain.LadoAlertStalled, SessionID: "water_ai-51", ProjectID: "water_ai", IssueRef: "gitlab:lado/water_ai#13", Value: 120, CreatedAt: start.Add(time.Minute)},
		domain.LadoAlert{ID: 3, Kind: domain.LadoAlertLimitResumed, ProjectID: "water_ai", IssueRef: "gitlab:lado/water_ai#13", CreatedAt: start.Add(time.Minute)},
		domain.LadoAlert{ID: 4, Kind: domain.LadoAlertAgentUnavailable, ProjectID: "water_ai", IssueRef: "gitlab:lado/water_ai#14", CreatedAt: start.Add(2 * time.Minute)},
	)
	alerts.mu.Unlock()
	d.pollAlerts(ctx, start.Add(3*time.Minute))
	d.pollAlerts(ctx, start.Add(4*time.Minute))
	got := srv.messages()
	if len(got) != 2 {
		t.Fatalf("published %d messages, want 2 (stalled, agent_unavailable): %+v", len(got), got)
	}
	if !strings.Contains(got[0].Title, "#13") || !strings.Contains(got[0].Message, "120") {
		t.Fatalf("stalled message = %+v", got[0])
	}
	if !strings.Contains(got[1].Title, "#14") {
		t.Fatalf("agent_unavailable message = %+v", got[1])
	}

	// An alert stamped earlier than one already read, but raised after it,
	// still goes out: the alert ids, not their times, mark what was read.
	alerts.mu.Lock()
	alerts.alerts = append(alerts.alerts, domain.LadoAlert{
		ID: 5, Kind: domain.LadoAlertConflictUnresolved, ProjectID: "water_ai", IssueRef: "gitlab:lado/water_ai#15", Value: 3, CreatedAt: start.Add(30 * time.Second),
	})
	alerts.mu.Unlock()
	d.pollAlerts(ctx, start.Add(5*time.Minute))
	if got := srv.messages(); len(got) != 3 || !strings.Contains(got[2].Title, "#15") {
		t.Fatalf("the late alert did not go out: %+v", got)
	}
}

func TestAFailedSendIsLoggedNotRetried(t *testing.T) {
	srv := newFakeServer(t)
	srv.fail = true
	d := newDispatcher(t, srv, &fakeAlerts{})
	d.handle(context.Background(), domain.NotificationEvent{Kind: domain.NotificationCreated, Record: domain.NotificationRecord{
		ID: "ntf-1", Type: domain.NotificationNeedsInput, SessionID: "s-1", ProjectID: "p",
	}})
	srv.mu.Lock()
	hits := srv.hits
	srv.mu.Unlock()
	if hits != 1 {
		t.Fatalf("the server was asked %d times, want 1 (no retry)", hits)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := LoadConfig(dir); ok || err != nil {
		t.Fatalf("without a file: ok=%v err=%v, want off", ok, err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"topic":"abc-123"}`)
	cfg, ok, err := LoadConfig(dir)
	if err != nil || !ok || cfg.Topic != "abc-123" || cfg.Server != DefaultServer {
		t.Fatalf("cfg=%+v ok=%v err=%v", cfg, ok, err)
	}
	// Plain http only reaches a server on this computer: the topic is in the body.
	write(`{"topic":"t","server":"http://127.0.0.1:2586"}`)
	if _, ok, err := LoadConfig(dir); !ok || err != nil {
		t.Fatalf("a loopback http server: ok=%v err=%v", ok, err)
	}
	for _, bad := range []string{`{"topic":""}`, `{"topic":"a/b"}`, `{"topic":"t","server":"ftp://x"}`, `{"topic":"t","server":"http://ntfy.example.com"}`, `not json`} {
		write(bad)
		if _, ok, err := LoadConfig(dir); ok || err == nil {
			t.Fatalf("%s: ok=%v err=%v, want an error", bad, ok, err)
		}
	}
}
