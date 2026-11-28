// Package ladobackground reads a LADO worker's live background work when its
// Claude ACP bridge cannot report it (LADO fork, 2026-11-27). A worker
// started before AO's bridge patch keeps the old bridge process, so AO hears
// nothing of its background shells; the LADO watch would take it as stalled
// or waiting on its owner. This package reads the worker's conversation file
// (a run_in_background tool call without its task notification yet runs) and
// settles it against the process table (a background shell still lives under
// the worker's chat host). The bridge's own report always wins
// (store.SetLadoSessionBackgroundReading).
package ladobackground

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// maxReadPerPass bounds one pass over a conversation file; the rest is read
// on the next tick.
const maxReadPerPass = 16 << 20

// DefaultMaxUnverifiedAge is how long a start the process table cannot
// settle is trusted: three times the default stall time.
const DefaultMaxUnverifiedAge = 6 * time.Hour

// Store lists a session's conversation files and keeps the reading.
type Store interface {
	ListLadoSessionTranscriptSources(ctx context.Context, session domain.SessionID) ([]domain.LadoTranscriptSource, error)
	SetLadoSessionBackgroundReading(ctx context.Context, id domain.SessionID, tasks []domain.BackgroundTask, at time.Time) error
	ForgetLadoSessionBackgroundReading(ctx context.Context, id domain.SessionID) error
}

// Shells counts the background shells alive under a session's agent; ok is
// false when the process table cannot tell.
type Shells interface {
	LiveBackgroundShells(session domain.SessionID) (n int, ok bool)
}

// Deps wires a Reader.
type Deps struct {
	Store Store
	// Shells settles a stale shell start; nil trusts the conversation file.
	Shells Shells
	// MaxUnverifiedAge bounds a start the process table cannot settle; 0 is
	// DefaultMaxUnverifiedAge.
	MaxUnverifiedAge time.Duration
	Log              *slog.Logger
}

// Reader reads background work from conversation files, each from where
// its last pass stopped.
type Reader struct {
	deps  Deps
	mu    sync.Mutex
	files map[string]*fileState
}

type fileState struct {
	offset int64
	// open are the run_in_background calls without a notice, by tool use id.
	open map[string]domain.BackgroundTask
}

// New returns a Reader.
func New(deps Deps) *Reader {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.MaxUnverifiedAge <= 0 {
		deps.MaxUnverifiedAge = DefaultMaxUnverifiedAge
	}
	return &Reader{deps: deps, files: map[string]*fileState{}}
}

// Read returns the session's background work as its conversation files and
// the process table show it at now, and records it. ok is false when the
// session has no readable Claude conversation file.
func (r *Reader) Read(ctx context.Context, id domain.SessionID, now time.Time) (domain.SessionBackgroundTasks, bool, error) {
	sources, err := r.deps.Store.ListLadoSessionTranscriptSources(ctx, id)
	if err != nil {
		return domain.SessionBackgroundTasks{}, false, err
	}
	// Only the conversation the agent writes now: an older file (before a
	// /clear) can keep starts that never got their notice.
	path := newestClaudeMain(sources)
	if path == "" {
		r.forget(ctx, id)
		return domain.SessionBackgroundTasks{}, false, nil
	}
	tasks, err := r.readFile(path)
	if err != nil {
		return domain.SessionBackgroundTasks{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	tasks = r.settle(id, tasks, now)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].StartedAt.Before(tasks[j].StartedAt) })
	tasks = domain.NormalizeBackgroundTasks(append([]domain.BackgroundTask{}, tasks...))
	if err := r.deps.Store.SetLadoSessionBackgroundReading(ctx, id, tasks, now); err != nil {
		r.deps.Log.Warn("LADO could not record a worker's background reading", "session", id, "err", err)
	}
	return domain.SessionBackgroundTasks{SessionID: id, Tasks: tasks, Source: domain.BackgroundSourceTranscript, UpdatedAt: now.UTC()}, true, nil
}

func (r *Reader) forget(ctx context.Context, id domain.SessionID) {
	if err := r.deps.Store.ForgetLadoSessionBackgroundReading(ctx, id); err != nil {
		r.deps.Log.Warn("LADO could not drop a worker's background reading", "session", id, "err", err)
	}
}

