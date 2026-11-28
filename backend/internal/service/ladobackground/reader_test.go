package ladobackground

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeStore struct {
	paths     []string
	written   [][]domain.BackgroundTask
	forgotten int
}

func (f *fakeStore) ForgetLadoSessionBackgroundReading(context.Context, domain.SessionID) error {
	f.forgotten++
	return nil
}

func (f *fakeStore) ListLadoSessionTranscriptSources(context.Context, domain.SessionID) ([]domain.LadoTranscriptSource, error) {
	out := make([]domain.LadoTranscriptSource, 0, len(f.paths))
	for _, p := range f.paths {
		out = append(out, domain.LadoTranscriptSource{Kind: domain.UsageSourceClaudeMain, Path: p})
	}
	return out, nil
}

func (f *fakeStore) SetLadoSessionBackgroundReading(_ context.Context, _ domain.SessionID, tasks []domain.BackgroundTask, _ time.Time) error {
	f.written = append(f.written, tasks)
	return nil
}

type fakeShells struct {
	n  int
	ok bool
}

func (f fakeShells) LiveBackgroundShells(domain.SessionID) (int, bool) { return f.n, f.ok }

const worker = domain.SessionID("water_ai-54")

func start(id, cmd, at string) string {
	line, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": at,
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_use", "id": id, "name": "Bash",
			"input": map[string]any{"command": cmd, "run_in_background": true},
		}}},
	})
	return string(line) + "\n"
}

func foreground(id string) string {
	line, _ := json.Marshal(map[string]any{
		"type": "assistant", "message": map[string]any{"content": []any{map[string]any{
			"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "ls"},
		}}},
	})
	return string(line) + "\n"
}

func notice(id string, asList bool) string {
	text := "<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>" + id + "</tool-use-id>\n<status>completed</status>\n</task-notification>"
	var content any = text
	if asList {
		content = []any{map[string]any{"type": "text", "text": text}}
	}
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	return string(line) + "\n"
}

func newReader(t *testing.T, st *fakeStore, shells Shells) *Reader {
	t.Helper()
	return New(Deps{Store: st, Shells: shells, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// LADO fork (2026-11-27): an open run_in_background with no task
// notification yet runs; a notice, in either content shape, ends it.
func TestReadPairsBackgroundStartsWithTheirNotices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, path, start("toolu_1", "end=$(( $(date +%s) + 1500 ))", "2026-11-27T01:58:51.097Z")+
		notice("toolu_1", false)+
		foreground("toolu_2")+
		"{not json\n"+
		start("toolu_3", "bash ~/.ao/data/artifacts/water_ai-54/wxdrill/records/wait_event.sh 7000\nsecond line", "2026-11-27T03:01:07.931Z"))
	st := &fakeStore{paths: []string{path}}
	r := newReader(t, st, fakeShells{n: 1, ok: true})
	now := time.Date(2026, 11, 27, 4, 0, 0, 0, time.UTC)
	got, ok, err := r.Read(context.Background(), worker, now)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(got.Tasks) != 1 || got.Source != domain.BackgroundSourceTranscript {
		t.Fatalf("got %+v", got)
	}
	task := got.Tasks[0]
	if task.Type != domain.BackgroundTaskShell || !strings.HasPrefix(task.Description, "bash ~/.ao/data/artifacts/water_ai-54") ||
		strings.Contains(task.Description, "second line") || !task.StartedAt.Equal(time.Date(2026, 11, 27, 3, 1, 7, 931e6, time.UTC)) {
		t.Fatalf("task = %+v", task)
	}
	if len(st.written) != 1 || len(st.written[0]) != 1 {
		t.Fatalf("written = %+v", st.written)
	}

	// Later lines are read from where the last pass stopped.
	appendTo(t, path, notice("toolu_3", true))
	got, _, _ = r.Read(context.Background(), worker, now.Add(time.Minute))
	if got.Running() {
		t.Fatalf("after the notice: %+v", got)
	}
}

// The process table settles a stale start: no background shell lives under
// the worker, so nothing runs. A table that cannot be read trusts the file.
func TestReadDropsAStartWhoseShellIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, path, start("toolu_1", "sleep 600", "2026-11-27T03:00:00Z"))
	now := time.Date(2026, 11, 27, 4, 0, 0, 0, time.UTC)
	gone, _, _ := newReader(t, &fakeStore{paths: []string{path}}, fakeShells{n: 0, ok: true}).Read(context.Background(), worker, now)
	if gone.Running() {
		t.Fatalf("with no live shell: %+v", gone)
	}
	unknown, _, _ := newReader(t, &fakeStore{paths: []string{path}}, fakeShells{ok: false}).Read(context.Background(), worker, now)
	if !unknown.Running() {
		t.Fatalf("with no process table: %+v", unknown)
	}
}

