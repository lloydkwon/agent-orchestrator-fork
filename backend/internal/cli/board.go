package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/cli/boardtui"
)

const (
	// boardPrintWidth 는 --print 의 폭이다. COLUMNS 가 있으면 그것을 쓴다.
	boardPrintWidth = 120
	// boardRetryDelay 는 이벤트 연결이 끊긴 뒤 다시 붙기까지 기다리는 시간이다.
	boardRetryDelay = 3 * time.Second
	// boardIdleTimeout 동안 한 줄도 오지 않으면 연결이 죽은 것으로 보고 다시 붙는다.
	// daemon 은 10초마다 heartbeat 를 보낸다(httpd/events.go eventsHeartbeatInterval).
	boardIdleTimeout = 30 * time.Second
	// boardMaxEventLine 은 SSE 한 줄의 최대 크기다. 넘는 줄은 연결을 끊고 다시 붙는다.
	boardMaxEventLine = 4 << 20
)

type boardOptions struct {
	project string
	print   bool
}

func newBoardCommand(ctx *commandContext) *cobra.Command {
	var opts boardOptions
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Show projects and their sessions as a kanban board in the terminal",
		Long: "Show every project with its session counts per kanban column, and open a project " +
			"to see its live worker sessions as the same kanban the desktop app shows. " +
			"Read-only: it never changes a session. Updates live from the daemon event stream. " +
			"Without a terminal (or with --print) it prints one snapshot and exits.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runBoard(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.project, "project", "", "Open this project's kanban directly")
	cmd.Flags().BoolVar(&opts.print, "print", false, "Print one plain-text snapshot instead of the interactive screen")
	return cmd
}

func (c *commandContext) runBoard(ctx context.Context, opts boardOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	snap, err := c.loadBoardSnapshot(ctx)
	if err != nil {
		return err
	}
	if opts.project != "" && !boardHasProject(snap, opts.project) {
		return fmt.Errorf("unknown project %q — see `ao project ls`", opts.project)
	}
	labels := boardtui.LabelsFor(boardLocale())
	if opts.print || !c.boardIsInteractive() {
		return c.printBoard(snap, opts.project, labels)
	}
	return c.runInteractiveBoard(ctx, opts.project, labels)
}

func (c *commandContext) loadBoardSnapshot(ctx context.Context) (boardtui.Snapshot, error) {
	var projects struct {
		Projects []boardtui.Project `json:"projects"`
	}
	if err := c.getJSON(ctx, "projects", &projects); err != nil {
		return boardtui.Snapshot{}, err
	}
	var sessions struct {
		Sessions []boardtui.Session `json:"sessions"`
	}
	if err := c.getJSON(ctx, "sessions", &sessions); err != nil {
		return boardtui.Snapshot{}, err
	}
	return boardtui.Snapshot{Projects: projects.Projects, Sessions: sessions.Sessions}, nil
}

func boardHasProject(snap boardtui.Snapshot, id string) bool {
	for _, p := range snap.Projects {
		if p.ID == id {
			return true
		}
	}
	for _, s := range snap.Sessions {
		if s.ProjectID == id && !boardtui.IsArchived(s) {
			return true
		}
	}
	return false
}