// settle checks the open starts against the process table: without a live
// background shell the shell starts are stale (a subagent has no shell to
// find). A table that cannot tell trusts a start only so long.
func (r *Reader) settle(id domain.SessionID, tasks []domain.BackgroundTask, now time.Time) []domain.BackgroundTask {
	if len(tasks) == 0 {
		return tasks
	}
	n, ok := 0, false
	if r.deps.Shells != nil {
		n, ok = r.deps.Shells.LiveBackgroundShells(id)
	}
	kept := tasks[:0]
	for _, task := range tasks {
		switch {
		case ok && n == 0 && task.Type == domain.BackgroundTaskShell:
			continue
		case !ok && !task.StartedAt.IsZero() && now.Sub(task.StartedAt) > r.deps.MaxUnverifiedAge:
			continue
		}
		kept = append(kept, task)
	}
	return kept
}

// newestClaudeMain is the most recently written Claude conversation file of
// the sources that exist, or "".
func newestClaudeMain(sources []domain.LadoTranscriptSource) string {
	var path string
	var newest time.Time
	for _, source := range sources {
		if source.Kind != domain.UsageSourceClaudeMain {
			continue
		}
		info, err := os.Stat(source.Path)
		if err != nil || info.IsDir() {
			continue
		}
		if path == "" || info.ModTime().After(newest) {
			path, newest = source.Path, info.ModTime()
		}
	}
	return path
}

// readFile reads path on from its offset and returns its open calls. A
// file shorter than the offset was replaced and is read from its start.
func (r *Reader) readFile(path string) ([]domain.BackgroundTask, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.files[path]
	if st == nil || info.Size() < st.offset {
		st = &fileState{open: map[string]domain.BackgroundTask{}}
		r.files[path] = st
	}
	if _, err := f.Seek(st.offset, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(io.LimitReader(f, maxReadPerPass), 1<<16)
	start := st.offset
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			st.offset += int64(len(line))
			st.apply(line)
		} else if st.offset == start && int64(len(line)) == maxReadPerPass {
			// One line longer than a whole pass would stop the reader for good:
			// skip it (it is no record the watch reads).
			st.offset += int64(len(line))
		}
		if err != nil {
			break // EOF, the pass limit, or a partial last line read next time
		}
	}
	out := make([]domain.BackgroundTask, 0, len(st.open))
	for _, task := range st.open {
		out = append(out, task)
	}
	return out, nil
}

var noticeToolUse = regexp.MustCompile(`<tool-use-id>([^<\s]+)</tool-use-id>`)

// apply reads one conversation record. A broken line is skipped.
func (st *fileState) apply(line []byte) {
	var record struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(line, &record) != nil {
		return
	}
	if record.Type == "queue-operation" {
		// A notice queued while a turn ran can stay only here.
		var text string
		if json.Unmarshal(record.Content, &text) == nil {
			st.ended(text)
		}
		return
	}
	if len(record.Message) == 0 {
		return
	}
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(record.Message, &message) != nil || len(message.Content) == 0 {
		return
	}
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		st.ended(text)
		return
	}
	var blocks []struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Text  string `json:"text"`
		Input struct {
			Command         string `json:"command"`
			Description     string `json:"description"`
			RunInBackground bool   `json:"run_in_background"`
		} `json:"input"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		return
	}
	for _, block := range blocks {
		switch block.Type {
		case "tool_use":
			if !block.Input.RunInBackground || block.ID == "" {
				continue
			}
			kind := domain.BackgroundTaskShell
			// The tool's own description first: a command line can carry a secret.
			description := firstLine(block.Input.Description)
			if description == "" {
				description = firstLine(block.Input.Command)
			}
			if block.Name != "Bash" {
				kind = domain.BackgroundTaskSubagent
				description = firstLine(block.Input.Description)
			}
			started, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
			st.open[block.ID] = domain.BackgroundTask{Type: kind, Description: description, StartedAt: started.UTC()}
		case "text":
			st.ended(block.Text)
		}
	}
}

func (st *fileState) ended(text string) {
	if !strings.Contains(text, "<task-notification>") {
		return
	}
	for _, m := range noticeToolUse.FindAllStringSubmatch(text, -1) {
		delete(st.open, m[1])
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