// A session without a conversation file is not known, and its old reading
// is dropped.
func TestReadWithoutAConversationFileIsUnknown(t *testing.T) {
	st := &fakeStore{paths: []string{filepath.Join(t.TempDir(), "missing.jsonl")}}
	r := newReader(t, st, fakeShells{ok: false})
	if _, ok, err := r.Read(context.Background(), worker, time.Now()); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want unknown", ok, err)
	}
	if st.forgotten != 1 {
		t.Fatalf("forgotten %d times, want 1", st.forgotten)
	}
	r2 := newReader(t, &fakeStore{}, fakeShells{ok: false})
	if _, ok, err := r2.Read(context.Background(), worker, time.Now()); ok || err != nil {
		t.Fatalf("no sources: ok=%v err=%v, want unknown", ok, err)
	}
}

// A replaced (shorter) file is read again from its start.
func TestReadStartsOverOnAReplacedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, path, start("toolu_1", "sleep 1", "2026-11-27T03:00:00Z")+start("toolu_2", "sleep 2", "2026-11-27T03:00:01Z"))
	r := newReader(t, &fakeStore{paths: []string{path}}, fakeShells{ok: false})
	now := time.Date(2026, 11, 27, 4, 0, 0, 0, time.UTC)
	if got, _, _ := r.Read(context.Background(), worker, now); len(got.Tasks) != 2 {
		t.Fatalf("got %+v", got)
	}
	if err := os.WriteFile(path, []byte(start("toolu_9", "sleep 9", "2026-11-27T03:30:00Z")), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.Read(context.Background(), worker, now)
	if len(got.Tasks) != 1 || got.Tasks[0].Description != "sleep 9" {
		t.Fatalf("after the file was replaced: %+v", got)
	}
}

// The process probe counts the background shells under a session's chat host.
func TestProcShellsCountsShellsUnderTheChatHost(t *testing.T) {
	root := t.TempDir()
	proc := func(pid, ppid int, comm string, argv ...string) {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "stat"), []byte(strconv.Itoa(pid)+" ("+comm+") S "+strconv.Itoa(ppid)+" 0 0"), 0o600)
	}
	proc(100, 1, "ao", "/x/ao", "chat-host", "water_ai-54", "/data", "/wt", "--", "node")
	proc(101, 100, "node", "node", "acp")
	proc(102, 101, "claude", "claude", "--output-format")
	proc(103, 102, "zsh", "/usr/bin/zsh", "-c", "source /home/u/.claude/shell-snapshots/snapshot-zsh-1.sh && eval 'bash wait.sh'")
	proc(104, 103, "bash", "bash", "wait.sh")
	proc(105, 102, "atuin", "atuin", "mcp")
	proc(200, 1, "ao", "/x/ao", "chat-host", "water_ai-55", "/data", "/wt")
	proc(201, 200, "zsh", "/usr/bin/zsh", "-c", "source /home/u/.claude/shell-snapshots/s.sh")
	shells := &ProcShells{Root: root}
	if n, ok := shells.LiveBackgroundShells("water_ai-54"); !ok || n != 1 {
		t.Fatalf("water_ai-54: n=%d ok=%v, want 1", n, ok)
	}
	if n, ok := shells.LiveBackgroundShells("water_ai-56"); ok || n != 0 {
		t.Fatalf("a session without a chat host: n=%d ok=%v, want unknown", n, ok)
	}
}

func subagent(id, description, at string) string {
	line, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": at,
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_use", "id": id, "name": "Agent",
			"input": map[string]any{"description": description, "prompt": "p", "run_in_background": true},
		}}},
	})
	return string(line) + "\n"
}