// boardLocale 은 화면 언어를 고르는 locale 값이다. POSIX 와 같은 순서로 본다.
func boardLocale() string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func (c *commandContext) boardIsInteractive() bool {
	in, inOK := c.deps.In.(*os.File)
	out, outOK := c.deps.Out.(*os.File)
	return inOK && outOK && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

func (c *commandContext) printBoard(snap boardtui.Snapshot, project string, labels boardtui.Labels) error {
	m := boardtui.New(boardtui.Options{
		Load:      func(context.Context) (boardtui.Snapshot, error) { return snap, nil },
		Labels:    labels,
		Now:       c.boardNow,
		ProjectID: project,
		Static:    true,
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: boardPrintColumns(), Height: 0})
	next, _ = next.Update(boardtui.SnapshotLoaded(snap))
	model, ok := next.(boardtui.Model)
	if !ok {
		return errors.New("board: unexpected model type")
	}
	_, err := fmt.Fprintln(c.deps.Out, trimRight(ansi.Strip(model.Render())))
	return err
}

func (c *commandContext) boardNow() time.Time {
	if c.deps.Now != nil {
		return c.deps.Now()
	}
	return time.Now()
}

func boardPrintColumns() int {
	var columns int
	if _, err := fmt.Sscan(os.Getenv("COLUMNS"), &columns); err == nil && columns > 0 {
		return columns
	}
	return boardPrintWidth
}

// trimRight 는 줄 끝 공백을 지운다. 칸 맞춤용 공백이 파일·파이프에 남지 않게 한다.
func trimRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func (c *commandContext) runInteractiveBoard(ctx context.Context, project string, labels boardtui.Labels) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := boardtui.New(boardtui.Options{
		Load:      c.loadBoardSnapshot,
		Labels:    labels,
		Now:       c.boardNow,
		ProjectID: project,
	})
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(c.deps.In), tea.WithOutput(c.deps.Out))
	go c.watchBoardEvents(ctx, p.Send, boardRetryDelay, boardIdleTimeout)
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// watchBoardEvents 는 daemon 의 CDC 이벤트를 읽어 화면에 알린다. 붙으면
// LiveMsg{true} 와, 붙기 전에 놓쳤을 변경을 메우는 ChangedMsg 를 보낸다. 보드를
// 바꿀 수 있는 이벤트마다 ChangedMsg 를 보내고, 끊기거나 idle 동안 한 줄도 오지
// 않으면 LiveMsg{false} 를 보낸 뒤 retry 만큼 쉬고 다시 붙는다. ctx 가 끝나면 돌아온다.
func (c *commandContext) watchBoardEvents(ctx context.Context, send func(tea.Msg), retry, idle time.Duration) {
	for ctx.Err() == nil {
		connected, _ := c.streamBoardEvents(ctx, send, idle)
		if connected {
			send(boardtui.LiveMsg{Connected: false})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func (c *commandContext) streamBoardEvents(ctx context.Context, send func(tea.Msg), idle time.Duration) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 줄이 올 때마다 늦춘다. 멈춘 연결은 요청을 끊어 읽기에서 빠져나온다.
	watchdog := time.AfterFunc(idle, cancel)
	defer watchdog.Stop()
	url, err := c.daemonURL("/api/v1/events?after=latest")
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody) // #nosec G704 -- daemon host is fixed loopback; path is an internal API route.
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	// 스트림은 오래 열려 있으므로 주입된 transport 만 쓰고 시간 제한은 없앤다.
	client := *c.deps.HTTPClient
	client.Timeout = 0
	resp, err := client.Do(req) // #nosec G704 -- request target is the fixed loopback daemon URL above.
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.CopyN(io.Discard, resp.Body, maxDrainedBodyBytes)
		return false, fmt.Errorf("event stream: HTTP %d", resp.StatusCode)
	}
	send(boardtui.LiveMsg{Connected: true})
	send(boardtui.ChangedMsg{})
	return true, readBoardEvents(resp.Body, send, func() { watchdog.Reset(idle) })
}

// readBoardEvents 는 SSE 를 읽다가 끝나거나 오류가 나면 돌아온다. 줄마다 seen 을 부른다.
func readBoardEvents(body io.Reader, send func(tea.Msg), seen func()) error {
	reader := bufio.NewReaderSize(body, 64<<10)
	event := ""
	for {
		line, err := readEventLine(reader)
		if err != nil {
			return err
		}
		seen()
		switch {
		case line == "":
			if boardEventMatters(event) {
				send(boardtui.ChangedMsg{})
			}
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
}

func readEventLine(reader *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		chunk, isPrefix, err := reader.ReadLine()
		if err != nil {
			return "", err
		}
		if b.Len()+len(chunk) > boardMaxEventLine {
			return "", errors.New("event stream: line too long")
		}
		b.Write(chunk)
		if !isPrefix {
			return b.String(), nil
		}
	}
}

// boardRefetchEvents 는 앱이 목록을 다시 읽는 CDC 이벤트다
// (frontend/src/renderer/lib/event-transport.ts 의 CDC_EVENT_TYPES).
var boardRefetchEvents = map[string]struct{}{
	"session_created":           {},
	"session_updated":           {},
	"pr_created":                {},
	"pr_updated":                {},
	"pr_check_recorded":         {},
	"pr_session_changed":        {},
	"pr_review_thread_added":    {},
	"pr_review_thread_resolved": {},
	"review_run_created":        {},
	"review_run_updated":        {},
}

// boardEventMatters 는 보드 숫자·카드를 바꿀 수 있는 이벤트인지 본다. 앱의 목록에
// 프로젝트 이벤트(project.*)를 더한다. 프로젝트 이름·폴더 상태가 표에 나오기 때문이다.
func boardEventMatters(event string) bool {
	if _, ok := boardRefetchEvents[event]; ok {
		return true
	}
	return strings.HasPrefix(event, "project")
}
