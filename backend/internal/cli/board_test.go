package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aoagents/agent-orchestrator/backend/internal/cli/boardtui"
)

const boardProjectsJSON = `{"projects":[
	{"id":"demo","name":"demo","path":"/repo/demo","folderMissing":false},
	{"id":"water","name":"water","path":"/repo/water","folderMissing":false}
]}`

const boardSessionsJSON = `{"sessions":[
	{"id":"demo-1","projectId":"demo","kind":"orchestrator","status":"working","kanbanColumn":"building","isTerminated":false,"createdAt":"2026-11-28T10:00:00Z","lastEventAt":"2026-11-28T11:59:00Z"},
	{"id":"demo-2","projectId":"demo","kind":"worker","displayName":"로그인 화면 고치기","status":"working","kanbanColumn":"building","isTerminated":false,"createdAt":"2026-11-28T10:00:00Z","lastEventAt":"2026-11-28T11:58:00Z","activity":{"state":"active","lastActivityAt":"2026-11-28T11:58:00Z"}},
	{"id":"demo-3","projectId":"demo","kind":"worker","status":"needs_input","kanbanColumn":"validating","isTerminated":false,"createdAt":"2026-11-28T10:00:00Z","lastEventAt":"2026-11-28T11:50:00Z","prs":[{"number":7,"url":"https://example.test/pr/7","state":"open","ci":"failing"}]},
	{"id":"demo-4","projectId":"demo","kind":"worker","status":"terminated","kanbanColumn":"archive","isTerminated":true,"createdAt":"2026-11-27T10:00:00Z","lastEventAt":"2026-11-27T11:00:00Z"},
	{"id":"water-1","projectId":"water","kind":"worker","status":"review_pending","kanbanColumn":"needs_review","isTerminated":false,"createdAt":"2026-11-28T10:00:00Z","lastEventAt":"2026-11-28T11:00:00Z","model":null,"displayName":null}
]}`

func boardServer(t *testing.T, sessions string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects":
			_, _ = io.WriteString(w, boardProjectsJSON)
		case "/api/v1/sessions":
			_, _ = io.WriteString(w, sessions)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBoardPrintShowsProjectSummary(t *testing.T) {
	t.Setenv("LC_ALL", "ko_KR.UTF-8")
	cfg := setConfigEnv(t)
	writeRunFileFor(t, cfg, boardServer(t, boardSessionsJSON))

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "board", "--print")
	if err != nil {
		t.Fatalf("board --print: %v\nstderr=%s", err, errOut)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("--print must not write ANSI escapes: %q", out)
	}
	for _, want := range []string{"LADO 보드", "개발 중", "검증 중", "리뷰 중", "준비 완료", "demo", "water"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "연결 끊김") {
		t.Errorf("--print must not claim a live connection state:\n%s", out)
	}
	demo := strings.Fields(lineWith(out, "demo "))
	if len(demo) < 7 || demo[2] != "1" || demo[3] != "1" || demo[4] != "0" || demo[5] != "0" || demo[6] != "1" {
		t.Errorf("demo row fields = %q, want building 1 validating 1 review 0 ready 0 attention 1", demo)
	}
}

func TestBoardPrintProjectShowsKanban(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	cfg := setConfigEnv(t)
	writeRunFileFor(t, cfg, boardServer(t, boardSessionsJSON))

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "board", "--print", "--project", "demo")
	if err != nil {
		t.Fatalf("board --print --project demo: %v\nstderr=%s", err, errOut)
	}
	for _, want := range []string{"Building 1", "Validating 1", "In review 0", "Ready 0", "로그인 화면 고치기", "demo-3", "Input needed", "PR #7"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"demo-1 ", "demo-4", "water-1"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output must not show %q (orchestrator, archived or other project):\n%s", unwanted, out)
		}
	}
}

func TestBoardRejectsUnknownProject(t *testing.T) {
	cfg := setConfigEnv(t)
	writeRunFileFor(t, cfg, boardServer(t, boardSessionsJSON))

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "board", "--print", "--project", "nope")
	if err == nil || !strings.Contains(err.Error(), `unknown project "nope"`) {
		t.Fatalf("err = %v, want unknown project", err)
	}
	if ExitCode(err) == 2 {
		t.Fatalf("an unknown project is a runtime lookup failure, not a usage error: %v", err)
	}
}

func TestBoardReportsMissingDaemon(t *testing.T) {
	setConfigEnv(t)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "board", "--print")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v, want daemon not running", err)
	}
}

func TestBoardRejectsArguments(t *testing.T) {
	setConfigEnv(t)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "board", "extra")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("err = %v, want usage error", err)
	}
}

// eventStream 은 연결마다 lines 를 보내고 끊는 SSE 서버다.
type eventStream struct {
	mu       sync.Mutex
	connects int
	queries  []string
	lines    string
}

func (e *eventStream) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/events" {
		http.NotFound(w, r)
		return
	}
	e.mu.Lock()
	e.connects++
	e.queries = append(e.queries, r.URL.RawQuery)
	e.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, e.lines)
}

