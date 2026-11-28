package domain

// LADO fork (2026-11-27): a session idle on its own background work. A worker
// that ends its turn while a background shell, Monitor or subagent it started
// still runs is idle, but waits on that work, not on its owner: AO shows it
// 💻 and the LADO stall watch leaves it alone. The facts come from the Claude
// ACP bridge's live task registry (frontend/scripts/build-acp-runtime-helpers.mjs,
// patchClaudeBackgroundTaskReport) and live in lado_session_background
// (migrations_lado/0025).

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Background task kinds the Claude SDK reports. An unknown kind is kept as
// the SDK named it.
const (
	BackgroundTaskShell    = "shell"
	BackgroundTaskSubagent = "subagent"
	BackgroundTaskMonitor  = "monitor"
	BackgroundTaskWorkflow = "workflow"
)

// Limits of what AO keeps of a session's background tasks.
const (
	BackgroundTasksMax           = 8
	BackgroundTaskDescriptionMax = 80
	backgroundTaskTypeMax        = 32
)

// BackgroundTask is one live background task of a session.
type BackgroundTask struct {
	Type        string    `json:"type"`
	Description string    `json:"description"`
	StartedAt   time.Time `json:"startedAt"`
}

// Where AO heard of a session's background tasks.
const (
	// BackgroundSourceBridge is the Claude ACP bridge's own report.
	BackgroundSourceBridge = "bridge"
	// BackgroundSourceTranscript is what the LADO watch read of a worker
	// whose bridge cannot report: its conversation file and process table.
	BackgroundSourceTranscript = "transcript"
)

// SessionBackgroundTasks is what AO last heard of a session's live
// background tasks. No row means AO does not know (an older bridge, or no
// live provider).
type SessionBackgroundTasks struct {
	SessionID SessionID        `json:"sessionId"`
	Tasks     []BackgroundTask `json:"tasks"`
	Source    string           `json:"source" enum:"bridge,transcript"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// Running reports whether any background task runs.
func (b SessionBackgroundTasks) Running() bool { return len(b.Tasks) > 0 }

// Since is when the oldest running task started; zero without one, or when no
// task names its start.
func (b SessionBackgroundTasks) Since() time.Time {
	var since time.Time
	for _, t := range b.Tasks {
		if t.StartedAt.IsZero() {
			continue
		}
		if since.IsZero() || t.StartedAt.Before(since) {
			since = t.StartedAt
		}
	}
	return since
}

// NormalizeBackgroundTasks trims a reported list to what AO keeps: at most
// BackgroundTasksMax tasks, a short kind and description without control
// characters, and UTC start times. A nil list stays nil (unknown).
func NormalizeBackgroundTasks(tasks []BackgroundTask) []BackgroundTask {
	if tasks == nil {
		return nil
	}
	out := make([]BackgroundTask, 0, min(len(tasks), BackgroundTasksMax))
	for _, t := range tasks {
		if len(out) == BackgroundTasksMax {
			break
		}
		out = append(out, BackgroundTask{
			Type:        clipRunes(oneLine(t.Type), backgroundTaskTypeMax),
			Description: clipRunes(oneLine(t.Description), BackgroundTaskDescriptionMax),
			StartedAt:   t.StartedAt.UTC(),
		})
	}
	return out
}

// oneLine keeps s on one line: control characters, newlines included, become
// spaces and runs of spaces one.
func oneLine(s string) string {
	return SanitizeControlChars(strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}), " "))
}

func clipRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit-1]) + "…"
}
