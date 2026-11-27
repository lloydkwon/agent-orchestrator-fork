package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeBackgroundTasksKeepsOneShortLine(t *testing.T) {
	if NormalizeBackgroundTasks(nil) != nil {
		t.Fatal("nil (unknown) became known")
	}
	if got := NormalizeBackgroundTasks([]BackgroundTask{}); got == nil || len(got) != 0 {
		t.Fatalf("an empty list = %#v, want empty and known", got)
	}
	at := time.Date(2026, 11, 27, 12, 1, 8, 0, time.FixedZone("KST", 9*3600))
	many := make([]BackgroundTask, BackgroundTasksMax+3)
	many[0] = BackgroundTask{Type: " shell ", Description: "bash wait.sh\n\t7000  " + strings.Repeat("x", 200), StartedAt: at}
	got := NormalizeBackgroundTasks(many)
	if len(got) != BackgroundTasksMax {
		t.Fatalf("kept %d tasks, want %d", len(got), BackgroundTasksMax)
	}
	d := got[0].Description
	if !strings.HasPrefix(d, "bash wait.sh 7000 x") || len([]rune(d)) != BackgroundTaskDescriptionMax || !strings.HasSuffix(d, "…") {
		t.Fatalf("description = %q", d)
	}
	if got[0].Type != "shell" || got[0].StartedAt.Location() != time.UTC || !got[0].StartedAt.Equal(at) {
		t.Fatalf("task = %+v", got[0])
	}
}

func TestSessionBackgroundTasksSinceIsTheOldestStart(t *testing.T) {
	t0 := time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)
	b := SessionBackgroundTasks{Tasks: []BackgroundTask{{StartedAt: t0.Add(time.Hour)}, {}, {StartedAt: t0}}}
	if !b.Running() || !b.Since().Equal(t0) {
		t.Fatalf("running=%v since=%v", b.Running(), b.Since())
	}
	if (SessionBackgroundTasks{}).Running() {
		t.Fatal("no tasks runs")
	}
}