func TestWatchBoardEventsSignalsChangesAndReconnects(t *testing.T) {
	cfg := setConfigEnv(t)
	stream := &eventStream{lines: strings.Join([]string{
		"id: 1", "event: cursor", "data: {}", "",
		"id: 2", "event: notification_created", "data: {}", "",
		"id: 3", "event: session_updated", `data: {"sessionId":"demo-2"}`, "",
		"id: 4", "event: project.max_concurrent_changed", "data: {}", "",
		"id: 5", "event: pr_updated", "data: {}", "",
		"event: heartbeat", "data: {}", "",
		"id: 6", "event: review_run_updated", `data: {"sessionId":"demo-2"}`, "",
		"",
	}, "\n")}
	srv := httptest.NewServer(http.HandlerFunc(stream.handler))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	var mu sync.Mutex
	var msgs []tea.Msg
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c := &commandContext{deps: Deps{ProcessAlive: func(int) bool { return true }}.withDefaults()}
	go func() {
		defer close(done)
		c.watchBoardEvents(ctx, func(msg tea.Msg) {
			mu.Lock()
			msgs = append(msgs, msg)
			mu.Unlock()
		}, time.Millisecond, time.Minute)
	}()
	waitFor(t, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return stream.connects >= 2
	})
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	// 한 연결: 붙음 → (붙자마자 놓친 변경을 메우는) 변경 → 이벤트 네 개 → 끊김.
	// 알림·heartbeat 는 거르고, 앱이 목록을 다시 읽는 세션·PR·리뷰 실행 이벤트와
	// 프로젝트 이벤트만 변경으로 본다.
	want := []tea.Msg{
		boardtui.LiveMsg{Connected: true}, boardtui.ChangedMsg{},
		boardtui.ChangedMsg{}, boardtui.ChangedMsg{}, boardtui.ChangedMsg{}, boardtui.ChangedMsg{},
		boardtui.LiveMsg{Connected: false},
	}
	if len(msgs) < len(want) {
		t.Fatalf("msgs = %v, want at least %v", msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Fatalf("msg[%d] = %#v, want %#v (all: %v)", i, msgs[i], want[i], msgs)
		}
	}
	if stream.queries[0] != "after=latest" {
		t.Fatalf("query = %q, want after=latest", stream.queries[0])
	}
}

func TestWatchBoardEventsWaitsForDaemon(t *testing.T) {
	setConfigEnv(t)
	var mu sync.Mutex
	var msgs []tea.Msg
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c := &commandContext{deps: Deps{ProcessAlive: func(int) bool { return true }}.withDefaults()}

	c.watchBoardEvents(ctx, func(msg tea.Msg) {
		mu.Lock()
		msgs = append(msgs, msg)
		mu.Unlock()
	}, time.Millisecond, time.Minute)

	for _, msg := range msgs {
		if msg == (boardtui.LiveMsg{Connected: true}) {
			t.Fatalf("no daemon, but watcher reported a live connection: %v", msgs)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

func lineWith(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func TestBoardEventsMatchTheAppRefetchList(t *testing.T) {
	for _, event := range []string{
		"session_created", "session_updated", "pr_created", "pr_updated", "pr_check_recorded",
		"pr_session_changed", "pr_review_thread_added", "pr_review_thread_resolved",
		"review_run_created", "review_run_updated", "project.max_concurrent_changed",
	} {
		if !boardEventMatters(event) {
			t.Errorf("%s should reload the board", event)
		}
	}
	for _, event := range []string{"", "cursor", "heartbeat", "notification_created", "lado_task_updated"} {
		if boardEventMatters(event) {
			t.Errorf("%s should not reload the board", event)
		}
	}
}

func TestBoardShowsDaemonErrorEnvelope(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/projects" {
			_, _ = io.WriteString(w, boardProjectsJSON)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"internal","code":"INTERNAL","message":"list sessions failed","requestId":"req-9"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "board", "--print")

	if err == nil || !strings.Contains(err.Error(), "list sessions failed") || !strings.Contains(err.Error(), "req-9") {
		t.Fatalf("err = %v, want the daemon message and request id", err)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("exit code = %d, want 1 for a daemon failure", ExitCode(err))
	}
}

func TestWatchBoardEventsTreatsNon200AsDisconnected(t *testing.T) {
	cfg := setConfigEnv(t)
	var mu sync.Mutex
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusNotImplemented)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var msgs []tea.Msg
	c := &commandContext{deps: Deps{ProcessAlive: func(int) bool { return true }}.withDefaults()}
	go func() {
		defer close(done)
		c.watchBoardEvents(ctx, func(msg tea.Msg) { msgs = append(msgs, msg) }, time.Millisecond, time.Minute)
	}()
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return attempts >= 3
	})
	cancel()
	<-done

	if len(msgs) != 0 {
		t.Fatalf("a refused stream must not report live or changes, got %v", msgs)
	}
}

func TestWatchBoardEventsReconnectsAfterSilence(t *testing.T) {
	cfg := setConfigEnv(t)
	var mu sync.Mutex
	connects := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connects++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // 연결을 연 채 아무것도 보내지 않는다.
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// 시험이 실패해도 감시를 먼저 멈춰야 srv.Close 가 열린 스트림을 기다리지 않는다.
	t.Cleanup(func() { cancel(); <-done })
	var msgsMu sync.Mutex
	var msgs []tea.Msg
	c := &commandContext{deps: Deps{ProcessAlive: func(int) bool { return true }}.withDefaults()}
	go func() {
		defer close(done)
		c.watchBoardEvents(ctx, func(msg tea.Msg) {
			msgsMu.Lock()
			msgs = append(msgs, msg)
			msgsMu.Unlock()
		}, time.Millisecond, 20*time.Millisecond)
	}()
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return connects >= 2
	})
	cancel()
	<-done

	msgsMu.Lock()
	defer msgsMu.Unlock()
	if len(msgs) < 3 || msgs[2] != (boardtui.LiveMsg{Connected: false}) {
		t.Fatalf("a silent stream should be dropped as disconnected, got %v", msgs)
	}
}