// Review findings (2026-11-27): no live shell drops only shell tasks; a
// notice kept only in a queue-operation record ends its task; only the newest
// conversation file counts; a start the process table cannot settle is
// trusted only so long; a Bash description goes before the command.
func TestReadReviewFindings(t *testing.T) {
	now := time.Date(2026, 11, 27, 4, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	// A background subagent has no shell to find.
	p1 := filepath.Join(dir, "a.jsonl")
	appendTo(t, p1, start("toolu_s", "sleep 600", "2026-11-27T03:00:00Z")+subagent("toolu_a", "review the plan", "2026-11-27T03:10:00Z"))
	got, _, _ := newReader(t, &fakeStore{paths: []string{p1}}, fakeShells{n: 0, ok: true}).Read(context.Background(), worker, now)
	if len(got.Tasks) != 1 || got.Tasks[0].Type != domain.BackgroundTaskSubagent || got.Tasks[0].Description != "review the plan" {
		t.Fatalf("with no shell: %+v, want the subagent only", got.Tasks)
	}

	// A notice kept as a queue-operation's content.
	p2 := filepath.Join(dir, "b.jsonl")
	queued, _ := json.Marshal(map[string]any{"type": "queue-operation", "operation": "enqueue",
		"content": "<task-notification>\n<tool-use-id>toolu_q</tool-use-id>\n</task-notification>"})
	appendTo(t, p2, start("toolu_q", "sleep 1", "2026-11-27T03:00:00Z")+string(queued)+"\n")
	if got, _, _ := newReader(t, &fakeStore{paths: []string{p2}}, fakeShells{n: 1, ok: true}).Read(context.Background(), worker, now); got.Running() {
		t.Fatalf("a queue-operation notice did not end its task: %+v", got.Tasks)
	}

	// Only the newest conversation file counts: an older one (before /clear)
	// may hold starts that never got a notice.
	old := filepath.Join(dir, "old.jsonl")
	appendTo(t, old, start("toolu_old", "sleep 9999", "2026-11-26T03:00:00Z"))
	_ = os.Chtimes(old, now.Add(-24*time.Hour), now.Add(-24*time.Hour))
	cur := filepath.Join(dir, "cur.jsonl")
	appendTo(t, cur, start("toolu_cur", "bash wait.sh", "2026-11-27T03:30:00Z"))
	got, _, _ = newReader(t, &fakeStore{paths: []string{old, cur}}, fakeShells{n: 1, ok: true}).Read(context.Background(), worker, now)
	if len(got.Tasks) != 1 || got.Tasks[0].Description != "bash wait.sh" {
		t.Fatalf("read the older file too: %+v", got.Tasks)
	}

	// Without a process table, a start older than the limit is not trusted.
	p3 := filepath.Join(dir, "c.jsonl")
	appendTo(t, p3, start("toolu_x", "sleep 1", "2026-11-26T20:00:00Z")+start("toolu_y", "sleep 2", "2026-11-27T03:59:00Z"))
	got, _, _ = newReader(t, &fakeStore{paths: []string{p3}}, fakeShells{ok: false}).Read(context.Background(), worker, now)
	if len(got.Tasks) != 1 || got.Tasks[0].Description != "sleep 2" {
		t.Fatalf("an unverifiable old start was trusted: %+v", got.Tasks)
	}

	// The Bash tool's description, when given, goes before the command.
	p4 := filepath.Join(dir, "d.jsonl")
	line, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-11-27T03:00:00Z",
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "toolu_d", "name": "Bash",
			"input": map[string]any{"command": "PGPASSWORD=s3cret psql -c 'select 1'", "description": "Run the drill query", "run_in_background": true}}}}})
	appendTo(t, p4, string(line)+"\n")
	got, _, _ = newReader(t, &fakeStore{paths: []string{p4}}, fakeShells{n: 1, ok: true}).Read(context.Background(), worker, now)
	if len(got.Tasks) != 1 || got.Tasks[0].Description != "Run the drill query" {
		t.Fatalf("description = %+v", got.Tasks)
	}
}
